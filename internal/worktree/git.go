// Package worktree gives each delegated task its own git worktree, so no two agents
// ever share a checkout.
package worktree

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/brymartinez/foreman/internal/app"
)

type Git struct{}

func (Git) Head(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "HEAD")
	return strings.TrimSpace(string(out)), err
}

// Checkpoint verifies the agent committed on the expected branch and left no
// tracked or untracked work behind. Its new head must extend the previous one.
func (g Git) Checkpoint(ctx context.Context, dir, branch, prior string) (string, error) {
	return g.committedHead(ctx, dir, branch, prior, false)
}

// ReviewHead also accepts the last checkpoint itself: that commit may contain
// the entire change, even if a later turn only validated it.
func (g Git) ReviewHead(ctx context.Context, dir, branch, prior string) (string, error) {
	return g.committedHead(ctx, dir, branch, prior, true)
}

func (g Git) committedHead(ctx context.Context, dir, branch, prior string, allowSame bool) (string, error) {
	ref, err := git(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("checkpoint branch: %w", err)
	}
	if got := strings.TrimSpace(string(ref)); got != branch {
		return "", fmt.Errorf("checkpoint is on branch %s, not %s", got, branch)
	}
	status, err := git(ctx, dir, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	if len(status) > 0 {
		return "", errors.New("checkpoint has uncommitted or untracked files")
	}
	head, err := g.Head(ctx, dir)
	if err != nil {
		return "", err
	}
	if prior == "" || (head == prior && !allowSame) {
		return "", errors.New("checkpoint has no new commit")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "merge-base", "--is-ancestor", prior, head)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("checkpoint commit does not extend %s: %w", prior, err)
	}
	return head, nil
}

