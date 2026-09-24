package worktree

import (
	"context"
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
