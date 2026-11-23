package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/cli"
	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/task"
)

type recordingWorkspace struct {
	published []task.Key
	knowledge []string
	err       error
}

func (w *recordingWorkspace) Pull(context.Context) (app.WorkspacePull, error) {
	return app.WorkspacePull{}, nil
}
func (w *recordingWorkspace) Publish(_ context.Context, k task.Key) error {
	w.published = append(w.published, k)
	return w.err
}
func (w *recordingWorkspace) Blocked(context.Context, task.Key, string) error { return nil }
func (w *recordingWorkspace) PublishKnowledge(_ context.Context, repo string) error {
	w.knowledge = append(w.knowledge, repo)
	return w.err
}
func (w *recordingWorkspace) Ack(context.Context, app.WorkspaceAck) error { return nil }

func TestIssueAddPublishesTheNewEntryBeforeReturning(t *testing.T) {
	st, _ := openTestState(t)
	ws := &recordingWorkspace{}
	st.Workspace = ws
	code, out, errs := runCmd(t, st, "", "issue", "add", "--title", "From the CLI")
	id := strings.TrimSpace(out)
	if code != 0 || errs != "" {
		t.Fatalf("add = %d %q %q", code, out, errs)
	}
	if len(ws.published) != 1 || ws.published[0].ExternalID != id || ws.published[0].Source != task.Local {
		t.Fatalf("published = %+v, want %s", ws.published, id)
	}
}

func TestIssueTriagePublishesTheConfirmedScope(t *testing.T) {
	st, _ := openTestState(t)
	_, out, _ := runCmd(t, st, "", "issue", "add", "--title", "Needs scope")
	id := strings.TrimSpace(out)
	ws := &recordingWorkspace{}
	st.Workspace = ws
	if code, _, errs := runCmd(t, st, "Keep it small.", "issue", "triage", id, "--file", "-"); code != 0 {
		t.Fatalf("triage failed: %s", errs)
	}
	if len(ws.published) != 1 || ws.published[0].ExternalID != id {
		t.Fatalf("published = %+v", ws.published)
	}
}

func TestIssueImportPublishesEveryImportedIssue(t *testing.T) {
	src, _ := openTestState(t)
	_, out, _ := runCmd(t, src, "", "issue", "add", "--title", "Portable")
	id := strings.TrimSpace(out)
	file := filepath.Join(t.TempDir(), "work.yaml")
	if code, _, errs := runCmd(t, src, "", "issue", "export", "-o", file); code != 0 {
		t.Fatal(errs)
	}
	dst, _ := openTestState(t)
	ws := &recordingWorkspace{}
	dst.Workspace = ws
	if code, _, errs := runCmd(t, dst, "", "issue", "import", file); code != 0 {
		t.Fatalf("import failed: %s", errs)
	}
	if len(ws.published) != 1 || ws.published[0].ExternalID != id {
		t.Fatalf("published = %+v, want %s", ws.published, id)
	}
}

func TestAFailedPublishDoesNotFailTheCommandAndTheEntryWaits(t *testing.T) {
	st, _ := openTestState(t)
	st.Workspace = &recordingWorkspace{err: os.ErrDeadlineExceeded}
	code, out, errs := runCmd(t, st, "", "issue", "add", "--title", "Offline")
	if code != 0 || strings.TrimSpace(out) == "" {
		t.Fatalf("add = %d %q", code, out)
	}
	if !strings.Contains(errs, "next refresh") {
		t.Fatalf("stderr = %q; it must say the entry waits", errs)
	}
}

func TestSQLiteModePublishesNothing(t *testing.T) {
	st, _ := openTestState(t)
	if st.Workspace != nil {
		t.Fatal("sqlite mode must have no workspace")
	}
	if code, _, _ := runCmd(t, st, "", "issue", "add", "--title", "Local only"); code != 0 {
		t.Fatal("add failed")
	}
}

func TestKnowledgeIngestPublishesTheRepository(t *testing.T) {
	src, srcCfg := openTestState(t)
	seedKnowledge(t, src)
	file := filepath.Join(t.TempDir(), "k.md")
	var out, errs bytes.Buffer
	cmd, _ := cli.Parse([]string{"knowledge", "export", "acme/widgets", "-o", file})
	if code := runKnowledge(context.Background(), srcCfg, src, cmd, &out, &errs); code != 0 {
		t.Fatal(errs.String())
	}
	dst, dstCfg := openTestState(t)
	ws := &recordingWorkspace{}
	dst.Workspace = ws
	cmd, _ = cli.Parse([]string{"knowledge", "ingest", file})
	if code := runKnowledge(context.Background(), dstCfg, dst, cmd, &out, &errs); code != 0 {
		t.Fatal(errs.String())
	}
	if len(ws.knowledge) != 1 || ws.knowledge[0] != "acme/widgets" {
		t.Fatalf("published knowledge = %v", ws.knowledge)
	}
}

