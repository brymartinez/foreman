package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/db"
	"github.com/brymartinez/foreman/internal/task"
)

func openDB(t *testing.T) (*SQLite, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "foreman.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return &SQLite{DB: d}, path
}

func sampleRecord(n int) *task.Record {
	return &task.Record{Key: task.Key{Repo: "acme/web", Number: n}, Kind: task.Issue, Title: "Fix sign in", URL: "https://github.com/acme/web/issues/1", State: task.Discovered, Body: "# Fix\n\n## Notes\n\nmine\n"}
}

func TestSQLiteTaskRoundTripKeepsUserNotesAndFrontmatter(t *testing.T) {
	s, _ := openDB(t)
	rec := sampleRecord(7)
	if err := s.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	// A user edits the stored document: a custom key with a comment, and notes.
	doc, err := s.Document(rec.Key)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(doc, "kind: issue\n", "kind: issue\ntags: [work] # my tags\n", 1) + "More notes.\n"
	if _, err := s.DB.Exec(`UPDATE tasks SET document = ? WHERE id = ?`, edited, taskID(rec.Key)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Update(ctx, rec.Key, func(cur *task.Record) (*task.Record, error) {
		cur.State = task.Done
		return cur, nil
	})
	if err != nil || got.State != task.Done {
		t.Fatalf("update = %+v, %v", got, err)
	}
	after, _ := s.Document(rec.Key)
	if !strings.Contains(after, "tags: [work] # my tags") || !strings.Contains(after, "More notes.") || !strings.Contains(after, "state: DONE") {
		t.Fatalf("saving dropped what the user added:\n%s", after)
	}
}

func TestSQLiteGetListAndMissing(t *testing.T) {
	s, _ := openDB(t)
	if rec, err := s.Get(ctx, task.Key{Repo: "a/b", Number: 1}); rec != nil || err != nil {
		t.Fatalf("missing task = %+v, %v", rec, err)
	}
	for _, n := range []int{10, 2} {
		if err := s.Save(ctx, sampleRecord(n)); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := s.List(ctx)
	if err != nil || len(recs) != 2 || recs[0].Number != 2 || recs[1].Number != 10 {
		t.Fatalf("list = %+v, %v", recs, err)
	}
}

func TestSQLiteUpdateOfAMissingTaskLetsTheCallerDecide(t *testing.T) {
	s, _ := openDB(t)
	key := task.Key{Repo: "a/b", Number: 3}
	got, err := s.Update(ctx, key, func(cur *task.Record) (*task.Record, error) {
		if cur != nil {
			t.Error("a missing task was passed as present")
		}
		return nil, nil
	})
	if err != nil || got != nil {
		t.Fatalf("update = %+v, %v", got, err)
	}
	if rec, _ := s.Get(ctx, key); rec != nil {
		t.Fatal("a nil result created a record")
	}
}

func TestSQLiteUpdatesFromTwoProcessesNeverLoseAWrite(t *testing.T) {
	s, path := openDB(t)
	rec := sampleRecord(1)
	rec.Delegation = &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Models: nil}
	if err := s.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	// A second connection pool stands in for the dashboard and a runner, which are
	// separate processes sharing one file.
	other, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	const each = 20
	for _, store := range []SQLite{*s, {DB: other}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				if _, err := store.Update(ctx, rec.Key, func(cur *task.Record) (*task.Record, error) {
					cur.Delegation.Checkpoints++
					return cur, nil
				}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	got, _ := s.Get(ctx, rec.Key)
	if got.Delegation.Checkpoints != 2*each {
		t.Fatalf("checkpoints = %d, want %d: an update was lost", got.Delegation.Checkpoints, 2*each)
	}
}

func TestSQLiteUpdateRollsBackWhenTheCallbackFails(t *testing.T) {
	s, _ := openDB(t)
	rec := sampleRecord(4)
	if err := s.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	_, err := s.Update(ctx, rec.Key, func(cur *task.Record) (*task.Record, error) {
		cur.Title = "changed"
		return nil, os.ErrInvalid
	})
	if err == nil {
		t.Fatal("update hid the callback's error")
	}
	if got, _ := s.Get(ctx, rec.Key); got.Title != "Fix sign in" {
		t.Fatalf("a failed update changed the record: %+v", got)
	}
}

func TestSQLiteRefusesALocalTaskWithoutAStableID(t *testing.T) {
	s, _ := openDB(t)
	rec := &task.Record{Key: task.Key{Repo: "a/b", Source: task.Local, ExternalID: "nope"}, Identifier: "nope", Kind: task.Issue, State: task.Discovered}
	if err := s.Save(ctx, rec); err == nil {
		t.Fatal("saved a local task with an invalid ID")
	}
}

func TestSQLiteTriageIsConfirmedOnceAndBoundToTheIssue(t *testing.T) {
	s, _ := openDB(t)
	tr := SQLiteTriage{DB: s.DB}
	key := task.Key{Source: task.Local, ExternalID: "L-0000002A"}
	if text, err := tr.Read(key); text != "" || err != nil {
		t.Fatalf("missing triage = %q, %v", text, err)
	}
	if err := tr.Save(key, "  Keep the API.\n"); err != nil {
		t.Fatal(err)
	}
	key.Repo = "acme/api" // the repository is chosen later and must not matter
	if text, err := tr.Read(key); text != "Keep the API." || err != nil {
		t.Fatalf("saved triage = %q, %v", text, err)
	}
	if err := tr.Save(key, "Replacement"); err == nil {
		t.Fatal("a second interview replaced confirmed triage")
	}
	if err := tr.Save(task.Key{Source: task.Local, ExternalID: "L-00000001"}, "  "); err == nil {
		t.Fatal("saved empty triage")
	}
	gh := task.Key{Repo: "acme/web", Number: 5}
	if err := tr.Save(gh, "GitHub scope."); err != nil {
		t.Fatal(err)
	}
	if text, _ := tr.Read(task.Key{Repo: "ACME/web", Number: 5}); text != "GitHub scope." {
		t.Fatalf("repository case changed the triage identity: %q", text)
	}
}

func TestSQLiteStartLockHoldsAcrossRepositoriesAndFreesOnRelease(t *testing.T) {
	s, _ := openDB(t)
	tr := SQLiteTriage{DB: s.DB}
	key := task.Key{Source: task.Local, ExternalID: "L-0000002A", Repo: "acme/web"}
	unlock, err := tr.LockStart(key)
	if err != nil {
		t.Fatal(err)
	}
	key.Repo = "acme/api"
	if release, err := tr.LockStart(key); err == nil {
		release()
		t.Fatal("another repository took the same issue's start lock")
	}
	unlock()
	release, err := tr.LockStart(key)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	release()
}

func TestSQLiteStartLockIsTakenOverFromADeadProcess(t *testing.T) {
	s, _ := openDB(t)
	tr := SQLiteTriage{DB: s.DB}
	key := task.Key{Source: task.Local, ExternalID: "L-0000002A"}
	id, _ := triageID(key)
	if _, err := s.DB.Exec(`INSERT INTO start_locks (id, pid, since) VALUES (?, ?, ?)`, id, 2147483000, time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	release, err := tr.LockStart(key)
	if err != nil {
		t.Fatalf("a dead launcher's lock blocked a start: %v", err)
	}
	release()
}

var _ = context.Background
