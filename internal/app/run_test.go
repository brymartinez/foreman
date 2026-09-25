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

func prTurn(id string) agent.Turn {
	return agent.Turn{Session: task.Session{Agent: task.Claude, ID: id}, Outcome: agent.Outcome{
		Status: agent.PROpened, Summary: "opened", PRNumber: 912, Validation: []task.Check{{Command: "go test ./...", Passed: true}},
	}}
}

func findings(sev string) agent.ReviewOutcome {
	return agent.ReviewOutcome{Summary: "one problem", Findings: []task.Finding{{Severity: sev, File: "retry.go", Line: 40, Summary: "4xx retried"}}}
}

func started(t *testing.T, f fixture, req StartRequest) task.Key {
	rec, err := f.app.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return rec.Key
}

func TestReviewLoopCleanFirstRound(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Reviewing || len(got.Reviews) != 1 || !got.Reviews[0].Clean || got.Reviews[0].Reviewer != task.Codex || got.Reviews[0].PR != 912 {
		t.Fatalf("state %s reviews %+v", got.State, got.Reviews)
	}
	if len(f.codex.reviewed) != 1 || len(f.claude.reviewed) != 0 {
		t.Fatalf("wrong reviewer: codex %d claude %d", len(f.codex.reviewed), len(f.claude.reviewed))
	}
	p := f.codex.reviewed[0]
	if !strings.Contains(p, "pull request #912") || !strings.Contains(p, "5xx-only retry semantics") || strings.Contains(p, "sid-1") {
		t.Errorf("review prompt must carry durable context and no implementer session:\n%s", p)
	}
	if got.Delegation.RunnerPID != 0 || got.LastOutcome.Status != "review_clean" {
		t.Errorf("pid %d outcome %+v", got.Delegation.RunnerPID, got.LastOutcome)
	}
}

func TestReviewLoopFixesFindingsThenReReviews(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{prTurn("sid-1"), prTurn("sid-1")}
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Reviewing || len(got.Reviews) != 2 || got.Reviews[0].Clean || !got.Reviews[1].Clean || got.Reviews[1].Round != 2 {
		t.Fatalf("state %s reviews %+v", got.State, got.Reviews)
	}
	if len(f.claude.resumed) != 1 || !strings.Contains(f.claude.resumed[0], "sid-1: Review round 1 of PR #912") || !strings.Contains(f.claude.resumed[0], "retry.go:40") {
		t.Errorf("implementer not resumed with findings: %q", f.claude.resumed)
	}
	if !strings.Contains(f.codex.reviewed[1], "Round 1 (one problem)") {
		t.Errorf("re-review lacks earlier round:\n%s", f.codex.reviewed[1])
	}
}

func TestReviewLoopEscalatesAfterRoundCapAndResumesOnAnswer(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking"), findings("blocking"), findings("should_fix")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	q := got.Pending()
	if got.State != task.NeedsDecision || len(got.Reviews) != 3 || q == nil || !strings.Contains(q.Question.Issue, "after 3 rounds") {
		t.Fatalf("state %s reviews %d pending %+v", got.State, len(got.Reviews), q)
	}
	if q.Question.Recommendation != "B" || !strings.Contains(q.Question.Context, "[should_fix] 4xx retried") {
		t.Errorf("question = %+v", q.Question)
	}
	if err := q.Question.Validate(); err != nil {
		t.Errorf("escalation question invalid: %v", err)
	}

	if _, err := f.app.Respond(context.Background(), key, "B"); err != nil {
		t.Fatal(err)
	}
	resumedBefore := len(f.claude.resumed)
	if err := f.app.RunTask(context.Background(), key, 4243, io.Discard); err != nil {
		t.Fatal(err)
	}
	got = f.store.recs[key]
	if got.State != task.Reviewing || !got.LatestReview().Clean || len(got.Reviews) != 4 {
		t.Fatalf("after answer: state %s reviews %d", got.State, len(got.Reviews))
	}
	if !strings.Contains(f.claude.resumed[resumedBefore], "Decision: B. Leave the code as is") {
		t.Errorf("answer not delivered: %q", f.claude.resumed[resumedBefore:])
	}
	if !strings.Contains(f.codex.reviewed[len(f.codex.reviewed)-1], "A: B. Leave the code as is") {
		t.Error("reviewer not told about the decision")
	}
}

func TestReviewLoopStopsWhenImplementerBlocksDuringFix(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	q := retryQuestion
	f.claude.turns = []agent.Turn{prTurn("sid-1"), {Session: task.Session{Agent: task.Claude, ID: "sid-1"}, Outcome: agent.Outcome{Status: agent.Blocked, Summary: "?", Question: &q}}}
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking")}
	f.app.RunTask(context.Background(), key, 4242, io.Discard)
	if got := f.store.recs[key]; got.State != task.NeedsDecision || len(got.Reviews) != 1 {
		t.Fatalf("state %s reviews %d", got.State, len(got.Reviews))
	}
}

