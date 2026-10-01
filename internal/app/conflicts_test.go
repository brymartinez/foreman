package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

func run(t *testing.T, f fixture, key task.Key) *task.Record {
	t.Helper()
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	return f.store.recs[key]
}

// mergesTo plays the implementer pushing a merge commit during a resumed turn.
func mergesTo(f fixture, sha string) {
	f.claude.onResume = func() { setHead(f, sha) }
}

func TestCleanBaseRunsReviewWithoutAgentTurn(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	got := run(t, f, key)
	if f.claude.starts != 1 || len(f.claude.resumed) != 0 || len(got.Reviews) != 1 || got.Reviews[0].Head != "c0ffee1" {
		t.Fatalf("starts %d resumed %d reviews %+v", f.claude.starts, len(f.claude.resumed), got.Reviews)
	}
	d := got.Delegation
	want := MergeCheck{Dir: d.Worktree, Branch: d.Branch, Base: "main"}
	if len(f.wt.checks) != 1 || f.wt.checks[0] != want {
		t.Errorf("checks = %+v, want one against the PR's base %+v", f.wt.checks, want)
	}
}

func TestConflictRunsMergeTurnThenReview(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	f.wt.conflicts = [][]string{{"go.mod", "retry.go"}}
	mergesTo(f, "merge01")
	got := run(t, f, key)
	if len(f.claude.resumed) != 1 || !strings.Contains(f.claude.resumed[0], "sid-1: Your pull request from feat/825-provider-retries conflicts with the latest origin/main") ||
		!strings.Contains(f.claude.resumed[0], "- retry.go\n") {
		t.Fatalf("implementer not resumed with the merge: %q", f.claude.resumed)
	}
	if len(f.codex.reviewed) != 1 || got.State != task.Reviewing || len(got.Reviews) != 1 || got.Reviews[0].Head != "merge01" || got.Delegation.PRHead != "merge01" {
		t.Fatalf("state %s reviews %+v head %s", got.State, got.Reviews, got.Delegation.PRHead)
	}
	if len(f.wt.checks) != 2 {
		t.Errorf("checks = %d, want one before and one after the merge", len(f.wt.checks))
	}
}

func TestFreshMergeTurnWhenSessionIsGone(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{prTurn("sid-1"), prTurn("sid-2")}
	f.claude.resumeErr = errors.Join(errors.New("gone"), agent.ErrNoSession)
	f.wt.conflicts = [][]string{{"retry.go"}}
	got := run(t, f, key)
	if !strings.Contains(f.claude.prompt, "## Merge conflicts") || !strings.Contains(f.claude.prompt, "- retry.go\n") {
		t.Errorf("fresh prompt lacks the merge:\n%s", f.claude.prompt)
	}
	if got.State != task.Reviewing || len(got.Reviews) != 1 {
		t.Errorf("state %s reviews %d", got.State, len(got.Reviews))
	}
}

func TestConflictAfterCleanReviewStillReviewsNewHead(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	// New commits after the clean review send the PR back for review (D63).
	setHead(f, "beef002")
	setCI(f, task.CIStatus{State: task.CIPassing})
	f.gh.items = []InboxItem{item(825, "open", t0, task.Assigned)}
	if _, err := f.app.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.wt.conflicts = [][]string{{"retry.go"}}
	mergesTo(f, "merge01")
	got := run(t, f, key)
	if len(f.claude.resumed) != 1 || len(got.Reviews) != 2 || got.LatestReview().Head != "merge01" {
		t.Fatalf("resumed %d reviews %+v", len(f.claude.resumed), got.Reviews)
	}
}

func TestMergeTurnDoesNotUseFixRound(t *testing.T) {
	f := newFixture()
	f.app.FixRounds = 1
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking")}
	// Clean before the first review, conflicting before the review of the fix.
	f.wt.conflicts = [][]string{nil, {"retry.go"}}
	got := run(t, f, key)
	if len(f.claude.resumed) != 2 || !strings.Contains(f.claude.resumed[1], "conflicts with the latest origin/main") {
		t.Fatalf("want a fix turn then a merge turn: %q", f.claude.resumed)
	}
	if got.State != task.Reviewing || len(got.Reviews) != 2 || !got.LatestReview().Clean || got.ChangeRequestsSinceDecision() != 1 {
		t.Fatalf("state %s reviews %d change requests %d", got.State, len(got.Reviews), got.ChangeRequestsSinceDecision())
	}
	if !strings.Contains(f.codex.reviewed[1], "review_rounds to 3.") {
		t.Errorf("the review after a merge must use the normal settings:\n%s", f.codex.reviewed[1])
	}
}

func TestConflictCheckBeforeEachPass(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking")}
	run(t, f, key)
	if len(f.wt.checks) != 2 || len(f.codex.reviewed) != 2 {
		t.Fatalf("first review and the fix's: checks %d reviews %d", len(f.wt.checks), len(f.codex.reviewed))
	}

	// A restart after an interrupted review picks up at the review.
	f.codex.reviewErr = errors.New("killed")
	setHead(f, "beef002")
	key = started(t, f, startReq(t))
	run(t, f, key)
	f.codex.reviewErr = nil
	key = started(t, f, startReq(t))
	got := run(t, f, key)
	if len(f.wt.checks) != 4 || got.LatestReview().Head != "beef002" {
		t.Fatalf("resumed runs: checks %d review head %s", len(f.wt.checks), got.LatestReview().Head)
	}
}

