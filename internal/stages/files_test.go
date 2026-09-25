package stages

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

type layout struct{ global, repo string }

func setup(t *testing.T, files map[string]string) layout {
	t.Helper()
	root := t.TempDir()
	l := layout{global: filepath.Join(root, "config", "stages"), repo: filepath.Join(root, "repo")}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

func TestResolveLayers(t *testing.T) {
	for _, tc := range []struct {
		name         string
		files        map[string]string
		want         string
		global, repo bool
	}{
		{"no files", nil, "", false, false},
		{"global only", map[string]string{"config/stages/review.md": "Three passes.\n"}, "Three passes.", true, false},
		{"repo only", map[string]string{"repo/.foreman/review.md": "Check migrations.\n"}, "Check migrations.", false, true},
		{"both, global first", map[string]string{
			"config/stages/review.md": "# Review\n\nThree passes.\n",
			"repo/.foreman/review.md": "Check migrations.\n",
		}, "# Review\n\nThree passes.\n\nCheck migrations.", true, true},
		{"blank file adds nothing", map[string]string{"config/stages/review.md": "  \n", "repo/.foreman/review.md": "Check migrations."}, "Check migrations.", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := setup(t, tc.files)
			got, err := Files{GlobalDir: l.global}.Resolve(l.repo, task.StageReview)
			if err != nil {
				t.Fatal(err)
			}
			if got.Content() != tc.want || got.Stage != task.StageReview {
				t.Errorf("content %q, want %q", got.Content(), tc.want)
			}
			if (got.GlobalPath == filepath.Join(l.global, "review.md")) != tc.global || (got.GlobalPath == "") == tc.global {
				t.Errorf("global path %q", got.GlobalPath)
			}
			if (got.RepoPath == filepath.Join(l.repo, ".foreman", "review.md")) != tc.repo || (got.RepoPath == "") == tc.repo {
				t.Errorf("repo path %q", got.RepoPath)
			}
		})
	}
}

func TestResolveSelectsTheStageFile(t *testing.T) {
	l := setup(t, map[string]string{
		"config/stages/implement.md": "IMPLEMENT", "config/stages/review.md": "REVIEW", "config/stages/pr.md": "PR",
		"repo/.foreman/implement.md": "repo IMPLEMENT", "repo/.foreman/pr.md": "repo PR",
	})
	for stage, want := range map[task.Stage]string{
		task.StageImplement: "IMPLEMENT\n\nrepo IMPLEMENT",
		task.StageReview:    "REVIEW",
		task.StagePR:        "PR\n\nrepo PR",
	} {
		got, err := Files{GlobalDir: l.global}.Resolve(l.repo, stage)
		if err != nil || got.Content() != want {
			t.Errorf("%s: %q, %v; want %q", stage, got.Content(), err, want)
		}
	}
	if _, err := (Files{GlobalDir: l.global}).Resolve(l.repo, "deploy"); err == nil {
		t.Error("an unknown stage must be an error")
	}
}

func TestRepoLayerComesFromTheRepoRootNotTheWorkingDirectory(t *testing.T) {
	l := setup(t, map[string]string{"repo/.foreman/pr.md": "repo PR", "elsewhere/.foreman/pr.md": "wrong repo"})
	t.Chdir(filepath.Join(filepath.Dir(l.repo), "elsewhere"))
	got, err := Files{}.Resolve(l.repo, task.StagePR)
	if err != nil || got.Content() != "repo PR" {
		t.Errorf("%q, %v", got.Content(), err)
	}
	if got, _ := (Files{}).Resolve("", task.StagePR); got.Content() != "" || got.RepoPath != "" {
		t.Errorf("no repo root must skip the repo layer: %+v", got)
	}
}

func TestResolveReportsRealFilesystemErrors(t *testing.T) {
	l := setup(t, map[string]string{"config/stages/review.md": "secret"})
	unreadable := filepath.Join(l.global, "review.md")
	os.Chmod(unreadable, 0o000)
	t.Cleanup(func() { os.Chmod(unreadable, 0o644) })
	if _, err := os.ReadFile(unreadable); err == nil {
		t.Skip("running as a user who can read mode 000 files")
	}
	_, err := Files{GlobalDir: l.global}.Resolve(l.repo, task.StageReview)
	if err == nil || err.Error() != "stage instructions "+unreadable+": permission denied" {
		t.Errorf("want a permission error naming the file, got %v", err)
	}

	os.MkdirAll(filepath.Join(l.repo, ".foreman", "pr.md"), 0o755)
	_, err = Files{}.Resolve(l.repo, task.StagePR)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("want a file-type error for a directory, got %v", err)
	}
}

func TestRepoRoot(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	sub := filepath.Join(root, "a", "b")
	os.MkdirAll(sub, 0o755)
	got, err := RepoRoot(context.Background(), sub)
	want, _ := filepath.EvalSymlinks(root)
	if err != nil || got != want {
		t.Errorf("RepoRoot(sub) = %q, %v; want %q", got, err, want)
	}
	if got, err := RepoRoot(context.Background(), t.TempDir()); err != nil || got != "" {
		t.Errorf("outside a repo: %q, %v", got, err)
	}
}