func TestReviewFailureFailsTask(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	f.codex.reviewErr = errors.New("codex never started a thread")
	f.app.RunTask(context.Background(), key, 4242, io.Discard)
	got := f.store.recs[key]
	if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "codex review failed") || got.Delegation.PR != 912 {
		t.Fatalf("state %s outcome %+v pr %d", got.State, got.LastOutcome, got.Delegation.PR)
	}
}

func TestCodexImplementationIsReviewedByClaude(t *testing.T) {
	f := newFixture()
	req := startReq(t)
	req.Agent = task.Codex
	key := started(t, f, req)
	f.codex.turn = agent.Turn{Session: task.Session{Agent: task.Codex, ID: "th-1"}, Outcome: prTurn("").Outcome}
	f.app.RunTask(context.Background(), key, 4242, io.Discard)
	got := f.store.recs[key]
	if got.State != task.Reviewing || len(f.claude.reviewed) != 1 || len(f.codex.reviewed) != 0 || got.Reviews[0].Reviewer != task.Claude {
		t.Fatalf("state %s claude reviews %d codex reviews %d", got.State, len(f.claude.reviewed), len(f.codex.reviewed))
	}
}

func TestFreshTurnCarriesOpenFindingsWhenSessionIsGone(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{prTurn("sid-1"), prTurn("sid-2")}
	f.claude.resumeErr = errors.Join(errors.New("gone"), agent.ErrNoSession)
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking")}
	f.app.RunTask(context.Background(), key, 4242, io.Discard)
	if !strings.Contains(f.claude.prompt, "## Outstanding review findings") || !strings.Contains(f.claude.prompt, "[blocking] 4xx retried (retry.go)") {
		t.Errorf("fresh prompt lacks findings:\n%s", f.claude.prompt)
	}
	if got := f.store.recs[key]; got.State != task.Reviewing || !got.LatestReview().Clean {
		t.Errorf("state %s", got.State)
	}
}

func TestImplementationPromptGetsOnlyImplementInstructions(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	p := f.claude.prompt
	if !strings.Contains(p, "## Developer workflow instructions") || !strings.Contains(p, "implement instructions") {
		t.Fatalf("implementation prompt lacks implement instructions:\n%s", p)
	}
	if strings.Contains(p, "review instructions") || strings.Contains(p, "pr instructions") {
		t.Errorf("implementation prompt carries another stage's instructions:\n%s", p)
	}
	if got := f.stages.calls[0]; got != "/wt/ridezoomo-armada-backend/825-provider-retries implement" {
		t.Errorf("resolved %q; want the worktree root and the implement stage", got)
	}
}

func TestUnreadableStageInstructionsFailTheTask(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.stages.err = errors.New("stage instructions /cfg/stages/implement.md: permission denied")
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "implement.md: permission denied") || f.claude.starts != 0 {
		t.Errorf("state %s outcome %+v starts %d", got.State, got.LastOutcome, f.claude.starts)
	}
}

func TestReviewPromptGetsOnlyReviewInstructions(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	p := f.codex.reviewed[0]
	if !strings.Contains(p, "## Developer workflow instructions") || !strings.Contains(p, "review instructions") {
		t.Fatalf("review prompt lacks review instructions:\n%s", p)
	}
	if strings.Contains(p, "implement instructions") || strings.Contains(p, "pr instructions") {
		t.Errorf("review prompt carries another stage's instructions:\n%s", p)
	}
	if got := f.stages.calls; len(got) != 2 || got[1] != "/wt/ridezoomo-armada-backend/825-provider-retries review" {
		t.Errorf("lookups %q; want implement then review, both at the worktree root", got)
	}
}

// failingStage fails only one stage's lookup.
type failingStage struct {
	fakeStages
	stage task.Stage
}

func (f *failingStage) Resolve(root string, s task.Stage) (task.StageInstructions, error) {
	if s == f.stage {
		return task.StageInstructions{}, errors.New("stage instructions /repo/.foreman/review.md: not a regular file")
	}
	return f.fakeStages.Resolve(root, s)
}

func TestUnreadableReviewInstructionsFailTheReview(t *testing.T) {
	f := newFixture()
	f.app.Stages = &failingStage{stage: task.StageReview}
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "review.md: not a regular file") || len(f.codex.reviewed) != 0 {
		t.Errorf("state %s outcome %+v reviews %d", got.State, got.LastOutcome, len(f.codex.reviewed))
	}
}