func TestOpenStateBuildsTheWorkspaceOnlyForNotionAndObsidian(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	os.MkdirAll(vault, 0o755)
	cfg := config.Config{Database: filepath.Join(dir, "foreman.db"), TasksDir: filepath.Join(dir, "t"), TriageDir: filepath.Join(dir, "tr"), KnowledgeDir: filepath.Join(dir, "k"),
		Storage: config.Storage{Backend: config.BackendObsidian, Obsidian: config.Obsidian{Vault: vault, Folder: "Foreman"}}}
	st, err := openState(cfg, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.Workspace == nil {
		t.Fatal("obsidian mode must have a workspace")
	}
}

func runStorage(t *testing.T, cfg config.Config, verb string) (int, string, string) {
	t.Helper()
	cmd, err := cli.Parse([]string{"storage", verb})
	if err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	code := runStorageCommand(context.Background(), cfg, cmd, &out, &errs)
	return code, out.String(), errs.String()
}

func TestStorageCheckReportsEachStepForAnObsidianVault(t *testing.T) {
	vault := t.TempDir()
	cfg := config.Config{Storage: config.Storage{Backend: config.BackendObsidian, Obsidian: config.Obsidian{Vault: vault, Folder: "Foreman"}}}
	code, out, errs := runStorage(t, cfg, "check")
	if code != 0 || errs != "" {
		t.Fatalf("check = %d %q %q", code, out, errs)
	}
	for _, want := range []string{"passed  create test note", "passed  delete test note"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestStorageCheckFailsWithANonZeroExitAndNamesTheFix(t *testing.T) {
	cfg := config.Config{Storage: config.Storage{Backend: config.BackendObsidian, Obsidian: config.Obsidian{Vault: filepath.Join(t.TempDir(), "missing"), Folder: "Foreman"}}}
	code, _, errs := runStorage(t, cfg, "check")
	if code == 0 || !strings.Contains(errs, "storage.obsidian.vault") || !strings.Contains(errs, "sqlite") {
		t.Fatalf("check = %d %q", code, errs)
	}
}

func TestStorageCheckNeverPrintsTheToken(t *testing.T) {
	t.Setenv(config.NotionTokenEnv, "secret_do_not_print")
	t.Setenv("FOREMAN_NOTION_API", "http://127.0.0.1:1")
	cfg := config.Config{Storage: config.Storage{Backend: config.BackendNotion, Notion: config.Notion{ParentPageID: "0123456789abcdef0123456789abcdef"}}}
	code, out, errs := runStorage(t, cfg, "check")
	if code == 0 {
		t.Fatal("an unreachable API must fail the check")
	}
	if strings.Contains(out+errs, "secret_do_not_print") {
		t.Fatalf("the token leaked:\n%s%s", out, errs)
	}
}

func TestStorageCheckOnSQLiteHasNothingToCheck(t *testing.T) {
	code, out, _ := runStorage(t, config.Config{Storage: config.Storage{Backend: config.BackendSQLite}}, "check")
	if code != 0 || !strings.Contains(out, "sqlite") {
		t.Fatalf("check = %d %q", code, out)
	}
}

func TestStorageMoveCopiesASQLiteWorkspaceIntoTheVaultAndOpensTheGate(t *testing.T) {
	st, cfg := openTestState(t)
	_, out, _ := runCmd(t, st, "", "issue", "add", "--title", "Moves with the workspace")
	id := strings.TrimSpace(out)
	st.Close()
	vault := t.TempDir()
	cfg.Storage = config.Storage{Backend: config.BackendObsidian, Obsidian: config.Obsidian{Vault: vault, Folder: "Foreman"}}

	if _, err := openState(cfg, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "foreman storage move") {
		t.Fatalf("before the move the gate must refuse and name the command, got %v", err)
	}
	code, out, errs := runStorage(t, cfg, "move")
	if code != 0 {
		t.Fatalf("move = %d\n%s\n%s", code, out, errs)
	}
	if !strings.Contains(out, "Moved 0 tasks, 1 issues without a task") {
		t.Fatalf("output = %q", out)
	}
	notes, _ := filepath.Glob(filepath.Join(vault, "Foreman", "Tasks", "*.md"))
	if len(notes) != 1 {
		t.Fatalf("notes = %v, want one for %s", notes, id)
	}
	st2, err := openState(cfg, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("after the move the gate must pass: %v", err)
	}
	st2.Close()
	code, out, _ = runStorage(t, cfg, "move")
	if code != 0 || !strings.Contains(out, "already moved") {
		t.Fatalf("second move = %d %q", code, out)
	}
}
