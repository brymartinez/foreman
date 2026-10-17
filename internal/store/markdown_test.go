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
			Implementer:       task.Session{Agent: task.Claude, ID: "d46213bb-3c77-45e9-9d7c-52856e03ab18"},
			StartedAt:         time.Date(2026, 9, 24, 16, 0, 0, 0, time.UTC),
			Reviewer:          task.Codex,
			Branch:            "agent/825-provider-retries",
			Worktree:          "/wt/ridezoomo-armada-backend/825-provider-retries",
			WorktreeRemovedAt: time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC),
			ContextFile:       "/ctx/825.md",
			Launch:            &task.Launch{Token: "5eed", PID: 4100, Since: time.Date(2026, 9, 24, 16, 0, 0, 0, time.UTC)},
			PR:                912,
			PRURL:             "https://github.com/ridezoomo/armada-backend/pull/912",
			PRHead:            "beef002",
			ConflictAttempts:  []task.ConflictAttempt{{Head: "beef000", Base: "ba5e000"}, {Head: "beef001", Base: "ba5e001"}},
			ConflictPending:   true,
		},
		Validation: []task.Check{{Command: "go test ./...", Passed: true}, {Command: "golangci-lint run", Passed: false}},
		Plan: []task.PlanComment{
			{Doc: task.Analysis, CommentID: 3301, URL: "https://github.com/ridezoomo/armada-backend/issues/825#issuecomment-3301"},
			{Doc: task.ImplementationPlan, CommentID: 3304, URL: "https://github.com/ridezoomo/armada-backend/issues/825#issuecomment-3304"},
		},
		Reviews: []task.Review{
			{Round: 1, Reviewer: task.Codex, PR: 912, Summary: "one bug", At: time.Date(2026, 9, 24, 18, 0, 0, 0, time.UTC),
				Findings: []task.Finding{{Severity: "blocking", File: "retry.go", Line: 40, Summary: "4xx retried", Detail: "guard"}}},
			{Round: 2, Reviewer: task.Codex, PR: 912, Clean: true, Summary: "ok", At: time.Date(2026, 9, 24, 19, 0, 0, 0, time.UTC), Head: "c0ffee1"},
		},
		Feedback: []task.Feedback{
			{Kind: task.IssueComment, ID: 4401, Author: "brymartinez", URL: "https://github.com/ridezoomo/armada-backend/pull/912#issuecomment-4401",
				At: time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC), Digest: task.Digest("ship it?"), HandledAt: time.Date(2026, 9, 25, 9, 30, 0, 0, time.UTC)},
			{Kind: task.ReviewComment, ID: 4402, Author: "dependabot[bot]", URL: "https://github.com/ridezoomo/armada-backend/pull/912#discussion_r4402",
				At: time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC), Digest: task.Digest("this retries forever")},
		},
		CI: &task.CIStatus{State: task.CIFailing, CheckedAt: time.Date(2026, 9, 24, 19, 30, 0, 0, time.UTC),
			Failing: []task.CheckRun{{Name: "test", Link: "https://github.com/ridezoomo/armada-backend/actions/runs/1"}}},
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

func TestLinearTaskKeepsSourceAndStableIssueIdentity(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	key := task.Key{Repo: "acme/web", Source: task.Linear, ExternalID: "123e4567-e89b-12d3-a456-426614174000"}
	rec := &task.Record{Key: key, Identifier: "WEB-42", Kind: task.Issue, Title: "Fix sign in", URL: "https://linear.app/acme/issue/WEB-42", State: task.Discovered}
	if err := s.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, rec) {
		t.Fatalf("round trip = %+v, want %+v", got, rec)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "acme-web-linear-123e4567-e89b-12d3-a456-426614174000.md")); err != nil {
		t.Fatal(err)
	}
}

func TestLinearPlanCommentKeepsStringID(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	key := task.Key{Repo: "acme/web", Source: task.Linear, ExternalID: "123e4567-e89b-12d3-a456-426614174000"}
	rec := &task.Record{Key: key, Identifier: "WEB-42", Kind: task.Issue, Title: "Fix sign in", URL: "https://linear.app/acme/issue/WEB-42", State: task.Discovered}
	rec.SetPlanComment(task.PlanComment{Doc: task.Analysis, ExternalCommentID: "abc-123", URL: "https://linear.app/acme/issue/WEB-42#comment-abc-123"})
	if err := s.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Plan, rec.Plan) {
		t.Fatalf("plan comments = %+v, want %+v", got.Plan, rec.Plan)
	}
}

