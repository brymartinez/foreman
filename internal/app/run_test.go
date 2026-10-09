package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
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
	if !strings.Contains(p, "pull request #912") || !strings.Contains(p, "> Retry 5xx only.") || strings.Contains(p, "5xx-only retry semantics") || strings.Contains(p, "sid-1") {
		t.Errorf("review prompt must carry the PR description, no context, and no implementer session:\n%s", p)
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
	if !strings.Contains(f.codex.reviewed[0], "review_rounds to 3. This is the first review") || !strings.Contains(f.codex.reviewed[1], "This is a re-review") {
		t.Errorf("reviews must carry review_rounds and say whether they re-review:\n%s", f.codex.reviewed)
	}
}

func TestReviewLoopEscalatesAfterRoundCapAndResumesOnAnswer(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking"), findings("blocking"), findings("blocking"), findings("should_fix")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	q := got.Pending()
	// Implement, then 3 fixes, each reviewed: 4 reviews, and the 4th's findings are unaddressed.
	if got.State != task.NeedsDecision || len(got.Reviews) != 4 || q == nil || !strings.Contains(q.Question.Issue, "after 3 rounds of fixes") {
		t.Fatalf("state %s reviews %d pending %+v", got.State, len(got.Reviews), q)
	}
	if len(f.claude.resumed) != 3 {
		t.Errorf("implementer fix turns = %d, want 3", len(f.claude.resumed))
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
	if got.State != task.Reviewing || !got.LatestReview().Clean || len(got.Reviews) != 5 {
		t.Fatalf("after answer: state %s reviews %d", got.State, len(got.Reviews))
	}
	if !strings.Contains(f.claude.resumed[resumedBefore], "Decision: B. Leave the code as is") || !strings.Contains(f.claude.resumed[resumedBefore], "Review round 4 of PR #912 requested these changes") {
		t.Errorf("answer not delivered: %q", f.claude.resumed[resumedBefore:])
	}
	// The reviewer judges the change by the PR description alone, not the developer's
	// answers to the implementer.
	if strings.Contains(f.codex.reviewed[len(f.codex.reviewed)-1], "Leave the code as is") {
		t.Error("reviewer was shown the developer's decision")
	}
}

func TestFixRoundsCapsTheLoopApartFromReviewRounds(t *testing.T) {
	f := newFixture()
	f.app.FixRounds, f.app.ReviewRounds = 1, 5
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking"), findings("blocking")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.NeedsDecision || len(got.Reviews) != 2 || len(f.claude.resumed) != 1 || !strings.Contains(got.Pending().Question.Issue, "after 1 round of fixes") {
		t.Fatalf("state %s reviews %d", got.State, len(got.Reviews))
	}
	if !strings.Contains(f.codex.reviewed[0], "review_rounds to 5.") {
		t.Errorf("review passes must follow review_rounds:\n%s", f.codex.reviewed[0])
	}
}