func (Git) Ensure(ctx context.Context, req app.WorktreeRequest) error {
	if _, err := git(ctx, req.RepoDir, "worktree", "prune"); err != nil {
		return err
	}
	list, err := git(ctx, req.RepoDir, "worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	want := canonical(req.Path)
	ref := "refs/heads/" + req.Branch
	for _, wt := range parsePorcelain(list) {
		samePath := canonical(wt.path) == want
		switch {
		case samePath && wt.branch == ref:
			return nil
		case samePath:
			return fmt.Errorf("%s is a worktree on %s, not %s", req.Path, strings.TrimPrefix(wt.branch, "refs/heads/"), req.Branch)
		case wt.branch == ref:
			return fmt.Errorf("branch %s is already checked out at %s", req.Branch, wt.path)
		}
	}
	if _, err := os.Stat(req.Path); err == nil {
		return fmt.Errorf("%s exists and is not a worktree of this repository", req.Path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(req.Path), 0o755); err != nil {
		return err
	}
	remote, base, fetch := strings.Cut(req.BaseRef, "/")
	// A base of the branch's own remote copy continues a PR, which starts at its head.
	onRemote := fetch && base == req.Branch
	if _, err := git(ctx, req.RepoDir, "show-ref", "--verify", "--quiet", ref); err == nil {
		if onRemote {
			if err := fastForward(ctx, req, remote, base); err != nil {
				return err
			}
		}
		_, err := git(ctx, req.RepoDir, "worktree", "add", req.Path, req.Branch)
		return err
	}
	if req.BaseRef == "" {
		return fmt.Errorf("branch %s does not exist in %s", req.Branch, req.RepoDir)
	}
	if fetch {
		if _, err := git(ctx, req.RepoDir, "fetch", remote, base); err != nil {
			return err
		}
	}
	_, err = git(ctx, req.RepoDir, "worktree", "add", "--no-track", "-b", req.Branch, req.Path, req.BaseRef)
	return err
}

// fastForward moves a local branch left from an earlier run up to the fetched remote
// head. A local branch with commits the remote lacks is refused, not overwritten.
// Ensure already refused a branch checked out elsewhere, so moving it is safe.
func fastForward(ctx context.Context, req app.WorktreeRequest, remote, branch string) error {
	if _, err := git(ctx, req.RepoDir, "fetch", remote, branch); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", req.RepoDir, "merge-base", "--is-ancestor", "refs/heads/"+req.Branch, req.BaseRef)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("local branch %s in %s has commits that %s lacks; remove or rename it, then start again: %w", req.Branch, req.RepoDir, req.BaseRef, err)
	}
	_, err := git(ctx, req.RepoDir, "branch", "--force", "--no-track", req.Branch, req.BaseRef)
	return err
}

func (Git) Remove(ctx context.Context, req app.WorktreeRemoval) error {
	common := ""
	if _, err := os.Stat(req.Path); errors.Is(err, fs.ErrNotExist) {
		if !req.DeleteBranch {
			return nil
		}
		if req.RepoDir == "" {
			return fmt.Errorf("%w: cannot delete branch %s: repository checkout is unknown", app.ErrBranchStatusUnknown, req.Branch)
		}
		// The worktree is gone but its branch may not be, so git runs from the checkout.
		out, err := git(ctx, req.RepoDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return fmt.Errorf("%w: cannot find checkout for branch %s: %v", app.ErrBranchStatusUnknown, req.Branch, err)
		}
		common = strings.TrimSpace(string(out))
		if _, err := git(ctx, common, "worktree", "prune"); err != nil {
			return fmt.Errorf("%w: cannot inspect worktrees for branch %s: %v", app.ErrBranchStatusUnknown, req.Branch, err)
		}
		exists, err := (Git{}).BranchExists(ctx, common, req.Branch)
		if err != nil {
			return fmt.Errorf("%w: cannot inspect branch %s: %v", app.ErrBranchStatusUnknown, req.Branch, err)
		}
		if !exists {
			return nil
		}
	} else if err != nil {
		return err
	} else {
		// The worktree can't remove itself, so git runs from the repository's shared .git.
		out, err := git(ctx, req.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return err
		}
		common = strings.TrimSpace(string(out))
		args := []string{"worktree", "remove", req.Path}
		// A forced removal discards work, so it takes only the worktree it was asked
		// for. Another worktree made at the same path is kept.
		if req.Force && req.RepoDir != "" {
			out, err := git(ctx, req.RepoDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
			if err != nil {
				return err
			}
			if want := strings.TrimSpace(string(out)); canonical(want) != canonical(common) {
				return fmt.Errorf("%s is a worktree of another repository (%s, not %s), so it was kept", req.Path, common, want)
			}
		}
		if req.Force && req.Branch != "" {
			held, err := heldBranch(ctx, req.Path)
			if err != nil {
				return err
			}
			if held != req.Branch {
				return fmt.Errorf("%s has %s checked out, not %s, so it was kept", req.Path, cmp.Or(held, "a detached HEAD"), req.Branch)
			}
		}
		if req.Force {
			// Twice: one --force keeps a locked worktree.
			args = []string{"worktree", "remove", "--force", "--force", req.Path}
		}
		if _, err := git(ctx, common, args...); err != nil {
			if strings.Contains(err.Error(), "contains modified or untracked files") {
				return app.ErrWorktreeDirty
			}
			return err
		}
	}
	if !req.DeleteBranch {
		return nil
	}
	// -D: a squash-merged branch is not an ancestor of the base, but its PR is merged.
	if _, err := git(ctx, common, "branch", "-D", req.Branch); err != nil {
		return fmt.Errorf("%w: %v", app.ErrBranchNotDeleted, err)
	}
	return nil
}

// heldBranch names the branch the worktree at dir has checked out. A rebase that
// stopped detaches HEAD, so its branch comes from the rebase state. Empty means a
// detached HEAD on no branch.
func heldBranch(ctx context.Context, dir string) (string, error) {
	if out, err := git(ctx, dir, "symbolic-ref", "-q", "--short", "HEAD"); err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	for _, state := range []string{"rebase-merge/head-name", "rebase-apply/head-name"} {
		out, err := git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-path", state)
		if err != nil {
			return "", err
		}
		if name, err := os.ReadFile(strings.TrimSpace(string(out))); err == nil {
			return strings.TrimPrefix(strings.TrimSpace(string(name)), "refs/heads/"), nil
		}
	}
	return "", nil
}

func (Git) CommonDir(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	return strings.TrimSpace(string(out)), err
}

// BranchExists asks show-ref, which exits 1 only for a missing ref; any other
// failure, such as a missing checkout, is an error.
func (Git) BranchExists(ctx context.Context, dir, branch string) (bool, error) {
	err := exec.CommandContext(ctx, "git", "-C", dir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	}
	return false, fmt.Errorf("check branch %s in %s: %w", branch, dir, err)
}

// MergeConflicts uses git merge-tree (git 2.38+), which merges in memory: exit 0 is a
// clean merge and exit 1 lists the conflicting files after the tree id.
func (Git) MergeConflicts(ctx context.Context, check app.MergeCheck) ([]string, error) {
	fetch, head := []string{"fetch", "origin", check.Base, check.Branch}, "origin/"+check.Branch
	if check.Local {
		fetch, head = []string{"fetch", "origin", check.Base}, "HEAD"
	}
	if _, err := git(ctx, check.Dir, fetch...); err != nil {
		return nil, err
	}
	args := []string{"merge-tree", "--write-tree", "--name-only", "--no-messages", head, "origin/" + check.Base}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", check.Dir}, args...)...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return nil, nil
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	seen := map[string]bool{}
	var files []string
	for _, f := range lines[1:] {
		if f != "" && !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("git %s reported a conflict without naming a file", strings.Join(args, " "))
	}
	return files, nil
}

func (Git) RemoteBranchHead(ctx context.Context, dir, branch string) (string, error) {
	out, err := git(ctx, dir, "ls-remote", "--heads", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	sha, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\t")
	return sha, nil
}

func (Git) Contains(ctx context.Context, dir, ancestor, descendant string) (bool, error) {
	if _, err := git(ctx, dir, "fetch", "origin"); err != nil {
		return false, err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "merge-base", "--is-ancestor", ancestor, descendant)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor: %w: %s", err, strings.TrimSpace(stderr.String()))
}

type entry struct{ path, branch string }

func parsePorcelain(out []byte) []entry {
	var entries []entry
	for _, block := range strings.Split(strings.TrimSpace(string(out)), "\n\n") {
		var e entry
		for _, line := range strings.Split(block, "\n") {
			if p, ok := strings.CutPrefix(line, "worktree "); ok {
				e.path = p
			}
			if b, ok := strings.CutPrefix(line, "branch "); ok {
				e.branch = b
			}
		}
		if e.path != "" {
			entries = append(entries, e)
		}
	}
	return entries
}

// canonical resolves symlinks (macOS /var -> /private/var) so paths git reports compare equal.
func canonical(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(dir, filepath.Base(p))
	}
	return filepath.Clean(p)
}

func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// ReviewRange reads the refs already fetched for the review's conflict check.
func (Git) ReviewRange(ctx context.Context, dir, baseRef, head, previous string) (app.ReviewRange, error) {
	base, err := git(ctx, dir, "rev-parse", "--verify", baseRef+"^{commit}")
	if err != nil {
		return app.ReviewRange{}, err
	}
	result := app.ReviewRange{BaseHead: strings.TrimSpace(string(base))}
	if previous == "" {
		return result, nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "merge-base", "--is-ancestor", previous, head)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		result.PreviousIsAncestor = true
		return result, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return result, nil
	default:
		return app.ReviewRange{}, fmt.Errorf("review ancestry: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
}