func TestConflictCheckBeforeReviewingAnsweredFeedback(t *testing.T) {
	f := newFixture()
	key := waiting(t, f)
	checks := len(f.wt.checks)
	f.claude.turn = prTurn("sid-1")
	movesHead(f, "d00d002")
	f.wt.conflicts = [][]string{{"retry.go"}}
	rec := resumeRun(t, f, key)
	if len(f.wt.checks) != checks+2 || rec.State != task.Reviewing || rec.LatestReview().Head != "d00d002" {
		t.Fatalf("checks %d state %s", len(f.wt.checks)-checks, rec.State)
	}
	if last := f.claude.resumed[len(f.claude.resumed)-1]; !strings.Contains(last, "conflicts with the latest origin/main") {
		t.Errorf("last turn was not the merge: %q", last)
	}
}

func TestBlockedMergeStopsInNeedsDecision(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	q := task.Question{Issue: "How should retry.go keep both backoff changes?", Context: "The merge is in progress in the worktree.",
		Options: []task.Option{{Label: "A", Description: "I resolved the files; verify, commit, push"}, {Label: "B", Description: "Keep main's"}}, Recommendation: "A", ImpactIfWrong: "x"}
	f.claude.turns = []agent.Turn{prTurn("sid-1"), {Session: task.Session{Agent: task.Claude, ID: "sid-1"}, Outcome: agent.Outcome{Status: agent.Blocked, Summary: "?", Questions: []task.Question{q}}}}
	f.wt.conflicts = [][]string{{"retry.go"}}
	got := run(t, f, key)
	if got.State != task.NeedsDecision || len(got.Reviews) != 0 || len(f.codex.reviewed) != 0 || got.Pending().Question.Issue != q.Issue {
		t.Fatalf("state %s reviews %d pending %+v", got.State, len(got.Reviews), got.Pending())
	}
	if len(f.wt.removed) != 0 {
		t.Errorf("worktree removed: %+v", f.wt.removed)
	}
}

func TestStillConflictingAfterRetryEscalates(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	f.wt.conflicts = [][]string{{"go.mod", "retry.go"}, {"retry.go"}, {"retry.go"}}
	got := run(t, f, key)
	if len(f.claude.resumed) != maxMergeAttempts || len(f.codex.reviewed) != 0 {
		t.Fatalf("merge turns %d reviews %d", len(f.claude.resumed), len(f.codex.reviewed))
	}
	p := got.Pending()
	if got.State != task.NeedsDecision || p == nil || !strings.Contains(p.Question.Context, "- retry.go") || !strings.Contains(p.Question.Context, got.Delegation.Worktree) || p.Question.Recommendation != "A" {
		t.Fatalf("state %s pending %+v", got.State, p)
	}
	if err := p.Question.Validate(); err != nil {
		t.Errorf("escalation question invalid: %v", err)
	}
}

func TestRespondAfterManualResolutionReviews(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	f.wt.conflicts = [][]string{{"retry.go"}, {"retry.go"}, {"retry.go"}}
	run(t, f, key)
	if _, err := f.app.Respond(context.Background(), key, "A"); err != nil {
		t.Fatal(err)
	}
	mergesTo(f, "merge01")
	got := run(t, f, key)
	last := f.claude.resumed[len(f.claude.resumed)-1]
	if !strings.Contains(last, "Decision: A. I resolved the files in the worktree") {
		t.Errorf("answer not delivered: %q", last)
	}
	if got.State != task.Reviewing || len(got.Reviews) != 1 || got.Reviews[0].Head != "merge01" {
		t.Fatalf("state %s reviews %+v", got.State, got.Reviews)
	}
}

func TestCheckErrorFailsTask(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	f.wt.checkErr = errors.New("git fetch origin main: exit status 128")
	got := run(t, f, key)
	if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "conflict check failed: git fetch") || len(f.codex.reviewed) != 0 {
		t.Fatalf("state %s outcome %+v reviews %d", got.State, got.LastOutcome, len(f.codex.reviewed))
	}
}

// A base branch that moves after the hand-over changes no PR head, so the gate starts
// nothing; only a later run checks it.
func TestBaseOnlyChangeStartsNoRun(t *testing.T) {
	f := newFixture()
	key := ready(t, f)
	checks := len(f.wt.checks)
	f.wt.conflicts = [][]string{{"retry.go"}}
	f.gh.items = []InboxItem{item(825, "open", t0, task.Assigned)}
	if _, err := f.app.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.ReadyForHuman || len(f.runners.spawned) != 1 || len(f.wt.checks) != checks {
		t.Fatalf("state %s spawned %d checks %d", got.State, len(f.runners.spawned), len(f.wt.checks)-checks)
	}
}