func TestReviewLoopStopsWhenImplementerBlocksDuringFix(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	q := retryQuestion
	f.claude.turns = []agent.Turn{prTurn("sid-1"), {Session: task.Session{Agent: task.Claude, ID: "sid-1"}, Outcome: agent.Outcome{Status: agent.Blocked, Summary: "?", Questions: []task.Question{q}}}}
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

func TestCodexPRUsesCodexReviewWhenAxiConfirmsClaudeNotSetUp(t *testing.T) {
	f := newFixture()
	f.app.Quota = fakeQuota{q: map[task.Agent]Quota{task.Claude: {Status: QuotaNotSetUp}}}
	req := startReq(t)
	req.Agent = task.Codex
	key := started(t, f, req)
	f.codex.turn = agent.Turn{Session: task.Session{Agent: task.Codex, ID: "th-1"}, Outcome: prTurn("").Outcome}
	f.app.RunTask(context.Background(), key, 4242, io.Discard)
	got := f.store.recs[key]
	if got.State != task.Reviewing || len(f.codex.reviewed) != 1 || len(f.claude.reviewed) != 0 || got.Reviews[0].Reviewer != task.Codex || got.Delegation.Reviewer != task.Codex {
		t.Fatalf("state %s reviewer %s reviews %+v; codex %d claude %d", got.State, got.Delegation.Reviewer, got.Reviews, len(f.codex.reviewed), len(f.claude.reviewed))
	}
}

func TestCodexPRFallsBackToCodexReviewWhenClaudeAuthenticationFails(t *testing.T) {
	f := newFixture()
	req := startReq(t)
	req.Agent = task.Codex
	key := started(t, f, req)
	f.codex.turn = agent.Turn{Session: task.Session{Agent: task.Codex, ID: "th-1"}, Outcome: prTurn("").Outcome}
	f.claude.reviewErr = fmt.Errorf("OAuth session expired: %w", agent.ErrUnavailable)
	f.app.RunTask(context.Background(), key, 4242, io.Discard)
	got := f.store.recs[key]
	if got.State != task.Reviewing || len(f.claude.reviewed) != 1 || len(f.codex.reviewed) != 1 || got.Reviews[0].Reviewer != task.Codex || got.Delegation.Reviewer != task.Codex {
		t.Fatalf("state %s reviewer %s reviews %+v; codex %d claude %d", got.State, got.Delegation.Reviewer, got.Reviews, len(f.codex.reviewed), len(f.claude.reviewed))
	}
	got.State = task.Failed
	resumed := started(t, f, req)
	if f.store.recs[resumed].Delegation.Reviewer != task.Codex {
		t.Fatalf("resumed reviewer = %s, want codex", f.store.recs[resumed].Delegation.Reviewer)
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

// The implementer opens the PR in the same turn, so it gets implement and pr.
func TestImplementationPromptGetsImplementAndPRInstructionsOnly(t *testing.T) {
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
	if !strings.Contains(p, "## Pull request description") || !strings.Contains(p, "pr instructions") {
		t.Errorf("implementation prompt lacks pr instructions:\n%s", p)
	}
	if strings.Contains(p, "review instructions") {
		t.Errorf("implementation prompt carries review instructions:\n%s", p)
	}
	root := "/wt/ridezoomo-armada-backend/825-provider-retries "
	if got := f.stages.calls[:2]; !slices.Equal(got, []string{root + "implement", root + "pr"}) {
		t.Errorf("resolved %q; want implement and pr at the worktree root", got)
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
	if got := f.stages.calls; len(got) != 4 || got[2] != "/wt/ridezoomo-armada-backend/825-provider-retries plan" || got[3] != "/wt/ridezoomo-armada-backend/825-provider-retries review" {
		t.Errorf("lookups %q; want implement, pr, plan, then review, all at the worktree root", got)
	}
}

func TestReviewPromptUsesPRBodyNotIssue(t *testing.T) {
	f := newFixture()
	f.gh.prComments = map[int][]PRComment{912: {{Kind: task.IssueComment, ID: 1, Author: "dev", Body: "Also handle 429."}}}
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	p := f.codex.reviewed[0]
	for _, want := range []string{"> Retry 5xx only.", "> Also handle 429."} {
		if !strings.Contains(p, want) {
			t.Errorf("review prompt missing %q:\n%s", want, p)
		}
	}
	// The issue body, the context file, and the implementer's validation claim.
	for _, leaked := range []string{"Retry failed requests.", "https://x/825", "Preserve the existing 5xx-only", "go test ./...: passed"} {
		if strings.Contains(p, leaked) {
			t.Errorf("review prompt carries %q:\n%s", leaked, p)
		}
	}
}

func TestReviewStopsWhenPRDescriptionIsEmpty(t *testing.T) {
	f := newFixture()
	pr := f.gh.prs[912]
	pr.Body = " \n "
	f.gh.prs[912] = pr
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "the pull request description is empty") || len(f.codex.reviewed) != 0 || len(got.Reviews) != 0 {
		t.Errorf("state %s outcome %+v reviews %d", got.State, got.LastOutcome, len(f.codex.reviewed))
	}
}

func TestReviewStopsWhenPRCannotBeRead(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	rec := *f.store.recs[key]
	d := *rec.Delegation
	d.PR, d.Reviewer = 913, task.Codex
	rec.Delegation = &d
	_, _, err := f.app.review(context.Background(), &rec, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no pull requests found") || len(f.codex.reviewed) != 0 {
		t.Errorf("err %v reviews %d", err, len(f.codex.reviewed))
	}
}

func TestReviewRunsWhenPRCommentsCannotBeRead(t *testing.T) {
	f := newFixture()
	f.gh.prCommentErr = errors.New("gh: HTTP 502")
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(f.codex.reviewed) != 1 || !strings.Contains(f.codex.reviewed[0], "could not be loaded (gh: HTTP 502)") {
		t.Errorf("review must run and name the missing comments: %q", f.codex.reviewed)
	}
}

// failingStage fails only one stage's lookup.
type failingStage struct {
	fakeStages
	stage task.Stage
}

func (f *failingStage) Resolve(root string, s task.Stage) (task.StageInstructions, error) {
	if s == f.stage {
		return task.StageInstructions{}, fmt.Errorf("stage instructions /repo/.foreman/%s.md: not a regular file", s)
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

func TestUnreadablePRInstructionsFailBeforeTheImplementerRuns(t *testing.T) {
	f := newFixture()
	f.app.Stages = &failingStage{stage: task.StagePR}
	key := started(t, f, startReq(t))
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.Failed || f.claude.starts != 0 {
		t.Errorf("state %s starts %d", got.State, f.claude.starts)
	}
}

func TestRunTaskStopsWhenTheTaskFileIsRemoved(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	f.claude.during = func() { delete(f.store.recs, key) }
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); !errors.Is(err, errTaskRemoved) {
		t.Fatalf("err = %v, want errTaskRemoved", err)
	}
	if _, ok := f.store.recs[key]; ok {
		t.Error("the runner recreated a removed task")
	}
}

func TestRunTaskStopsWhenThePRIsClosed(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	// The gate ends the task while the agent works, as it does when the developer
	// closes the PR. The runner must record nothing over it, least of all a failure.
	f.claude.during = func() { f.store.recs[key].State = task.PRClosed }
	f.notes.sent = nil
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); !errors.Is(err, errPRClosed) {
		t.Fatalf("err = %v, want errPRClosed", err)
	}
	if got := f.store.recs[key]; got.State != task.PRClosed {
		t.Errorf("the runner moved an ended task to %s", got.State)
	}
	if len(f.notes.sent) != 0 {
		t.Errorf("the runner notified the developer: %v", f.notes.sent)
	}
}

func TestRunTaskRecordsACancelledTurn(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	ctx, cancel := context.WithCancelCause(context.Background())
	f.claude.turn = agent.Turn{Session: task.Session{Agent: task.Claude, ID: "sid-1"}, Usage: task.Usage{ClaudeCostUSD: 1.25, ClaudeTurns: 1}}
	f.claude.err = errors.New("claude printed no result (signal: terminated)")
	f.claude.during = func() { cancel(errors.New("terminated signal received")) }
	var log strings.Builder
	if err := f.app.RunTask(ctx, key, 4242, &log); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || got.LastOutcome.Status != Cancelled || got.LastOutcome.Summary != "run cancelled during planning (terminated signal received)" {
		t.Fatalf("state %s outcome %+v", got.State, got.LastOutcome)
	}
	if got.Delegation.Implementer.ID != "sid-1" || got.Usage.ClaudeCostUSD != 1.25 || got.Delegation.RunnerPID != 0 {
		t.Errorf("a cancelled turn must keep its session and usage: %+v %+v", got.Delegation, got.Usage)
	}
	if !strings.Contains(log.String(), "foreman: stopped during planning: terminated signal received") {
		t.Errorf("log = %q", log.String())
	}
	if len(f.notes.sent) == 0 || !strings.Contains(f.notes.sent[len(f.notes.sent)-1], "Run cancelled") {
		t.Errorf("notifications = %q", f.notes.sent)
	}
}

func TestRunTaskRecordsACancelledReview(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	ctx, cancel := context.WithCancel(context.Background())
	f.claude.turn = prTurn("sid-1")
	f.claude.during = cancel
	f.codex.reviewErr = errors.New("codex never started a thread")
	if err := f.app.RunTask(ctx, key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || got.LastOutcome.Status != Cancelled || !strings.HasPrefix(got.LastOutcome.Summary, "run cancelled during review") || got.Delegation.PR != 912 {
		t.Fatalf("state %s outcome %+v pr %d", got.State, got.LastOutcome, got.Delegation.PR)
	}
}

func TestAFailedTurnWithoutACancelIsNotCancelled(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.err = errors.New("claude exited: exit status 1")
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.Failed || got.LastOutcome.Status != "failed" {
		t.Fatalf("state %s outcome %+v", got.State, got.LastOutcome)
	}
}

func plannedTurn(id string) agent.Turn {
	return agent.Turn{Session: task.Session{Agent: task.Claude, ID: id}, Usage: task.Usage{ClaudeCostUSD: 0.5, ClaudeTurns: 1}, Outcome: agent.Outcome{
		Status: agent.Planned, Summary: "planned",
		Documents: map[task.PlanDoc]string{task.Analysis: "Retries 5xx only today.", task.PRD: "R1. Retry 5xx.", task.Design: "Wrap the client.", task.ImplementationPlan: "### Task 1"},
	}}
}

func TestPlanIsPostedThenAFreshSessionImplementsIt(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2")}
	var log strings.Builder
	if err := f.app.RunTask(context.Background(), key, 4242, &log); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Reviewing || !got.PlanPosted() || len(got.Plan) != 4 {
		t.Fatalf("state %s plan %+v", got.State, got.Plan)
	}
	for i, doc := range task.PlanDocs {
		c := got.Plan[i]
		body := f.gh.comments[c.CommentID].Body
		if c.Doc != doc || !strings.HasPrefix(body, "<!-- foreman:"+string(doc)+" -->\n## "+doc.Title()+"\n\n") {
			t.Errorf("comment %d = %+v body %q", i, c, body)
		}
	}
	if len(f.claude.prompts) != 2 || !strings.Contains(f.claude.prompts[0], "## Plan first") || !strings.Contains(f.claude.prompts[0], "plan instructions") {
		t.Errorf("the first turn must plan: %q", f.claude.prompts)
	}
	if f.claude.starts != 2 || len(f.claude.resumed) != 0 || got.Delegation.Implementer.ID != "sid-2" || !strings.Contains(f.claude.prompt, "- Implementation plan: https://github.com/") || !strings.Contains(f.claude.prompt, "implement instructions") || strings.Contains(f.claude.prompt, "## Plan first") {
		t.Errorf("implementation must start fresh with the posted plan and instructions: starts %d resumed %q session %q prompt %q", f.claude.starts, f.claude.resumed, got.Delegation.Implementer.ID, f.claude.prompt)
	}
	if !slices.Equal(f.claude.stages, []task.Stage{task.StagePlan, task.StageImplement}) {
		t.Errorf("implementer stages = %v", f.claude.stages)
	}
	if strings.Contains(f.codex.reviewed[0], "## PRD") || strings.Contains(f.codex.reviewed[0], "R1. Retry 5xx.") {
		t.Errorf("the review must not carry the PRD:\n%s", f.codex.reviewed[0])
	}
	if got.Usage.ClaudeCostUSD != 0.5 || !strings.Contains(log.String(), "foreman: posted the PRD: https://") {
		t.Errorf("usage %+v log %q", got.Usage, log.String())
	}
}

func TestReviewFindingsResumeImplementationSessionAfterPlan(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2"), prTurn("sid-2")}
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.claude.starts != 2 || len(f.claude.resumed) != 1 || !strings.HasPrefix(f.claude.resumed[0], "sid-2: Review round 1 of PR #912") {
		t.Errorf("review findings must resume the implementation session: starts %d resumed %q", f.claude.starts, f.claude.resumed)
	}
	if !slices.Equal(f.claude.stages, []task.Stage{task.StagePlan, task.StageImplement, task.StageImplement}) {
		t.Errorf("implementer stages = %v", f.claude.stages)
	}
}

func TestReplanningEditsTheSameComments(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.gh.postErr, f.gh.postOK = errors.New("gh: HTTP 502"), 2
	f.claude.turns = []agent.Turn{plannedTurn("sid-1")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "posting the Design on the issue failed") || len(got.Plan) != 2 {
		t.Fatalf("state %s outcome %+v plan %+v", got.State, got.LastOutcome, got.Plan)
	}
	// Restarting plans again: it edits the analysis comment, and posts the PRD anew
	// because the developer deleted its comment.
	f.gh.postErr = nil
	delete(f.gh.comments, 1002)
	started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{plannedTurn("sid-2"), prTurn("sid-3")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got = f.store.recs[key]
	if !got.PlanPosted() || got.Plan[0].CommentID != 1001 || f.gh.edits != 1 {
		t.Errorf("plan %+v edits %d; want the analysis comment edited in place", got.Plan, f.gh.edits)
	}
}

func TestAnsweredPlanningQuestionResumesThePlanning(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	rec := f.store.recs[key]
	rec.Delegation.Implementer.ID = "sid-1"
	rec.Decisions = []task.Decision{{Question: task.Question{Issue: "Retry 4xx?"}, Answer: "A. 5xx only", AnsweredAt: f.app.Now()}}
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if len(f.claude.resumed) != 1 || !strings.Contains(f.claude.resumed[0], "planned, blocked, or failed") || f.claude.starts != 1 || got.Delegation.Implementer.ID != "sid-2" {
		t.Errorf("planning answer must resume before implementation starts fresh: resumed %q starts %d", f.claude.resumed, f.claude.starts)
	}
	if !slices.Equal(f.claude.stages, []task.Stage{task.StagePlan, task.StageImplement}) {
		t.Errorf("implementer stages = %v", f.claude.stages)
	}
}

func TestATaskWithAPRSkipsPlanning(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.store.recs[key].Delegation.PR = 912
	f.claude.turn = prTurn("sid-1")
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.claude.prompt, "## Plan first") || len(f.gh.comments) != 0 {
		t.Errorf("planned a task that already has a PR")
	}
}

func TestPlanCommentFitsGitHubsLimit(t *testing.T) {
	body := planCommentBody(task.ImplementationPlan, strings.Repeat("é", maxCommentChars))
	if n := len([]rune(body)); n > maxCommentChars || !strings.HasSuffix(body, "_Truncated at GitHub's comment length limit._") {
		t.Errorf("body is %d characters", n)
	}
}

func TestCodexPlansAndClaudeReviewsWithoutThePRD(t *testing.T) {
	f := newFixture()
	req := startReq(t)
	req.Agent = task.Codex
	key := started(t, f, req)
	planned := plannedTurn("th-1")
	planned.Session.Agent = task.Codex
	f.codex.turns = []agent.Turn{planned, {Session: task.Session{Agent: task.Codex, ID: "th-2"}, Outcome: prTurn("").Outcome}}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Reviewing || !got.PlanPosted() {
		t.Fatalf("state %s plan %+v", got.State, got.Plan)
	}
	if len(f.codex.prompts) != 2 || !strings.Contains(f.codex.prompts[0], "## Plan first") || !strings.Contains(f.codex.prompt, "- Implementation plan: https://github.com/") || len(f.codex.resumed) != 0 || got.Delegation.Implementer.ID != "th-2" {
		t.Errorf("codex must implement in a fresh thread: prompts %q resumed %q session %q", f.codex.prompts, f.codex.resumed, got.Delegation.Implementer.ID)
	}
	if len(f.claude.reviewed) != 1 || strings.Contains(f.claude.reviewed[0], "R1. Retry 5xx.") || !strings.Contains(f.claude.reviewed[0], "> Retry 5xx only.") {
		t.Errorf("claude's review must carry the PR description, not the PRD: %q", f.claude.reviewed)
	}
}

func TestPlanIsPostedAgainWhenItsCommentWasDeleted(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	// An earlier plan's analysis comment that the developer has since deleted.
	f.store.recs[key].SetPlanComment(task.PlanComment{Doc: task.Analysis, CommentID: 77, URL: "https://github.com/x/issues/825#issuecomment-77"})
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	c := got.PlanComment(task.Analysis)
	if !got.PlanPosted() || c == nil || c.CommentID == 77 || f.gh.edits != 0 {
		t.Fatalf("plan %+v edits %d; want the analysis posted as a new comment", got.Plan, f.gh.edits)
	}
	if _, ok := f.gh.comments[c.CommentID]; !ok {
		t.Errorf("comment %d was not posted", c.CommentID)
	}
}

func TestFailedPlanPostNamesTheDocument(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.gh.postErr = errors.New("gh: HTTP 502")
	f.claude.turns = []agent.Turn{plannedTurn("sid-1")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	want := "posting the Analysis on the issue failed: gh: HTTP 502"
	if got.State != task.Failed || got.LastOutcome.Status != "failed" || got.LastOutcome.Summary != want || len(got.Plan) != 0 {
		t.Fatalf("state %s outcome %+v plan %+v", got.State, got.LastOutcome, got.Plan)
	}
	if f.claude.starts != 1 {
		t.Errorf("starts = %d; a failed post must not start the implementation", f.claude.starts)
	}
}
