package store

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/db"
	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/task"
)

type legacyDirs struct {
	Legacy
	md Markdown
	tr Triage
	kn knowledge.Store
}

func newLegacy(t *testing.T) legacyDirs {
	t.Helper()
	root := t.TempDir()
	l := Legacy{TasksDir: filepath.Join(root, "tasks"), TriageDir: filepath.Join(root, "triage"), KnowledgeDir: filepath.Join(root, "knowledge")}
	return legacyDirs{Legacy: l, md: Markdown{Dir: l.TasksDir}, tr: Triage{Dir: l.TriageDir}, kn: knowledge.Store{Dir: l.KnowledgeDir}}
}

const linearTaskFile = `---
repo: acme/web
number: 0
issue_source: linear
external_issue_id: 123e4567-e89b-12d3-a456-426614174000
issue_identifier: WEB-42
kind: issue
title: Linear work
url: https://linear.app/acme/issue/WEB-42
state: READY_FOR_HUMAN
---
# WEB-42
`

func seed(t *testing.T, l legacyDirs) (userDoc string) {
	t.Helper()
	rec := &task.Record{Key: task.Key{Repo: "acme/web", Number: 7}, Kind: task.Issue, Title: "GitHub work", URL: "https://github.com/acme/web/issues/7", State: task.ReadyForHuman, Body: "# GitHub work\n\n## Notes\n\nmy private notes\n"}
	if err := l.md.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	path := l.md.Path(rec.Key)
	raw, _ := os.ReadFile(path)
	userDoc = strings.Replace(string(raw), "kind: issue\n", "kind: issue\ntags: [obsidian] # keep me\n", 1)
	if err := os.WriteFile(path, []byte(userDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.TasksDir, "acme-web-linear-123e4567-e89b-12d3-a456-426614174000.md"), []byte(linearTaskFile), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.TasksDir, "vault-note.md"), []byte("# Not a task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := l.tr.Save(task.Key{Repo: "Acme/Web", Number: 7}, "Keep the API."); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(l.TriageDir, "linear"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.TriageDir, "linear", "123e4567-e89b-12d3-a456-426614174000.md"), []byte("<!-- foreman:confirmed-triage linear/123e4567-e89b-12d3-a456-426614174000 -->\n# Triage decisions\n\nx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if err := l.kn.Apply("acme/web", knowledge.Evaluation{Task: "7", TaskAt: at, At: at, Fingerprint: "fp", Assessment: "ok", MaintainerRefs: []string{"D1"},
		Proposals: []knowledge.Proposal{{Kind: knowledge.Convention, Text: "Wrap errors with the operation name.", Scope: knowledge.Reusable, Source: knowledge.Maintainer, Cites: []string{"D1"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := l.kn.RecordAttempt("acme/web", "7", knowledge.Attempt{Fingerprint: "fp", At: at}); err != nil {
		t.Fatal(err)
	}
	return userDoc
}

func TestMigrationMovesGitHubWorkAndDeletesLinearRecords(t *testing.T) {
	l := newLegacy(t)
	userDoc := seed(t, l)
	d, err := db.Open(filepath.Join(t.TempDir(), "foreman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	rep, err := MigrateLegacy(d, l.Legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Ran || rep.Tasks != 1 || rep.Triage != 1 || rep.Repositories != 1 || rep.LinearRemoved != 2 {
		t.Fatalf("report = %+v", rep)
	}
	s := SQLite{DB: d}
	doc, err := s.Document(task.Key{Repo: "acme/web", Number: 7})
	if err != nil || doc != userDoc {
		t.Fatalf("migrated document differs from the user's file (err %v):\n%s", err, doc)
	}
	if text, err := (SQLiteTriage{DB: d}).Read(task.Key{Repo: "acme/web", Number: 7}); err != nil || text != "Keep the API." {
		t.Fatalf("triage = %q, %v", text, err)
	}
	ls, err := (knowledge.SQLStore{DB: d}).Learnings("acme/web")
	if err != nil || len(ls) != 1 || ls[0].Status != knowledge.Active {
		t.Fatalf("learnings = %+v, %v", ls, err)
	}
	if at, _ := (knowledge.SQLStore{DB: d}).Attempt("acme/web", "7"); at == nil || at.Fingerprint != "fp" {
		t.Fatalf("attempt = %+v", at)
	}
	recs, _ := s.List(ctx)
	if len(recs) != 1 {
		t.Fatalf("tasks after upgrade = %d; a Linear task must not survive", len(recs))
	}
	// Linear records are gone; the user's originals are kept, out of the vault's way.
	if _, err := os.Stat(filepath.Join(l.TasksDir, "acme-web-linear-123e4567-e89b-12d3-a456-426614174000.md")); !os.IsNotExist(err) {
		t.Error("the Linear task file was not deleted")
	}
	if _, err := os.Stat(filepath.Join(l.TriageDir, "linear", "123e4567-e89b-12d3-a456-426614174000.md")); !os.IsNotExist(err) {
		t.Error("the Linear triage record was not deleted")
	}
	if _, err := os.Stat(filepath.Join(l.TasksDir, backupDir, "acme-web-7.md")); err != nil {
		t.Errorf("the original task file was not kept in the backup folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(l.TasksDir, "vault-note.md")); err != nil {
		t.Error("an ordinary vault note was touched")
	}
}

func TestMigrationIsSafeToRetry(t *testing.T) {
	l := newLegacy(t)
	seed(t, l)
	d, _ := db.Open(filepath.Join(t.TempDir(), "foreman.db"))
	defer d.Close()
	if _, err := MigrateLegacy(d, l.Legacy); err != nil {
		t.Fatal(err)
	}
	// The user edits a task in the database after the upgrade; a rerun must not undo it.
	s := SQLite{DB: d}
	if _, err := s.Update(ctx, task.Key{Repo: "acme/web", Number: 7}, func(cur *task.Record) (*task.Record, error) { cur.Title = "edited"; return cur, nil }); err != nil {
		t.Fatal(err)
	}
	rep, err := MigrateLegacy(d, l.Legacy)
	if err != nil || rep.Ran || rep.LinearRemoved != 0 {
		t.Fatalf("second run = %+v, %v; want a no-op", rep, err)
	}
	if rec, _ := s.Get(ctx, task.Key{Repo: "acme/web", Number: 7}); rec.Title != "edited" {
		t.Fatalf("a rerun overwrote newer database state: %+v", rec)
	}
}

func TestMigrationFinishesLinearCleanupAfterACrashBetweenCommitAndCleanup(t *testing.T) {
	l := newLegacy(t)
	seed(t, l)
	d, _ := db.Open(filepath.Join(t.TempDir(), "foreman.db"))
	defer d.Close()
	if _, err := MigrateLegacy(d, l.Legacy); err != nil {
		t.Fatal(err)
	}
	// Simulate the crash: the import committed, but cleanup never ran or finished.
	if _, err := d.Exec(`DELETE FROM meta WHERE key = ?`, metaCleanup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.TasksDir, "acme-web-linear-123e4567-e89b-12d3-a456-426614174000.md"), []byte(linearTaskFile), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := MigrateLegacy(d, l.Legacy)
	if err != nil || rep.Ran || rep.LinearRemoved != 1 {
		t.Fatalf("resumed run = %+v, %v; want only the cleanup", rep, err)
	}
	if _, err := os.Stat(filepath.Join(l.TasksDir, "acme-web-linear-123e4567-e89b-12d3-a456-426614174000.md")); !os.IsNotExist(err) {
		t.Error("the Linear file survived the resumed cleanup")
	}
}

func TestMigrationWaitsForEveryLiveRunnerIncludingALinearOne(t *testing.T) {
	l := newLegacy(t)
	seed(t, l)
	live := strings.Replace(linearTaskFile, "state: READY_FOR_HUMAN", "state: IMPLEMENTING\nimplementation_agent: claude\nrunner_pid: "+strconv.Itoa(os.Getpid()), 1)
	if err := os.WriteFile(filepath.Join(l.TasksDir, "acme-web-linear-123e4567-e89b-12d3-a456-426614174000.md"), []byte(live), 0o644); err != nil {
		t.Fatal(err)
	}
	d, _ := db.Open(filepath.Join(t.TempDir(), "foreman.db"))
	defer d.Close()
	// A live runner of any task can still write its file, so the upgrade waits for it.
	var active ErrRunnersActive
	if _, err := MigrateLegacy(d, l.Legacy); !errors.As(err, &active) {
		t.Fatalf("migration with a live runner = %v, want ErrRunnersActive", err)
	}
	recs, _ := (SQLite{DB: d}).List(ctx)
	if len(recs) != 0 {
		t.Fatal("a blocked upgrade imported tasks")
	}
	if _, err := os.Stat(l.md.Path(task.Key{Repo: "acme/web", Number: 7})); err != nil {
		t.Fatal("a blocked upgrade moved the original files")
	}
}

func TestAnInvalidLegacyFileLeavesTheDatabaseAndFilesUntouched(t *testing.T) {
	l := newLegacy(t)
	seed(t, l)
	bad := "---\nrepo: acme/web\nnumber: 9\nkind: issue\ntitle: Bad\nurl: u\nstate: NOT_A_STATE\n---\n"
	if err := os.WriteFile(filepath.Join(l.TasksDir, "acme-web-9.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	d, _ := db.Open(filepath.Join(t.TempDir(), "foreman.db"))
	defer d.Close()
	if _, err := MigrateLegacy(d, l.Legacy); err == nil || !strings.Contains(err.Error(), "acme-web-9.md") {
		t.Fatalf("migration = %v, want an error naming the bad file", err)
	}
	var n int
	d.QueryRow(`SELECT (SELECT COUNT(*) FROM tasks) + (SELECT COUNT(*) FROM triage) + (SELECT COUNT(*) FROM learnings)`).Scan(&n)
	if n != 0 {
		t.Fatalf("a failed import left %d rows behind", n)
	}
	if _, err := os.Stat(filepath.Join(l.TasksDir, "acme-web-linear-123e4567-e89b-12d3-a456-426614174000.md")); err != nil {
		t.Error("a failed upgrade deleted the Linear record; deletion must wait for a validated import")
	}
	if v, _ := metaGet(d, metaImport); v != "" {
		t.Error("a failed import was marked done")
	}
}

func TestMigrationOfEmptyDirectoriesJustMarksTheUpgradeDone(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "foreman.db"))
	defer d.Close()
	l := Legacy{TasksDir: filepath.Join(t.TempDir(), "none"), TriageDir: filepath.Join(t.TempDir(), "none2"), KnowledgeDir: filepath.Join(t.TempDir(), "none3")}
	rep, err := MigrateLegacy(d, l)
	if err != nil || !rep.Ran || rep.Tasks != 0 {
		t.Fatalf("report = %+v, %v", rep, err)
	}
}
