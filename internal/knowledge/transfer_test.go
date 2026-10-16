package knowledge

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func snapshot(t *testing.T, s Store, r string) Snapshot {
	t.Helper()
	x, err := s.Snapshot(r)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

// mixedStore has one active, one candidate, and one retired learning.
func mixedStore(t *testing.T) Store {
	t.Helper()
	s, _ := newStore(t)
	l := activeLearning(t, s)
	mustApply(t, s, repo, eval("3", 4, observed(Fact, "The API package has no tests.")))
	mustApply(t, s, repo, eval("4", 5, observed(Convention, "Name test helpers after what they build.")))
	ev := eval("5", 6)
	ev.Revisions = []Revision{{ID: "L3", Action: Retire, Reason: "Maintainer dropped it.", Source: Maintainer, Cites: []string{"C1"}}}
	mustApply(t, s, repo, ev)
	if ls := learnings(t, s, repo); len(ls) != 3 || ls[0].ID != l.ID || ls[1].Status != Candidate || ls[2].Status != Retired {
		t.Fatalf("setup: %+v", ls)
	}
	return s
}

func TestExportRoundTripKeepsEveryLearningAndEvaluation(t *testing.T) {
	s := mixedStore(t)
	x := snapshot(t, s, repo)
	x.ExportedAt = day(30)
	data, err := Format(x)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, x) {
		t.Fatalf("round trip changed the snapshot:\n got %+v\nwant %+v", got, x)
	}
	if got.Repo != repo || got.Format != FormatVersion || len(got.Evaluations) != 5 || got.NextID != 3 {
		t.Fatalf("payload = %+v", got)
	}
}

func TestExportShowsActiveGuidanceAboveThePayload(t *testing.T) {
	s := mixedStore(t)
	data, err := Format(snapshot(t, s, repo))
	if err != nil {
		t.Fatal(err)
	}
	readable, _, ok := strings.Cut(string(data), payloadMarker)
	if !ok {
		t.Fatalf("no payload marker:\n%s", data)
	}
	for _, want := range []string{"# Foreman knowledge export", "Repository: acme/widgets", "1 active, 1 candidate, 1 retired", "### Working practices", "- Run the e2e script before pushing."} {
		if !strings.Contains(readable, want) {
			t.Errorf("readable part lacks %q:\n%s", want, readable)
		}
	}
	for _, hidden := range []string{"The API package has no tests.", "Name test helpers"} {
		if strings.Contains(readable, hidden) {
			t.Errorf("readable part shows a non-active learning %q", hidden)
		}
	}
}

