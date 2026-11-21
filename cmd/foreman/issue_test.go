package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/cli"
	"github.com/brymartinez/foreman/internal/task"
	"github.com/brymartinez/foreman/internal/tui"
)

func runCmd(t *testing.T, st state, stdin string, argv ...string) (code int, out, errs string) {
	t.Helper()
	cmd, err := cli.Parse(argv)
	if err != nil {
		t.Fatal(err)
	}
	var o, e bytes.Buffer
	code = runIssue(context.Background(), st, cmd, strings.NewReader(stdin), &o, &e)
	return code, o.String(), e.String()
}

func TestIssueAddSavesScopeAndPrintsTheStableID(t *testing.T) {
	st, _ := openTestState(t)
	dir := t.TempDir()
	triage := filepath.Join(dir, "triage.md")
	os.WriteFile(triage, []byte("Keep the API.\n"), 0o600)
	code, out, errs := runCmd(t, st, "Cache is slow.", "issue", "add", "--title", "Fix cache", "--url", "https://linear.app/acme/issue/WEB-42",
		"--description-file", "-", "--triage-file", triage, "--repo", "acme/web", "--tag", "perf")
	id := strings.TrimSpace(out)
	if code != 0 || !strings.HasPrefix(id, "L-") || errs != "" {
		t.Fatalf("add = %d, %q, %q", code, out, errs)
	}
	i, err := st.Issues.Get(context.Background(), id)
	if err != nil || i.Description != "Cache is slow." || i.Repo != "acme/web" || i.SourceURL != "https://linear.app/acme/issue/WEB-42" {
		t.Fatalf("issue = %+v, %v", i, err)
	}
	if text, _ := st.Triage.Read(task.Key{Source: task.Local, ExternalID: id}); text != "Keep the API." {
		t.Fatalf("triage = %q", text)
	}
}

func TestIssueAddWithoutTriageAwaitsTriageAndTriageConfirmsItLater(t *testing.T) {
	st, _ := openTestState(t)
	code, out, _ := runCmd(t, st, "", "issue", "add", "--title", "Needs scope")
	id := strings.TrimSpace(out)
	if code != 0 {
		t.Fatal("add failed")
	}
	_, list, _ := runCmd(t, st, "", "issue", "list")
	if !strings.Contains(list, id+"\tawaiting triage\tNeeds scope") {
		t.Fatalf("list = %q", list)
	}
	if code, _, errs := runCmd(t, st, "Decision: keep it small.", "issue", "triage", id, "--file", "-"); code != 0 {
		t.Fatalf("triage = %d %s", code, errs)
	}
	_, list, _ = runCmd(t, st, "", "issue", "list")
	if !strings.Contains(list, id+"\ttriaged\t") {
		t.Fatalf("list after triage = %q", list)
	}
	_, show, _ := runCmd(t, st, "", "issue", "show", id)
	if !strings.Contains(show, "keep it small") {
		t.Fatalf("show = %q", show)
	}
	if code, _, errs := runCmd(t, st, "A second interview.", "issue", "triage", id, "--file", "-"); code == 0 || !strings.Contains(errs, "already exists") {
		t.Fatalf("second triage = %d %q", code, errs)
	}
}

func TestIssueAddRejectsBadInputWithoutSavingAnything(t *testing.T) {
	st, _ := openTestState(t)
	for name, argv := range map[string][]string{
		"file URL":       {"issue", "add", "--title", "x", "--url", "file:///etc/passwd"},
		"empty triage":   {"issue", "add", "--title", "x", "--triage-file", "-"},
		"missing file":   {"issue", "add", "--title", "x", "--description-file", "/nonexistent/file"},
		"two stdin uses": {"issue", "add", "--title", "x", "--description-file", "-", "--triage-file", "-"},
	} {
		if code, _, _ := runCmd(t, st, "   ", argv...); code == 0 {
			t.Errorf("%s: succeeded", name)
		}
	}
	if issues, _ := st.Issues.List(context.Background()); len(issues) != 0 {
		t.Fatalf("rejected intake saved %d issues", len(issues))
	}
}

