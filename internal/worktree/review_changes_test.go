package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReviewChangesIncludesRenamesAndOnlyTheFixRange(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	base := commitFile(t, dir, "base.go", "package main\n", "base")
	run(t, dir, "switch", "-q", "-c", "feature")
	first := commitFile(t, dir, "old name.go", "package main\n// feature\n", "feature")
	run(t, dir, "mv", "old name.go", "new\tname.go")
	unusual := "quote' dollar$ file.go"
	if err := os.WriteFile(filepath.Join(dir, unusual), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-qm", "fix and rename")
	head := run(t, dir, "rev-parse", "HEAD")
	got, err := (Git{}).ReviewChanges(context.Background(), dir, base, head, first)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]int{}
	for _, f := range got {
		paths[f.Path] = f.ChangedLines
	}
	if len(paths) != 3 || paths["old name.go"] != 2 || paths["new\tname.go"] != 2 || paths[unusual] != 1 {
		t.Fatalf("lost rename or unusual path: %v", paths)
	}
	all, err := (Git{}).ReviewChanges(context.Background(), dir, base, head, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range all {
		if f.Path == "base.go" || f.Path == "old name.go" {
			t.Fatalf("unchanged/deleted-before-base path in full diff: %+v", all)
		}
	}
	empty, err := (Git{}).ReviewChanges(context.Background(), dir, base, head, head)
	if err != nil || len(empty) != 0 {
		t.Fatalf("same-head diff: %+v %v", empty, err)
	}
}