func TestLegacyTaskKeepsGitHubIdentity(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	rec := delegated()
	if err := s.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, rec.Key)
	if err != nil {
		t.Fatal(err)
	}
	if got.IssueSource() != task.GitHub || got.Key != rec.Key {
		t.Fatalf("legacy identity = %+v", got.Key)
	}
}

func TestMarkdownRoundTripsImplementationCheckpoint(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	want := delegated()
	want.Delegation.PR, want.Delegation.PRURL, want.Delegation.PRHead = 0, "", ""
	want.Delegation.CheckpointHead = "c0ffee1"
	want.Delegation.CheckpointNext = "Add the log pager"
	want.Delegation.Checkpoints = 2
	if err := s.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, want.Key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Delegation.CheckpointHead != "c0ffee1" || got.Delegation.CheckpointNext != "Add the log pager" || got.Delegation.Checkpoints != 2 {
		t.Fatalf("checkpoint = %+v", got.Delegation)
	}
}

func TestOldFileLoadsWithoutConflictFields(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	old := "---\nrepo: a/b\nnumber: 3\nkind: issue\nstate: READY_FOR_HUMAN\nimplementation_agent: claude\nbranch: feat/3-x\npr: 7\npr_head: c0ffee1\n---\n"
	if err := os.WriteFile(filepath.Join(s.Dir, "a-b-3.md"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, task.Key{Repo: "a/b", Number: 3})
	if err != nil {
		t.Fatal(err)
	}
	d := got.Delegation
	if d.ConflictAttempts != nil || d.ConflictPending || d.ConflictAttempted("c0ffee1", "") {
		t.Fatalf("delegation = %+v", d)
	}
	if err := s.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(s.Dir, "a-b-3.md"))
	if strings.Contains(string(raw), "conflict_") {
		t.Errorf("saved empty conflict fields:\n%s", raw)
	}
}

func TestMarkdownRoundTripsPRClosed(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	want := delegated()
	want.State = task.PRClosed
	if err := s.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, want.Key)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != task.PRClosed || got.Delegation.Branch != want.Delegation.Branch {
		t.Fatalf("state %s branch %q", got.State, got.Delegation.Branch)
	}
}

