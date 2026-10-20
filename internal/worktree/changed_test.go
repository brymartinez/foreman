package worktree

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func commitFile(t *testing.T, dir, path, content, msg string) string {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", msg)
	return run(t, dir, "rev-parse", "HEAD")
}

func TestChangedPathsListsEveryPathBetweenACommitAndItsDescendant(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	base := commitFile(t, dir, "main.go", "package main\n", "one")
	run(t, dir, "switch", "-q", "-c", "feat")
	tested := commitFile(t, dir, "retry.go", "package main\n", "the change the full suite ran on")

	t.Run("the same commit has no changed paths", func(t *testing.T) {
		got, err := (Git{}).ChangedPaths(ctx, dir, tested, tested)
		if err != nil || len(got) != 0 {
			t.Fatalf("paths = %v, %v", got, err)
		}
	})

	docs := commitFile(t, dir, "docs/guide.md", "guide\n", "docs")
	t.Run("a documentation commit lists its path", func(t *testing.T) {
		got, err := (Git{}).ChangedPaths(ctx, dir, tested, docs)
		if err != nil || !slices.Equal(got, []string{"docs/guide.md"}) {
			t.Fatalf("paths = %v, %v", got, err)
		}
	})

	run(t, dir, "mv", "retry.go", "backoff.go")
	run(t, dir, "commit", "-q", "-m", "rename")
	renamed := run(t, dir, "rev-parse", "HEAD")
	t.Run("a rename lists both paths", func(t *testing.T) {
		got, err := (Git{}).ChangedPaths(ctx, dir, docs, renamed)
		if err != nil || !slices.Equal(got, []string{"backoff.go", "retry.go"}) {
			t.Fatalf("paths = %v, %v", got, err)
		}
	})

	t.Run("an abbreviated SHA names its commit", func(t *testing.T) {
		got, err := (Git{}).ChangedPaths(ctx, dir, tested[:7], docs)
		if err != nil || !slices.Equal(got, []string{"docs/guide.md"}) {
			t.Fatalf("paths = %v, %v", got, err)
		}
	})

	// HEAD or a branch names another commit once it moves, so it cannot record a run.
	t.Run("a ref name is refused", func(t *testing.T) {
		for _, ref := range []string{"HEAD", "feat", "HEAD~1"} {
			if _, err := (Git{}).ChangedPaths(ctx, dir, ref, renamed); err == nil || !strings.Contains(err.Error(), "not a commit SHA") {
				t.Errorf("%s: err = %v", ref, err)
			}
		}
	})

	t.Run("a commit that is not an ancestor is refused", func(t *testing.T) {
		_, err := (Git{}).ChangedPaths(ctx, dir, renamed, base)
		if err == nil || !strings.Contains(err.Error(), "not an ancestor") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a merge of the base lists the paths it brings in", func(t *testing.T) {
		for _, c := range []struct {
			name, path string
		}{
			{"a source file", "server.go"},
			{"a documentation file", "docs/changelog.md"},
		} {
			run(t, dir, "switch", "-q", "main")
			commitFile(t, dir, c.path, c.name+"\n", "base: "+c.name)
			run(t, dir, "switch", "-q", "feat")
			before := run(t, dir, "rev-parse", "HEAD")
			run(t, dir, "merge", "-q", "--no-edit", "main")
			merged := run(t, dir, "rev-parse", "HEAD")
			got, err := (Git{}).ChangedPaths(ctx, dir, before, merged)
			if err != nil || !slices.Equal(got, []string{c.path}) {
				t.Errorf("%s: paths = %v, %v", c.name, got, err)
			}
		}
	})
}
