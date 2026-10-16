package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// staleClone builds an origin whose main is behind commits ahead of a fresh clone.
func staleClone(t *testing.T, behind int) (origin, seed, clone string) {
	root := t.TempDir()
	origin, seed, clone = filepath.Join(root, "origin.git"), filepath.Join(root, "seed"), filepath.Join(root, "clone")
	run(t, root, "init", "-q", "--bare", "-b", "main", origin)
	run(t, root, "clone", "-q", origin, seed)
	writeFile(t, filepath.Join(seed, "AGENTS.md"), "old guidance")
	run(t, seed, "add", ".")
	run(t, seed, "commit", "-q", "-m", "one")
	run(t, seed, "push", "-q", "origin", "main")
	run(t, root, "clone", "-q", origin, clone)
	for i := 0; i < behind; i++ {
		writeFile(t, filepath.Join(seed, "AGENTS.md"), "new guidance "+strings.Repeat("x", i))
		run(t, seed, "commit", "-q", "-am", "next")
	}
	run(t, seed, "push", "-q", "origin", "main")
	return origin, seed, clone
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSyncDefaultBranchFastForwardsAStaleCheckout(t *testing.T) {
	_, seed, clone := staleClone(t, 30)
	ctx := context.Background()
	if err := (Git{}).SyncDefaultBranch(ctx, clone, "main"); err != nil {
		t.Fatal(err)
	}
	if got, want := run(t, clone, "rev-parse", "main"), run(t, seed, "rev-parse", "HEAD"); got != want {
		t.Errorf("main = %s, want origin head %s", got, want)
	}
	if got := read(t, filepath.Join(clone, "AGENTS.md")); !strings.HasPrefix(got, "new guidance") {
		t.Errorf("AGENTS.md = %q, want the current file", got)
	}
	if err := (Git{}).SyncDefaultBranch(ctx, clone, "main"); err != nil {
		t.Fatalf("second sync: %v", err)
	}
}

func TestSyncDefaultBranchStopsWhenTheFetchFails(t *testing.T) {
	origin, _, clone := staleClone(t, 1)
	if err := os.RemoveAll(origin); err != nil {
		t.Fatal(err)
	}
	err := (Git{}).SyncDefaultBranch(context.Background(), clone, "main")
	if err == nil || !strings.Contains(err.Error(), "fetch origin main") {
		t.Fatalf("err = %v", err)
	}
	if got := read(t, filepath.Join(clone, "AGENTS.md")); got != "old guidance" {
		t.Errorf("AGENTS.md = %q", got)
	}
}

func TestSyncDefaultBranchStopsWhenLocalMainHasDiverged(t *testing.T) {
	_, _, clone := staleClone(t, 1)
	run(t, clone, "commit", "-q", "--allow-empty", "-m", "local only")
	local := run(t, clone, "rev-parse", "main")
	err := (Git{}).SyncDefaultBranch(context.Background(), clone, "main")
	if err == nil || !strings.Contains(err.Error(), "diverged") {
		t.Fatalf("err = %v", err)
	}
	if got := run(t, clone, "rev-parse", "main"); got != local {
		t.Errorf("main moved to %s", got)
	}
}

func TestSyncDefaultBranchKeepsUncommittedChanges(t *testing.T) {
	_, _, clone := staleClone(t, 1)
	writeFile(t, filepath.Join(clone, "AGENTS.md"), "my edit")
	err := (Git{}).SyncDefaultBranch(context.Background(), clone, "main")
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("err = %v", err)
	}
	if got := read(t, filepath.Join(clone, "AGENTS.md")); got != "my edit" {
		t.Errorf("AGENTS.md = %q, want the local edit kept", got)
	}
}

func TestSyncDefaultBranchStopsWhenAnotherBranchIsCheckedOut(t *testing.T) {
	_, seed, clone := staleClone(t, 1)
	run(t, clone, "switch", "-q", "-c", "work")
	err := (Git{}).SyncDefaultBranch(context.Background(), clone, "main")
	if err == nil || !strings.Contains(err.Error(), "on work, not main") {
		t.Fatalf("err = %v", err)
	}
	if got := run(t, clone, "branch", "--show-current"); got != "work" {
		t.Errorf("checked out %s", got)
	}
	if got, want := run(t, clone, "rev-parse", "main"), run(t, seed, "rev-parse", "HEAD"); got != want {
		t.Errorf("main = %s, want it advanced to %s", got, want)
	}
}
