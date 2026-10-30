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
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	cr := got.CompetingReviews()
	if got.State != task.WaitingForCI || len(cr) != 1 || !cr[0].Clean || cr[0].Reviewer != task.Codex || cr[0].PR != 912 {
		t.Fatalf("state %s reviews %+v", got.State, got.Reviews)
	}
	// Claude's one review is its own self-review.
	if len(f.codex.reviewed) != 1 || len(f.claude.reviewed) != 1 {
		t.Fatalf("wrong reviewer: codex %d claude %d", len(f.codex.reviewed), len(f.claude.reviewed))
	}
	p := f.codex.reviewed[0]
	if !strings.Contains(p, "## Review scope") || strings.Contains(p, "Retry 5xx only.") || strings.Contains(p, "5xx-only retry semantics") || strings.Contains(p, "sid-1") {
		t.Errorf("competing review must see only the diff as task context:\n%s", p)
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
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	// Round 1 is the self-review. The fix keeps the head, so it still covers the change.
	cr := got.CompetingReviews()
	if got.State != task.WaitingForCI || len(cr) != 2 || cr[0].Clean || !cr[1].Clean || cr[1].Round != 3 {
		t.Fatalf("state %s reviews %+v", got.State, got.Reviews)
	}
	if len(f.claude.prompts) != 2 || !strings.Contains(f.claude.prompts[1], "## Findings to correct") || !strings.Contains(f.claude.prompts[1], "retry.go:40") {
		t.Errorf("correction did not start fresh with findings: %q", f.claude.prompts)
	}
	if strings.Contains(f.codex.reviewed[1], "Round 2 (one problem)") {
		t.Errorf("re-review leaked an earlier round:\n%s", f.codex.reviewed[1])
	}
	if !strings.Contains(f.codex.reviewed[0], "Read the complete diff 3 times") || !strings.Contains(f.codex.reviewed[1], "Read the complete diff 3 times") {
		t.Errorf("reviews must honor review_rounds:\n%s", f.codex.reviewed)
	}
}