func TestParseRejectsInvalidExports(t *testing.T) {
	s := mixedStore(t)
	good, err := Format(snapshot(t, s, repo))
	if err != nil {
		t.Fatal(err)
	}
	edit := func(old, new string) []byte {
		if !strings.Contains(string(good), old) {
			t.Fatalf("export lacks %q", old)
		}
		return []byte(strings.Replace(string(good), old, new, 1))
	}
	for name, data := range map[string][]byte{
		"plain markdown":         []byte("# Notes\n\n- Use tabs.\n"),
		"unclosed payload block": []byte(strings.TrimSuffix(string(good), "```\n")),
		"unsupported version":    edit("format: foreman-knowledge/1", "format: foreman-knowledge/2"),
		"invalid repository":     edit("repo: acme/widgets", "repo: ../widgets"),
		"invalid task id":        edit("task: \"3\"", "task: ../3"),
		"unknown kind":           edit("kind: fact", "kind: rumour"),
		"unknown status":         edit("status: candidate", "status: pending"),
		"unknown source":         edit("source: observed", "source: hearsay"),
		"unknown field":          edit("next_id: 3", "next_id: 3\nowner: someone"),
		"duplicate learning id":  edit("id: L2", "id: L1"),
		"unsafe learning text":   edit("text: The API package has no tests.", "text: See https://example.com for tests."),
		"second yaml document":   edit("next_id: 3", "next_id: 3\n---\nnext_id: 4"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(data); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestRestoreKeepsIDsAndMovesNextIDPastThem(t *testing.T) {
	x := Snapshot{Repo: "acme/old", NextID: 1, Learnings: []Learning{{ID: "L1"}, {ID: "L7"}}}
	got := Restore(x, repo)
	if got.Repo != repo || got.NextID != 7 || got.Learnings[1].ID != "L7" {
		t.Fatalf("got %+v", got)
	}
}

// tree reads every file under dir, so a test can check nothing changed.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		rel, _ := filepath.Rel(dir, path)
		out[rel] = string(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReplaceStoresTheSnapshotAndKeepsAttemptsAndTheLock(t *testing.T) {
	s := mixedStore(t)
	if err := s.RecordAttempt(repo, "3", Attempt{Fingerprint: "fp", At: day(4)}); err != nil {
		t.Fatal(err)
	}
	unlock, err := s.Lock(repo)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	attempt, _ := os.ReadFile(filepath.Join(s.Dir, repo, "attempts", "3.yaml"))

	src, _ := newStore(t)
	mustApply(t, src, repo, eval("7", 9, Proposal{Kind: Fact, Text: "Docs live under docs.", Scope: Reusable, Source: Maintainer, Cites: []string{"D1"}}))
	if err := s.Replace(repo, snapshot(t, src, repo)); err != nil {
		t.Fatal(err)
	}
	files := tree(t, filepath.Join(s.Dir, repo))
	if files["attempts/3.yaml"] != string(attempt) {
		t.Fatal("Replace changed attempts")
	}
	if _, ok := files["learning.lock"]; !ok {
		t.Fatal("Replace removed the lock file")
	}
	if _, ok := files["evaluations/3.yaml"]; ok {
		t.Fatal("an evaluation the snapshot lacks survived")
	}
	if !reflect.DeepEqual(snapshot(t, s, repo).Learnings, snapshot(t, src, repo).Learnings) {
		t.Fatal("learnings not replaced")
	}
	if files["knowledge.md"] != knowledgeMD(t, src, repo) {
		t.Fatalf("knowledge.md not regenerated:\n%s", files["knowledge.md"])
	}
	for name := range files {
		if strings.HasPrefix(name, ".") {
			t.Fatalf("Replace left %s behind", name)
		}
	}
}

func TestReplaceKeepsTheOldFilesWhenAMoveFails(t *testing.T) {
	for _, failAt := range []int{1, 3, 4, 6} {
		s := mixedStore(t)
		before := tree(t, filepath.Join(s.Dir, repo))
		calls := 0
		rename = func(from, to string) error {
			calls++
			if calls == failAt {
				return errors.New("disk full")
			}
			return os.Rename(from, to)
		}
		err := s.Replace(repo, Snapshot{Repo: repo, Learnings: []Learning{{ID: "L1", Kind: Fact, Text: "New.", Status: Active}}})
		rename = os.Rename
		if err == nil {
			t.Fatalf("move %d: no error", failAt)
		}
		if after := tree(t, filepath.Join(s.Dir, repo)); !reflect.DeepEqual(after, before) {
			t.Fatalf("move %d changed the store:\n%v\n%v", failAt, before, after)
		}
	}
}

func TestDigestChangesOnlyWhenTheStoreChanges(t *testing.T) {
	s := mixedStore(t)
	a, err := s.Digest(repo)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := s.Digest(repo); b != a {
		t.Fatal("digest is not stable")
	}
	if err := s.RecordAttempt(repo, "9", Attempt{Fingerprint: "x"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.Digest(repo); b != a {
		t.Fatal("an attempt changed the digest")
	}
	mustApply(t, s, repo, eval("9", 12))
	if b, _ := s.Digest(repo); b == a {
		t.Fatal("a new evaluation left the digest unchanged")
	}
}
