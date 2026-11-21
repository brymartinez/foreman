package knowledge

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/db"
)

func newSQL(t *testing.T) SQLStore {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "foreman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return SQLStore{DB: d}
}

// The database store must reach the same learnings as the file store did, because
// the promotion rules are the contract.
func TestSQLStoreAppliesTheSameRulesAsTheFileStore(t *testing.T) {
	files, _ := newStore(t)
	sql := newSQL(t)
	maintainer := Proposal{Kind: Convention, Text: "Wrap errors with the operation name.", Scope: Reusable, Source: Maintainer, Cites: []string{"D1"}}
	for _, ev := range []Evaluation{
		eval("1", 0, maintainer, observed(Fact, "Config lives in config.yaml.")),
		eval("2", 1, observed(Fact, "Config lives in config.yaml.", "L2")),
		eval("3", 2, Proposal{Kind: Practice, Text: "One-off detail.", Scope: OneOff, Source: Observed}),
	} {
		mustApply(t, files, repo, ev)
		if err := sql.Apply(repo, ev); err != nil {
			t.Fatal(err)
		}
	}
	want, _ := files.Learnings(repo)
	got, err := sql.Learnings(repo)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("learnings differ (err %v)\n sql: %+v\nfile: %+v", err, got, want)
	}
	fileSnap, _ := files.Snapshot(repo)
	sqlSnap, err := sql.Snapshot(repo)
	if err != nil || !SameSnapshot(sqlSnap, fileSnap) {
		t.Fatalf("snapshots differ (err %v)", err)
	}
	if ev, err := sql.Evaluation(repo, "2"); err != nil || ev == nil || ev.Fingerprint != "fp-2" {
		t.Fatalf("evaluation = %+v, %v", ev, err)
	}
	if ev, _ := sql.Evaluation(repo, "99"); ev != nil {
		t.Fatalf("missing evaluation = %+v", ev)
	}
	evs, _ := sql.Evaluations(repo, 2)
	if len(evs) != 2 || evs[0].Task != "3" {
		t.Fatalf("newest evaluations = %+v", evs)
	}
	picked, err := sql.Select(repo, "config", 5, 500)
	wantPicked, _ := files.Select(repo, "config", 5, 500)
	if err != nil || !reflect.DeepEqual(picked, wantPicked) {
		t.Fatalf("select = %+v, %v; want %+v", picked, err, wantPicked)
	}
}

func TestSQLStoreRejectsRepositoriesAndTasksThatCouldEscape(t *testing.T) {
	s := newSQL(t)
	for _, bad := range []string{"", "acme", "../x/y", "a/b/c", "a/.."} {
		if _, err := s.Learnings(bad); err == nil {
			t.Errorf("Learnings(%q) accepted a bad repository", bad)
		}
	}
	if err := s.Apply(repo, Evaluation{Task: "../x"}); err == nil {
		t.Error("Apply accepted a bad task id")
	}
}

func TestSQLStoreReplaceIsAllOrNothingAndDigestTracksChanges(t *testing.T) {
	s := newSQL(t)
	before, err := s.Digest(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(repo, eval("1", 0, observed(Fact, "A fact."))); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Digest(repo)
	if after == before {
		t.Fatal("the digest did not change after a write")
	}
	// An invalid evaluation mid-replace must leave the old state whole.
	bad := Snapshot{Repo: repo, NextID: 9, Learnings: []Learning{{ID: "L9", Kind: Fact, Text: "New.", Status: Active}}, Evaluations: []Evaluation{eval("2", 1), {Task: "../bad"}}}
	if err := s.Replace(repo, bad); err == nil {
		t.Fatal("replace accepted an invalid evaluation")
	}
	if d, _ := s.Digest(repo); d != after {
		t.Fatal("a failed replace changed stored knowledge")
	}
	good := Snapshot{Repo: repo, NextID: 9, Learnings: []Learning{{ID: "L9", Kind: Fact, Text: "New.", Status: Active}}, Evaluations: []Evaluation{eval("2", 1)}}
	if err := s.Replace(repo, good); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(repo)
	if len(snap.Learnings) != 1 || snap.Learnings[0].ID != "L9" || len(snap.Evaluations) != 1 || snap.Evaluations[0].Task != "2" {
		t.Fatalf("replaced state = %+v", snap)
	}
}

func TestSQLStoreLockExcludesALiveHolderAndRecoversFromADeadOne(t *testing.T) {
	s := newSQL(t)
	unlock, err := s.Lock(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lock(repo); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock = %v, want ErrLocked", err)
	}
	if !s.Busy(repo) {
		t.Error("Busy did not see the held lock")
	}
	if _, err := s.Lock("acme/other"); err != nil {
		t.Errorf("another repository's lock was blocked: %v", err)
	}
	unlock()
	if s.Busy(repo) {
		t.Error("the lock was not released")
	}
	// A holder that died without releasing does not block forever.
	if _, err := s.DB.Exec(`UPDATE locks SET pid = 1 WHERE 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO locks (name, pid, token, since) VALUES (?, 2147483000, 'dead', 'then') ON CONFLICT(name) DO UPDATE SET pid = 2147483000`, "learning:"+repo); err != nil {
		t.Fatal(err)
	}
	if unlock, err := s.Lock(repo); err != nil {
		t.Fatalf("a dead holder's lock blocked: %v", err)
	} else {
		unlock()
	}
}

func TestSQLStoreAttempts(t *testing.T) {
	s := newSQL(t)
	if at, err := s.Attempt(repo, "1"); at != nil || err != nil {
		t.Fatalf("missing attempt = %+v, %v", at, err)
	}
	want := Attempt{Fingerprint: "fp", At: t0}
	if err := s.RecordAttempt(repo, "1", want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Attempt(repo, "1")
	if err != nil || got == nil || got.Fingerprint != "fp" || !got.At.Equal(t0) {
		t.Fatalf("attempt = %+v, %v", got, err)
	}
}

func TestSQLStoreRendersKnowledgeMarkdownForToolsThatReadFiles(t *testing.T) {
	s := newSQL(t)
	s.Dir = t.TempDir()
	maintainer := Proposal{Kind: Convention, Text: "Wrap errors with the operation name.", Scope: Reusable, Source: Maintainer, Cites: []string{"D1"}}
	if err := s.Apply(repo, eval("1", 0, maintainer, observed(Fact, "Only one task backs this."))); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(s.Dir, "acme", "widgets", "knowledge.md"))
	if err != nil || !strings.Contains(string(raw), "Wrap errors with the operation name.") || strings.Contains(string(raw), "Only one task") {
		t.Fatalf("knowledge.md = %q, %v; want active guidance only", raw, err)
	}
	snap, _ := s.Snapshot(repo)
	snap.Learnings = nil
	if err := s.Replace(repo, snap); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(s.Dir, "acme", "widgets", "knowledge.md"))
	if strings.Contains(string(raw), "Wrap errors") {
		t.Fatalf("knowledge.md kept retired guidance after a replace: %q", raw)
	}
}