func TestReviewLoopEscalatesAfterRoundCapAndResumesOnAnswer(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	postedPlan(f, f.store.recs[key])
	f.claude.turn = prTurn("sid-1")
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking"), findings("blocking"), findings("blocking"), findings("should_fix")}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	q := got.Pending()
	// Implement, then 3 fixes, each reviewed: 4 competing reviews after one clean
	// self-review, and the 4th's findings are unaddressed.
	if got.State != task.NeedsDecision || len(got.CompetingReviews()) != 4 || q == nil || !strings.Contains(q.Question.Issue, "after 3 rounds of fixes") {
		t.Fatalf("state %s reviews %d pending %+v", got.State, len(got.Reviews), q)
	}
	if f.claude.starts != 4 {
		t.Errorf("fresh implementer and correction turns = %d, want 4", f.claude.starts)
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
	startsBefore := f.claude.starts
	if err := f.run(context.Background(), key, 4243, io.Discard); err != nil {
		t.Fatal(err)
	}
	got = f.store.recs[key]
	if got.State != task.WaitingForCI || !got.LatestReview().Clean || len(got.CompetingReviews()) != 5 {
		t.Fatalf("after answer: state %s reviews %d", got.State, len(got.Reviews))
	}
	if f.claude.starts <= startsBefore || !strings.Contains(f.claude.prompts[len(f.claude.prompts)-1], "Answer: B. Leave the code as is") {
		t.Errorf("answer not delivered to fresh correction turn: %q", f.claude.prompts)
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
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.NeedsDecision || len(got.CompetingReviews()) != 2 || f.claude.starts != 2 || !strings.Contains(got.Pending().Question.Issue, "after 1 round of fixes") {
		t.Fatalf("state %s reviews %d", got.State, len(got.Reviews))
	}
	if !strings.Contains(f.codex.reviewed[0], "Read the complete diff 5 times") {
		t.Errorf("review passes must follow review_rounds:\n%s", f.codex.reviewed[0])
	}
}

func TestReviewLoopStopsWhenImplementerBlocksDuringFix(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	q := retryQuestion
	f.claude.turns = []agent.Turn{prTurn("sid-1"), {Session: task.Session{Agent: task.Claude, ID: "sid-1"}, Outcome: agent.Outcome{Status: agent.Blocked, Summary: "?", Questions: []task.Question{q}}}}
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking")}
	f.run(context.Background(), key, 4242, io.Discard)
	if got := f.store.recs[key]; got.State != task.NeedsDecision || len(got.CompetingReviews()) != 1 {
		t.Fatalf("state %s reviews %d", got.State, len(got.Reviews))
	}
}

func TestReviewFailureFailsTask(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	f.codex.reviewErr = errors.New("codex never started a thread")
	f.run(context.Background(), key, 4242, io.Discard)
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
	f.run(context.Background(), key, 4242, io.Discard)
	got := f.store.recs[key]
	// Codex's one review is its own self-review.
	if got.State != task.WaitingForCI || len(f.claude.reviewed) != 1 || len(f.codex.reviewed) != 1 || got.LatestCompeting().Reviewer != task.Claude {
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
	f.run(context.Background(), key, 4242, io.Discard)
	got := f.store.recs[key]
	// Codex self-reviews, then reviews again in a separate session in Claude's place.
	if got.State != task.WaitingForCI || len(f.codex.reviewed) != 2 || len(f.claude.reviewed) != 0 || got.LatestCompeting().Reviewer != task.Codex || got.Delegation.Reviewer != task.Codex {
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
	f.run(context.Background(), key, 4242, io.Discard)
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || len(f.claude.reviewed) != 1 || len(f.codex.reviewed) != 2 || got.LatestCompeting().Reviewer != task.Codex || got.Delegation.Reviewer != task.Codex {
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
	f.run(context.Background(), key, 4242, io.Discard)
	if !strings.Contains(f.claude.prompt, "## Findings to correct") || !strings.Contains(f.claude.prompt, "[blocking] 4xx retried") {
		t.Errorf("fresh prompt lacks findings:\n%s", f.claude.prompt)
	}
	if got := f.store.recs[key]; got.State != task.WaitingForCI || !got.LatestReview().Clean {
		t.Errorf("state %s", got.State)
	}
}

// The implementer opens the PR in the same turn, so it gets implement and pr.
func TestImplementationPromptGetsImplementAndPRInstructionsOnly(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	postedPlan(f, f.store.recs[key])
	f.claude.turn = prTurn("sid-1")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
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
	root := "/wt/testorg-example/feat/825-provider-retries "
	if got := f.stages.calls[:2]; !slices.Equal(got, []string{root + "implement", root + "pr"}) {
		t.Errorf("resolved %q; want implement and pr at the worktree root", got)
	}
}

func TestUnreadableStageInstructionsFailTheTask(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.stages.err = errors.New("stage instructions /cfg/stages/implement.md: permission denied")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
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
	postedPlan(f, f.store.recs[key])
	f.claude.turn = prTurn("sid-1")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	p := f.codex.reviewed[0]
	if !strings.Contains(p, "## Developer workflow instructions") || !strings.Contains(p, "review instructions") {
		t.Fatalf("review prompt lacks review instructions:\n%s", p)
	}
	if strings.Contains(p, "implement instructions") || strings.Contains(p, "pr instructions") {
		t.Errorf("review prompt carries another stage's instructions:\n%s", p)
	}
	if got := f.stages.calls; len(got) != 4 || got[0] != "/wt/testorg-example/feat/825-provider-retries implement" || got[1] != "/wt/testorg-example/feat/825-provider-retries pr" || got[2] != "/wt/testorg-example/feat/825-provider-retries review" || got[3] != got[2] {
		t.Errorf("lookups %q; want implement, pr, then two review lookups", got)
	}
}

func TestCompetingReviewPromptOmitsPRBodyAndConfirmedScope(t *testing.T) {
	f := newFixture()
	f.gh.prComments = map[int][]PRComment{912: {{Kind: task.IssueComment, ID: 1, Author: "dev", Body: "Also handle 429."}}}
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	p := f.codex.reviewed[0]
	for _, excluded := range []string{"> Retry 5xx only.", "> Also handle 429.", "## Confirmed triage scope", "> Retry failed requests.", "> Source: https://x/825"} {
		if strings.Contains(p, excluded) {
			t.Errorf("review prompt leaked %q:\n%s", excluded, p)
		}
	}
	// Context and the implementer's validation claim remain private to implementation.
	for _, leaked := range []string{"Preserve the existing 5xx-only", "go test ./...: passed"} {
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
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
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
	_, _, err := f.app.reviewDescription(context.Background(), &rec)
	if err == nil || !strings.Contains(err.Error(), "no pull requests found") || len(f.codex.reviewed) != 0 {
		t.Errorf("err %v reviews %d", err, len(f.codex.reviewed))
	}
}

func TestReviewRunsWhenPRCommentsCannotBeRead(t *testing.T) {
	f := newFixture()
	f.gh.prCommentErr = errors.New("gh: HTTP 502")
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(f.codex.reviewed) != 1 || strings.Contains(f.codex.reviewed[0], "gh: HTTP 502") {
		t.Errorf("review must run without PR comments: %q", f.codex.reviewed)
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
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
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
	postedPlan(f, f.store.recs[key])
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
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
	if err := f.run(context.Background(), key, 4242, io.Discard); !errors.Is(err, errTaskRemoved) {
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
	if err := f.run(context.Background(), key, 4242, io.Discard); !errors.Is(err, errPRClosed) {
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
	if err := f.run(ctx, key, 4242, &log); err != nil {
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
	if err := f.run(ctx, key, 4242, io.Discard); err != nil {
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
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.Failed || got.LastOutcome.Status != "failed" {
		t.Fatalf("state %s outcome %+v", got.State, got.LastOutcome)
	}
}

func plannedTurn(id string) agent.Turn {
	return agent.Turn{Session: task.Session{Agent: task.Claude, ID: id}, Usage: task.Usage{ClaudeCostUSD: 0.5, ClaudeTurns: 1}, Outcome: agent.Outcome{
		Status: agent.Planned, Summary: "planned",
		Documents: map[task.PlanDoc]string{task.PRD: "R1. Retry 5xx.", task.ImplementationPlan: "### Task 1: Wrap the client (R1)"},
	}}
}

func TestPlanIsPostedThenAFreshSessionImplementsIt(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2")}
	var log strings.Builder
	if err := f.run(context.Background(), key, 4242, &log); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || !got.PlanPosted() || len(got.Plan) != 2 {
		t.Fatalf("state %s plan %+v", got.State, got.Plan)
	}
	for i, doc := range task.PlanDocs {
		c := got.Plan[i]
		body := f.gh.comments[c.CommentID].Body
		if c.Doc != doc || !strings.HasPrefix(body, task.PlanMarker(doc, task.DescriptionFingerprint(f.gh.issue.Body))+"\n## "+doc.Title()+"\n\n") {
			t.Errorf("comment %d = %+v body %q", i, c, body)
		}
	}
	if len(f.claude.prompts) != 2 || !strings.Contains(f.claude.prompts[0], "## Plan first") || !strings.Contains(f.claude.prompts[0], "plan instructions") {
		t.Errorf("the first turn must plan: %q", f.claude.prompts)
	}
	if f.claude.starts != 2 || len(f.claude.resumed) != 0 || got.Delegation.Implementer.ID != "sid-2" || !strings.Contains(f.claude.prompt, "### Task 1") || !strings.Contains(f.claude.prompt, "implement instructions") || strings.Contains(f.claude.prompt, "## Plan first") {
		t.Errorf("implementation must start fresh with the posted plan and instructions: starts %d resumed %q session %q prompt %q", f.claude.starts, f.claude.resumed, got.Delegation.Implementer.ID, f.claude.prompt)
	}
	if !slices.Equal(f.claude.stages, []task.Stage{task.StagePlan, task.StageImplement}) {
		t.Errorf("implementer stages = %v", f.claude.stages)
	}
	if !strings.Contains(f.claude.reviewed[0], "## Requirements") || !strings.Contains(f.claude.reviewed[0], "### Task 1") || strings.Contains(f.codex.reviewed[0], "### Task 1") {
		t.Errorf("the self-review needs the plan, competing review does not: self %q competing %q", f.claude.reviewed[0], f.codex.reviewed[0])
	}
	if got.Usage.ClaudeCostUSD != 0.5 || !strings.Contains(log.String(), "foreman: posted the Requirements: https://") {
		t.Errorf("usage %+v log %q", got.Usage, log.String())
	}
}

func TestReviewFindingsStartFreshCorrectionSessionAfterPlan(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2"), prTurn("sid-2")}
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking")}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.claude.starts != 3 || len(f.claude.resumed) != 0 || !strings.Contains(f.claude.prompts[2], "## Findings to correct") {
		t.Errorf("review findings must start a correction session: starts %d resumed %q", f.claude.starts, f.claude.resumed)
	}
	if !slices.Equal(f.claude.stages, []task.Stage{task.StagePlan, task.StageImplement, task.StageImplement}) {
		t.Errorf("implementer stages = %v", f.claude.stages)
	}
}

func TestReplanningEditsTheSameComments(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.gh.postErr, f.gh.postOK = errors.New("gh: HTTP 502"), 1
	f.claude.turns = []agent.Turn{plannedTurn("sid-1")}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "posting the Work plan on the issue failed") || len(got.Plan) != 1 {
		t.Fatalf("state %s outcome %+v plan %+v", got.State, got.LastOutcome, got.Plan)
	}
	// Restarting plans again edits Requirements and posts the missing Work plan.
	f.gh.postErr = nil
	started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{plannedTurn("sid-2"), prTurn("sid-3")}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got = f.store.recs[key]
	if !got.PlanPosted() || got.Plan[0].CommentID != 1001 || f.gh.edits != 1 {
		t.Errorf("plan %+v edits %d; want Requirements edited in place", got.Plan, f.gh.edits)
	}
}

func TestAnsweredPlanningQuestionResumesThePlanning(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	rec := f.store.recs[key]
	rec.Delegation.Implementer.ID = "sid-1"
	rec.Decisions = []task.Decision{{Question: task.Question{Issue: "Retry 4xx?"}, Answer: "A. 5xx only", AnsweredAt: f.app.Now()}}
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2")}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
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
	postedPlan(f, f.store.recs[key])
	f.store.recs[key].Delegation.PR = 912
	f.claude.turn = prTurn("sid-1")
	comments := len(f.gh.comments)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.claude.prompt, "## Plan first") || len(f.gh.comments) != comments {
		t.Errorf("planned a task that already has a PR")
	}
}

func TestPlanCommentFitsGitHubsLimit(t *testing.T) {
	body := planCommentBody(task.ImplementationPlan, task.DescriptionFingerprint("body"), strings.Repeat("é", maxCommentChars))
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
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || !got.PlanPosted() {
		t.Fatalf("state %s plan %+v", got.State, got.Plan)
	}
	if len(f.codex.prompts) != 2 || !strings.Contains(f.codex.prompts[0], "## Plan first") || !strings.Contains(f.codex.prompt, "### Task 1") || len(f.codex.resumed) != 0 || got.Delegation.Implementer.ID != "th-2" {
		t.Errorf("codex must implement in a fresh thread: prompts %q resumed %q session %q", f.codex.prompts, f.codex.resumed, got.Delegation.Implementer.ID)
	}
	if len(f.claude.reviewed) != 1 || strings.Contains(f.claude.reviewed[0], "R1. Retry 5xx.") || strings.Contains(f.claude.reviewed[0], "> Retry 5xx only.") {
		t.Errorf("claude's competing review must see only the diff: %q", f.claude.reviewed)
	}
}

func TestPlanIsPostedAgainWhenItsCommentWasDeleted(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	// An earlier Requirements comment that the developer has since deleted.
	f.store.recs[key].SetPlanComment(task.PlanComment{Doc: task.PRD, CommentID: 77, URL: "https://github.com/x/issues/825#issuecomment-77"})
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2")}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	c := got.PlanComment(task.PRD)
	if !got.PlanPosted() || c == nil || c.CommentID == 77 || f.gh.edits != 0 {
		t.Fatalf("plan %+v edits %d; want Requirements posted as a new comment", got.Plan, f.gh.edits)
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
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	want := "posting the Requirements on the issue failed: gh: HTTP 502"
	if got.State != task.Failed || got.LastOutcome.Status != "failed" || got.LastOutcome.Summary != want || len(got.Plan) != 0 {
		t.Fatalf("state %s outcome %+v plan %+v", got.State, got.LastOutcome, got.Plan)
	}
	if f.claude.starts != 1 {
		t.Errorf("starts = %d; a failed post must not start the implementation", f.claude.starts)
	}
}
