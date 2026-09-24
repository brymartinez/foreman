// Package worktree gives each delegated task its own git worktree, so no two agents
// ever share a checkout.
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/brymartinez/foreman/internal/app"
)

type Git struct{}

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
	if _, err := git(ctx, req.RepoDir, "show-ref", "--verify", "--quiet", ref); err == nil {
		_, err := git(ctx, req.RepoDir, "worktree", "add", req.Path, req.Branch)
		return err
	}
	if remote, branch, ok := strings.Cut(req.BaseRef, "/"); ok {
		if _, err := git(ctx, req.RepoDir, "fetch", remote, branch); err != nil {
			return err
		}
	}
	_, err = git(ctx, req.RepoDir, "worktree", "add", "--no-track", "-b", req.Branch, req.Path, req.BaseRef)
	return err
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