func TestIssueExportAndImportRoundTripThroughFiles(t *testing.T) {
	src, _ := openTestState(t)
	_, out, _ := runCmd(t, src, "Scope.", "issue", "add", "--title", "Work", "--triage-file", "-")
	id := strings.TrimSpace(out)
	file := filepath.Join(t.TempDir(), "work.yaml")
	if code, _, errs := runCmd(t, src, "", "issue", "export", "-o", file); code != 0 {
		t.Fatalf("export: %s", errs)
	}
	dst, _ := openTestState(t)
	code, out, _ := runCmd(t, dst, "", "issue", "import", file, "--dry-run")
	if code != 0 || !strings.Contains(out, "Would import 1 new records") {
		t.Fatalf("dry run = %d %q", code, out)
	}
	if issues, _ := dst.Issues.List(context.Background()); len(issues) != 0 {
		t.Fatal("a dry run imported")
	}
	if code, out, errs := runCmd(t, dst, "", "issue", "import", file); code != 0 || !strings.Contains(out, "Imported 1 new records") {
		t.Fatalf("import = %d %q %q", code, out, errs)
	}
	if code, out, _ := runCmd(t, dst, "", "issue", "import", file); code != 0 || !strings.Contains(out, "0 new records; 1 already present") {
		t.Fatalf("repeat import = %d %q", code, out)
	}
	if _, err := dst.Issues.Get(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	// A conflicting file stops with a message that names the way forward.
	dst.DB.Exec(`UPDATE local_issues SET title = 'Changed' WHERE id = ?`, id)
	code, out, errs := runCmd(t, dst, "", "issue", "import", file)
	if code != 1 || !strings.Contains(out, "conflict: issue "+id) || !strings.Contains(errs, "--on-conflict") {
		t.Fatalf("conflicting import = %d %q %q", code, out, errs)
	}
	// An invalid file changes nothing and says so.
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	os.WriteFile(bad, []byte("format: nope\n"), 0o600)
	if code, _, errs := runCmd(t, dst, "", "issue", "import", bad); code != 1 || !strings.Contains(errs, "nothing was imported") {
		t.Fatalf("invalid import = %d %q", code, errs)
	}
}

func TestExportMarkdownWritesEveryTaskForObsidian(t *testing.T) {
	st, _ := openTestState(t)
	if err := st.Tasks.Save(context.Background(), &task.Record{Key: task.Key{Repo: "acme/web", Number: 7}, Kind: task.Issue, Title: "Work", URL: "u", State: task.Done, Body: "# Work\n\nnotes\n"}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "vault")
	if code, out, errs := runCmd(t, st, "", "export", "markdown", dir); code != 0 || !strings.Contains(out, "Exported 1 tasks") {
		t.Fatalf("export markdown = %d %q %q", code, out, errs)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "acme-web-7.md"))
	if err != nil || !strings.Contains(string(raw), "title: Work") || !strings.Contains(string(raw), "notes") {
		t.Fatalf("exported note = %q, %v", raw, err)
	}
}

func TestTriagePromptNamesTheExactSaveCommand(t *testing.T) {
	newIssue := triagePrompt("/bin/foreman", tui.TriageRequest{})
	for _, want := range []string{"foreman-triage", "/bin/foreman issue add", "--triage-file", "Foreman never writes to that tracker"} {
		if !strings.Contains(newIssue, want) {
			t.Errorf("intake prompt lacks %q:\n%s", want, newIssue)
		}
	}
	local := triagePrompt("/bin/foreman", tui.TriageRequest{Key: task.Key{Source: task.Local, ExternalID: "L-0000002A"}, Title: "Fix"})
	if !strings.Contains(local, "issue triage L-0000002A --file") || !strings.Contains(local, "issue show L-0000002A") {
		t.Errorf("local prompt:\n%s", local)
	}
	gh := triagePrompt("/bin/foreman", tui.TriageRequest{Key: task.Key{Repo: "acme/web", Number: 5}, Title: "Fix"})
	if !strings.Contains(gh, "acme/web#5") || strings.Contains(gh, "issue triage") {
		t.Errorf("GitHub prompt:\n%s", gh)
	}
}
