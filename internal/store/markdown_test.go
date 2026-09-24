package store

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

var ctx = context.Background()

func delegated() *task.Record {
	synced := time.Date(2026, 9, 24, 20, 0, 0, 0, time.FixedZone("CEST", 2*3600))
	return &task.Record{
		Key:   task.Key{Repo: "ridezoomo/armada-backend", Number: 825},
		Kind:  task.Issue,
		Title: "Provider retries",
		URL:   "https://github.com/ridezoomo/armada-backend/issues/825",
		State: task.Working,
		Delegation: &task.Delegation{
			Implementer: task.Session{Agent: task.Claude, ID: "d46213bb-3c77-45e9-9d7c-52856e03ab18"},
			Reviewer:    task.Codex,
			Branch:      "agent/825-provider-retries",
			Worktree:    "/wt/ridezoomo-armada-backend/825-provider-retries",
			ContextFile: "/ctx/825.md",
			PR:          912,
			PRURL:       "https://github.com/ridezoomo/armada-backend/pull/912",
		},
		Validation: []task.Check{{Command: "go test ./...", Passed: true}, {Command: "golangci-lint run", Passed: false}},
		Reviews: []task.Review{
			{Round: 1, Reviewer: task.Codex, PR: 912, Summary: "one bug", At: time.Date(2026, 9, 24, 18, 0, 0, 0, time.UTC),
				Findings: []task.Finding{{Severity: "blocking", File: "retry.go", Line: 40, Summary: "4xx retried", Detail: "guard"}}},
			{Round: 2, Reviewer: task.Codex, PR: 912, Clean: true, Summary: "ok", At: time.Date(2026, 9, 24, 19, 0, 0, 0, time.UTC)},
		},
		GitHub: &task.Snapshot{
			UpdatedAt: time.Date(2026, 9, 24, 17, 0, 0, 0, time.UTC),
			State:     "open",
			Reasons:   []task.InboxReason{task.Assigned},
		},
		LastGitHubSync: synced,
		Decisions: []task.Decision{{
			AskedAt: synced,
			Question: task.Question{
				Issue:          "Should retries apply to 4xx?",
				Context:        "Retries 5xx only.",
				Options:        []task.Option{{Label: "A", Description: "5xx only"}},
				Recommendation: "A",
				ImpactIfWrong:  "Repeated client errors.",
			},
			Answer:      "A. 5xx only",
			AnsweredAt:  synced,
			DeliveredAt: synced,
		}},
		Body: "# #825 Provider retries\n\n## Decisions\n\n- Preserve existing retry behavior.\n",
	}
}

func TestRoundTrip(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	want := delegated()
	if err := s.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, want.Key)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastGitHubSync.Equal(want.LastGitHubSync) || !got.Decisions[0].AskedAt.Equal(want.Decisions[0].AskedAt) {
		t.Fatalf("times drifted: %v / %v", got.LastGitHubSync, got.Decisions[0].AskedAt)
	}
	got.LastGitHubSync, got.Decisions[0].AskedAt = want.LastGitHubSync, want.Decisions[0].AskedAt
	got.Decisions[0].AnsweredAt, got.Decisions[0].DeliveredAt = want.Decisions[0].AnsweredAt, want.Decisions[0].DeliveredAt
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

func TestFileMatchesDesignDocShape(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	if err := s.Save(ctx, delegated()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(s.Dir, "ridezoomo-armada-backend-825.md"))
	for _, line := range []string{
		"---\nrepo: ridezoomo/armada-backend\nnumber: 825\n",
		"\nstate: WORKING\n",
		"\nimplementation_agent: claude\n",
		"\nreview_agent: codex\n",
		"\nbranch: agent/825-provider-retries\n",
		"\n  reasons: [assigned]\n",
		"\n---\n# #825 Provider retries\n",
	} {
		if !strings.Contains(string(raw), line) {
			t.Errorf("file missing %q:\n%s", line, raw)
		}
	}
}

func TestSavePreservesUserFrontmatterAndBody(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	rec := delegated()
	if err := s.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir, "ridezoomo-armada-backend-825.md")
	raw, _ := os.ReadFile(path)
	edited := strings.Replace(string(raw), "---\nrepo:", "---\n# my note\ntags: [work, retries]\nrepo:", 1)
	edited = strings.Replace(edited, "state: WORKING", "state: WORKING # watching this one", 1)
	edited += "\nHuman notes added in Obsidian.\n"
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := s.Get(ctx, rec.Key)
	if err != nil {
		t.Fatal(err)
	}
	loaded.State = task.Reviewing
	loaded.Delegation.PR = 912
	if err := s.Save(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(path)
	for _, want := range []string{
		"# my note\ntags: [work, retries]\nrepo:",
		"state: REVIEWING # watching this one",
		"pr: 912",
		"Human notes added in Obsidian.\n",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q after save:\n%s", want, out)
		}
	}
}

func TestSaveRemovesClearedOwnedKeys(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	rec := delegated()
	if err := s.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	rec.Delegation.ContextFile = ""
	if err := s.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(s.Dir, "ridezoomo-armada-backend-825.md"))
	if strings.Contains(string(raw), "context_file") {
		t.Errorf("cleared key still present:\n%s", raw)
	}
}

func TestListSkipsNonTaskMarkdownAndSorts(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	for _, n := range []int{30, 4} {
		r := delegated()
		r.Number, r.Delegation = n, nil
		r.State = task.Discovered
		if err := s.Save(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(s.Dir, "README.md"), []byte("# Tasks\n"), 0o644)
	os.WriteFile(filepath.Join(s.Dir, "note.md"), []byte("---\ntags: [x]\n---\nhello\n"), 0o644)

	recs, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].Number != 4 || recs[1].Number != 30 {
		t.Fatalf("got %d records: %+v", len(recs), recs)
	}
}

func TestMalformedTaskFileNamesThePath(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	os.WriteFile(filepath.Join(s.Dir, "bad.md"), []byte("---\nrepo: a/b\nnumber: 1\nkind: issue\nstate: THINKING\n---\n"), 0o644)
	_, err := s.List(ctx)
	if err == nil || !strings.Contains(err.Error(), "bad.md") || !strings.Contains(err.Error(), "THINKING") {
		t.Fatalf("err = %v", err)
	}
}

func TestGetMissingIsNil(t *testing.T) {
	rec, err := Markdown{Dir: t.TempDir()}.Get(ctx, task.Key{Repo: "a/b", Number: 1})
	if rec != nil || err != nil {
		t.Fatalf("got %v, %v", rec, err)
	}
}

func TestUpdateSerializesConcurrentWriters(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	rec := delegated()
	rec.Delegation.PR = 0
	if err := s.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Update(ctx, rec.Key, func(cur *task.Record) (*task.Record, error) {
				cur.Delegation.PR++
				return cur, nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, _ := s.Get(ctx, rec.Key)
	if got.Delegation.PR != 20 {
		t.Fatalf("lost updates: PR counter = %d, want 20", got.Delegation.PR)
	}
}