func TestMarkdownRoundTripsReviewed(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	want := delegated()
	want.State = task.Reviewed
	if err := s.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, want.Key)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != task.Reviewed || len(got.UnhandledFeedback()) != 1 || got.UnhandledFeedback()[0].ID != 4402 {
		t.Fatalf("state %s unanswered %+v", got.State, got.UnhandledFeedback())
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

func TestFileKeepsSameAgentReviewerAfterFallback(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	rec := delegated()
	rec.Delegation.Reviewer = rec.Delegation.Implementer.Agent
	if err := s.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, rec.Key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Delegation.Reviewer != rec.Delegation.Reviewer {
		t.Fatalf("reviewer after reload = %v, err %v", got.Delegation.Reviewer, err)
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

func TestStoreRoundTripsProfile(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	want := delegated()
	want.Delegation.Profile = &task.Profile{Name: "claude", Agent: task.Claude, PlanModel: "sonnet", Model: "opus"}
	want.Delegation.RouteReason = "broad or ambiguous design work"
	want.Delegation.RouteLogged = true
	if err := s.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, want.Key)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Delegation.Profile, want.Delegation.Profile) || got.Delegation.RouteReason != want.Delegation.RouteReason || !got.Delegation.RouteLogged {
		t.Fatalf("got %+v %q %v", got.Delegation.Profile, got.Delegation.RouteReason, got.Delegation.RouteLogged)
	}
}

func TestOldFileLoadsWithoutAProfile(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	old := "---\nrepo: a/b\nnumber: 3\nkind: issue\nstate: WORKING\nimplementation_agent: claude\nbranch: feat/3-x\n---\n"
	if err := os.WriteFile(filepath.Join(s.Dir, "a-b-3.md"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, task.Key{Repo: "a/b", Number: 3})
	if err != nil {
		t.Fatal(err)
	}
	if d := got.Delegation; d.Profile != nil || d.RouteReason != "" || d.RouteLogged {
		t.Fatalf("delegation = %+v", d)
	}
}

func TestStoreRejectsAProfileForAnotherAgent(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	bad := "---\nrepo: a/b\nnumber: 3\nkind: issue\nstate: WORKING\nimplementation_agent: claude\nprofile:\n  name: codex\n  agent: codex\n---\n"
	if err := os.WriteFile(filepath.Join(s.Dir, "a-b-3.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, task.Key{Repo: "a/b", Number: 3}); err == nil || !strings.Contains(err.Error(), "profile") {
		t.Fatalf("got %v, want a profile error", err)
	}
}

func TestStoreRoundTripsAttempts(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	want := delegated()
	start := time.Date(2026, 9, 24, 16, 0, 0, 0, time.UTC)
	want.Attempts = []task.Attempt{
		{StartedAt: start, Agent: task.Codex, Branch: "agent/825-provider-retries", PR: 900, PRURL: "https://x/pull/900", EndedAt: start.Add(time.Hour),
			Marks: []task.Mark{{At: start, Phase: task.PhasePlanning}, {At: start.Add(time.Hour), Phase: task.PhaseEnded}}},
		{StartedAt: start.Add(2 * time.Hour), Agent: task.Claude, LogStart: 4096, HandoffAt: start.Add(3 * time.Hour),
			Marks: []task.Mark{{At: start.Add(2 * time.Hour), Phase: task.PhaseReview}}},
	}
	if err := s.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, want.Key)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Attempts, want.Attempts) {
		t.Fatalf("attempts\n got: %+v\nwant: %+v", got.Attempts, want.Attempts)
	}
}

func TestOldFileLoadsWithoutAttemptsAndSavesNone(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	old := "---\nrepo: a/b\nnumber: 3\nkind: issue\nstate: READY_FOR_HUMAN\nimplementation_agent: claude\nbranch: feat/3-x\npr: 7\n---\n"
	if err := os.WriteFile(filepath.Join(s.Dir, "a-b-3.md"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, task.Key{Repo: "a/b", Number: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got.Attempts != nil {
		t.Fatalf("attempts = %+v", got.Attempts)
	}
	if err := s.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(s.Dir, "a-b-3.md"))
	if strings.Contains(string(raw), "attempts") {
		t.Errorf("saved an empty attempts key:\n%s", raw)
	}
}

func TestStoreRejectsAnUnknownPhase(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	bad := "---\nrepo: a/b\nnumber: 3\nkind: issue\nstate: WORKING\nimplementation_agent: claude\nattempts:\n  - marks:\n      - at: 2026-10-01T09:00:00Z\n        phase: lunch\n---\n"
	if err := os.WriteFile(filepath.Join(s.Dir, "a-b-3.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, task.Key{Repo: "a/b", Number: 3}); err == nil || !strings.Contains(err.Error(), "lunch") {
		t.Fatalf("got %v, want an unknown phase error", err)
	}
}

func TestStoreRoundTripsToolCallCounts(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	want := delegated()
	want.Usage = task.Usage{CodexTurns: 2, ToolCalls: map[string]int{"command_execution": 4, "file_change": 2}}
	if err := s.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, want.Key)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Usage, want.Usage) {
		t.Fatalf("usage\n got: %+v\nwant: %+v", got.Usage, want.Usage)
	}
}

func TestOldFileLoadsWithoutToolCallsAndSavesNone(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	old := "---\nrepo: a/b\nnumber: 3\nkind: issue\nstate: READY_FOR_HUMAN\nimplementation_agent: claude\nbranch: feat/3-x\nusage:\n  claude_cost_usd: 1.5\n  claude_turns: 2\n---\n"
	if err := os.WriteFile(filepath.Join(s.Dir, "a-b-3.md"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, task.Key{Repo: "a/b", Number: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got.Usage.ToolCalls != nil || got.Usage.ClaudeCostUSD != 1.5 {
		t.Fatalf("usage = %+v", got.Usage)
	}
	if err := s.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(s.Dir, "a-b-3.md"))
	if strings.Contains(string(raw), "tool_calls") {
		t.Errorf("saved an empty tool_calls key:\n%s", raw)
	}
}

func TestStoreRoundTripsPinnedAccount(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	want := delegated()
	want.Delegation.Account = "octo-work"
	want.Delegation.AccountEmail = "42+octo-work@users.noreply.github.com"
	if err := s.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, want.Key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Delegation.Account != want.Delegation.Account || got.Delegation.AccountEmail != want.Delegation.AccountEmail {
		t.Fatalf("account = %q <%q>", got.Delegation.Account, got.Delegation.AccountEmail)
	}
}

func TestOldFileLoadsWithoutAPinnedAccountAndSavesNone(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	old := "---\nrepo: a/b\nnumber: 3\nkind: issue\nstate: FAILED\nimplementation_agent: claude\nbranch: feat/3-x\n---\n"
	if err := os.WriteFile(filepath.Join(s.Dir, "a-b-3.md"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, task.Key{Repo: "a/b", Number: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got.Delegation.Account != "" || got.Delegation.AccountEmail != "" {
		t.Fatalf("delegation = %+v", got.Delegation)
	}
	if err := s.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(s.Dir, "a-b-3.md"))
	if strings.Contains(string(raw), "account") {
		t.Errorf("saved empty account fields:\n%s", raw)
	}
}

func TestStoreRoundTripsResetMarkWithoutADelegation(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	at := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	want := &task.Record{
		Key: task.Key{Repo: "a/b", Number: 3}, Kind: task.Issue, Title: "x", State: task.Discovered,
		Reset: &task.ResetMark{At: at, PR: 12, PRURL: "https://github.com/a/b/pull/12", Branch: "feat/3-x", PRState: "open",
			Cleanup: &task.ResetCleanup{Worktree: "/wt/feat/3-x", Branch: "feat/3-x", RepoDir: "/repos/a-b", RunnerLog: "/logs/a-b-3.log"}},
		Body: "# a/b#3 x\n",
	}
	if err := s.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, want.Key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Delegation != nil || got.Reset == nil || !got.Reset.At.Equal(at) {
		t.Fatalf("record = %+v", got)
	}
	got.Reset.At = at
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch\n got: %+v\nwant: %+v", got, want)
	}
	got.Reset = nil
	if err := s.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(s.Dir, "a-b-3.md"))
	if strings.Contains(string(raw), "reset") {
		t.Errorf("cleared reset mark is still saved:\n%s", raw)
	}
}

func TestStoreRoundTripsThePriorPR(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	want := delegated()
	want.Delegation.PriorPR = 12
	if err := s.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, want.Key)
	if err != nil || got.Delegation.PriorPR != 12 {
		t.Fatalf("prior PR = %+v, %v", got.Delegation, err)
	}
}

func TestStoreRoundTripsTheCheckoutOfTheBranch(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	want := delegated()
	want.Delegation.RepoDir = "/src/a-b"
	if err := s.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, want.Key)
	if err != nil || got.Delegation.RepoDir != "/src/a-b" {
		t.Fatalf("repo dir = %+v, %v", got.Delegation, err)
	}
}

func TestStoreRoundTripsTheLocalReviewProposal(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	want := delegated()
	want.Delegation.PR, want.Delegation.PRURL, want.Delegation.PRHead = 0, "", ""
	want.Delegation.ReviewBeforePR = true
	want.Delegation.LocalHead = "abc1234"
	want.Delegation.ProposedTitle = "feat: retry 5xx"
	want.Delegation.ProposedBody = "## Problem\n\nRetries.\n\nCloses #825"
	if err := s.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, want.Key)
	if err != nil {
		t.Fatal(err)
	}
	d := got.Delegation
	if !d.ReviewBeforePR || d.LocalHead != "abc1234" || d.ProposedTitle != want.Delegation.ProposedTitle || d.ProposedBody != want.Delegation.ProposedBody {
		t.Fatalf("delegation = %+v", d)
	}
}

func TestOldFileLoadsWithReviewBeforePROffAndSavesNone(t *testing.T) {
	s := Markdown{Dir: t.TempDir()}
	old := "---\nrepo: a/b\nnumber: 3\nkind: issue\nstate: WORKING\nimplementation_agent: claude\nbranch: feat/3-x\n---\n"
	if err := os.WriteFile(filepath.Join(s.Dir, "a-b-3.md"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, task.Key{Repo: "a/b", Number: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got.Delegation.ReviewBeforePR || got.Delegation.LocalHead != "" {
		t.Fatalf("delegation = %+v", got.Delegation)
	}
	if err := s.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(s.Dir, "a-b-3.md"))
	for _, key := range []string{"review_before_pr", "local_head", "proposed_"} {
		if strings.Contains(string(raw), key) {
			t.Errorf("saved empty %s:\n%s", key, raw)
		}
	}
}
