package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/app"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// repoWithOrigin builds a bare "origin" whose main is one commit ahead of the local clone,
// proving Ensure fetches before branching.
func repoWithOrigin(t *testing.T) (clone, originHead string) {
	root := t.TempDir()
	origin, seed := filepath.Join(root, "origin.git"), filepath.Join(root, "seed")
	clone = filepath.Join(root, "clone")
	run(t, root, "init", "-q", "--bare", "-b", "main", origin)
	run(t, root, "clone", "-q", origin, seed)
	run(t, seed, "commit", "-q", "--allow-empty", "-m", "one")
	run(t, seed, "push", "-q", "origin", "main")
	run(t, root, "clone", "-q", origin, clone)
	run(t, seed, "commit", "-q", "--allow-empty", "-m", "two")
	run(t, seed, "push", "-q", "origin", "main")
	return clone, run(t, seed, "rev-parse", "HEAD")
}

func TestEnsureCreatesFromFreshBaseAndIsIdempotent(t *testing.T) {
	clone, originHead := repoWithOrigin(t)
	req := app.WorktreeRequest{
		RepoDir: clone,
		Path:    filepath.Join(t.TempDir(), "ridezoomo-armada", "825-provider-retries"),
		Branch:  "agent/825-provider-retries",
		BaseRef: "origin/main",
	}
	ctx := context.Background()
	if err := (Git{}).Ensure(ctx, req); err != nil {
		t.Fatal(err)
	}
	if got := run(t, req.Path, "rev-parse", "HEAD"); got != originHead {
		t.Errorf("worktree HEAD = %s, want fetched origin/main %s", got, originHead)
	}
	if got := run(t, req.Path, "branch", "--show-current"); got != req.Branch {
		t.Errorf("branch = %s", got)
	}
	if err := (Git{}).Ensure(ctx, req); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}

	os.RemoveAll(req.Path)
	if err := (Git{}).Ensure(ctx, req); err != nil {
		t.Fatalf("Ensure after worktree dir removed (branch kept): %v", err)
	}
	if got := run(t, req.Path, "branch", "--show-current"); got != req.Branch {
		t.Errorf("recreated branch = %s", got)
	}
}

func TestEnsureRefusesConflicts(t *testing.T) {
	clone, _ := repoWithOrigin(t)
	ctx := context.Background()
	base := t.TempDir()
	req := app.WorktreeRequest{RepoDir: clone, Path: filepath.Join(base, "825-a"), Branch: "agent/825-a", BaseRef: "origin/main"}
	if err := (Git{}).Ensure(ctx, req); err != nil {
		t.Fatal(err)
	}

	sameBranchElsewhere := req
	sameBranchElsewhere.Path = filepath.Join(base, "825-b")
	if err := (Git{}).Ensure(ctx, sameBranchElsewhere); err == nil || !strings.Contains(err.Error(), "already checked out") {
		t.Errorf("same branch, other path: %v", err)
	}

	otherBranchSamePath := req
	otherBranchSamePath.Branch = "agent/826-x"
	if err := (Git{}).Ensure(ctx, otherBranchSamePath); err == nil || !strings.Contains(err.Error(), "not agent/826-x") {
		t.Errorf("other branch, same path: %v", err)
	}

	stray := filepath.Join(base, "stray")
	os.MkdirAll(stray, 0o755)
	strayReq := app.WorktreeRequest{RepoDir: clone, Path: stray, Branch: "agent/9-s", BaseRef: "origin/main"}
	if err := (Git{}).Ensure(ctx, strayReq); err == nil || !strings.Contains(err.Error(), "not a worktree") {
		t.Errorf("stray dir: %v", err)
	}
}

func TestRemove(t *testing.T) {
	clone, _ := repoWithOrigin(t)
	ctx := context.Background()
	req := app.WorktreeRequest{RepoDir: clone, Path: filepath.Join(t.TempDir(), "825-a"), Branch: "agent/825-a", BaseRef: "origin/main"}
	if err := (Git{}).Ensure(ctx, req); err != nil {
		t.Fatal(err)
	}
	rm := app.WorktreeRemoval{Path: req.Path, Branch: req.Branch}

	os.WriteFile(filepath.Join(req.Path, "notes.txt"), []byte("wip"), 0o644)
	if err := (Git{}).Remove(ctx, rm); !errors.Is(err, app.ErrWorktreeDirty) {
		t.Fatalf("untracked work must block removal, got %v", err)
	}
	os.Remove(filepath.Join(req.Path, "notes.txt"))

	if err := (Git{}).Remove(ctx, rm); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(req.Path); !os.IsNotExist(err) {
		t.Errorf("worktree still on disk: %v", err)
	}
	if strings.Contains(run(t, clone, "worktree", "list"), "825-a") {
		t.Error("worktree still registered")
	}
	if run(t, clone, "branch", "--list", req.Branch) == "" {
		t.Error("branch deleted without DeleteBranch")
	}
	if err := (Git{}).Remove(ctx, rm); err != nil {
		t.Errorf("removing again must be a no-op: %v", err)
	}

	if err := (Git{}).Ensure(ctx, req); err != nil {
		t.Fatal(err)
	}
	rm.DeleteBranch = true
	if err := (Git{}).Remove(ctx, rm); err != nil {
		t.Fatal(err)
	}
	if run(t, clone, "branch", "--list", req.Branch) != "" {
		t.Error("branch kept with DeleteBranch")
	}
}

// prWorktree builds a bare origin with main and a pushed task branch, and a worktree on
// the branch. seed is a clone that can push new commits to either.
func prWorktree(t *testing.T) (wt, seed string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	seed, wt = filepath.Join(root, "seed"), filepath.Join(root, "wt")
	run(t, root, "init", "-q", "--bare", "-b", "main", origin)
	run(t, root, "clone", "-q", origin, seed)
	write(t, seed, "a.txt", "base\n")
	write(t, seed, "b.txt", "base\n")
	run(t, seed, "add", ".")
	run(t, seed, "commit", "-q", "-m", "base")
	run(t, seed, "push", "-q", "origin", "main")
	run(t, seed, "checkout", "-q", "-b", "agent/1-x")
	write(t, seed, "a.txt", "branch\n")
	write(t, seed, "b.txt", "branch\n")
	run(t, seed, "commit", "-q", "-am", "branch")
	run(t, seed, "push", "-q", "origin", "agent/1-x")
	run(t, seed, "checkout", "-q", "main")
	run(t, root, "clone", "-q", "-b", "agent/1-x", origin, wt)
	return wt, seed
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func advanceMain(t *testing.T, seed string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		write(t, seed, name, body)
	}
	run(t, seed, "add", ".")
	run(t, seed, "commit", "-q", "-m", "main moves")
	run(t, seed, "push", "-q", "origin", "main")
}

var check = app.MergeCheck{Branch: "agent/1-x", Base: "main"}

func TestMergeConflictsListsConflictingFiles(t *testing.T) {
	wt, seed := prWorktree(t)
	advanceMain(t, seed, map[string]string{"b.txt": "main\n", "a.txt": "main\n", "c.txt": "new\n"})
	c := check
	c.Dir = wt
	got, err := (Git{}).MergeConflicts(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a.txt,b.txt" {
		t.Errorf("conflicts = %v, want [a.txt b.txt]", got)
	}
}

func TestMergeConflictsCleanWhenBaseAdvances(t *testing.T) {
	wt, seed := prWorktree(t)
	advanceMain(t, seed, map[string]string{"c.txt": "new\n"})
	head := run(t, wt, "rev-parse", "HEAD")
	c := check
	c.Dir = wt
	got, err := (Git{}).MergeConflicts(context.Background(), c)
	if err != nil || got != nil {
		t.Fatalf("conflicts = %v, %v; want none", got, err)
	}
	if after := run(t, wt, "rev-parse", "HEAD"); after != head {
		t.Errorf("HEAD moved from %s to %s", head, after)
	}
	if status := run(t, wt, "status", "--porcelain"); status != "" {
		t.Errorf("worktree changed:\n%s", status)
	}
}

func TestMergeConflictsFetchesLatestBase(t *testing.T) {
	wt, seed := prWorktree(t)
	c := check
	c.Dir = wt
	if got, err := (Git{}).MergeConflicts(context.Background(), c); err != nil || got != nil {
		t.Fatalf("before main moved: %v, %v", got, err)
	}
	advanceMain(t, seed, map[string]string{"a.txt": "main\n"})
	got, err := (Git{}).MergeConflicts(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a.txt" {
		t.Errorf("conflicts = %v, want [a.txt] from the fetched main", got)
	}
}

func TestMergeConflictsErrorsOnMissingBranch(t *testing.T) {
	wt, _ := prWorktree(t)
	c := app.MergeCheck{Dir: wt, Branch: "agent/gone", Base: "main"}
	if _, err := (Git{}).MergeConflicts(context.Background(), c); err == nil {
		t.Fatal("want an error for a branch origin doesn't have")
	}
}
