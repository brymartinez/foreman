package app

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// gatedLocalTask starts a task reviewed before its PR, with the full-suite gate on.
func gatedLocalTask(t *testing.T, f fixture) task.Key {
	t.Helper()
	f.app.FullSuiteGate = true
	return localTask(t, f)
}

func withFullRun(turn agent.Turn, report *agent.FullSuite) agent.Turn {
	turn.Outcome.Validation = []task.Check{{Command: "go test ./internal/app", Passed: true, Scope: task.FocusedCheck}, fullCheck(true)}
	turn.Outcome.FullSuite = report
	return turn
}

// reusing reports an earlier full run from a turn that ran no full check itself.
func reusing(turn agent.Turn, report *agent.FullSuite) agent.Turn {
	turn.Outcome.FullSuite = report
	return turn
}

func focusedOnly(turn agent.Turn) agent.Turn {
	turn.Outcome.Validation = []task.Check{{Command: "go test ./internal/app", Passed: true, Scope: task.FocusedCheck}}
	return turn
}

func readyTurn() agent.Turn { return reviewReadyTurn("sid-1", proposedBody) }

func countResumed(f fixture, text string) int {
	n := 0
	for _, r := range f.claude.resumed {
		if strings.Contains(r, text) {
			n++
		}
	}
	return n
}

func TestAReviewFixWithFocusedChecksGetsOneFinalCheckAndNoSecondReviewOfTheSameHead(t *testing.T) {
	f := newFixture()
	key := gatedLocalTask(t, f)
	f.wt.checkpointHeads = []string{"commit1", "commit2", "commit2"}
	f.claude.turns = []agent.Turn{
		withFullRun(readyTurn(), fullRun("commit1")),
		focusedOnly(readyTurn()),
		withFullRun(readyTurn(), fullRun("commit2")),
		prTurn("sid-1"),
	}
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking")}
	opensPR(t, f, "commit2", proposedTitle, proposedBody, 2)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || got.Delegation.PR != 912 || got.Delegation.PRHead != "commit2" {
		t.Fatalf("state %s delegation %+v outcome %+v", got.State, got.Delegation, got.LastOutcome)
	}
	if len(f.codex.reviewed) != 2 || countResumed(f, "## Final full check") != 1 {
		t.Fatalf("competing reviews %d, final checks %d", len(f.codex.reviewed), countResumed(f, "## Final full check"))
	}
	fix := f.claude.resumed[0]
	if !strings.Contains(fix, "Review round") || strings.Contains(fix, "has no full-suite result") ||
		!strings.Contains(fix, "Do not rerun the full suite in this turn") || strings.Contains(fix, "Run the full suite once") {
		t.Errorf("the fix turn must run focused checks only:\n%s", fix)
	}
	if !strings.Contains(f.claude.resumed[1], "covers commit1, and the change moved to commit2") {
		t.Errorf("the final check must say why the result is stale:\n%s", f.claude.resumed[1])
	}
	if r := got.FullResult; !r.Covers("commit2") || got.Delegation.FinalChecks != 0 {
		t.Errorf("full result %+v, final checks %d", r, got.Delegation.FinalChecks)
	}
}

func TestADocumentationOnlyFixWithEvidenceReusesTheFullResult(t *testing.T) {
	f := newFixture()
	key := gatedLocalTask(t, f)
	f.wt.checkpointHeads = []string{"commit1", "commit2"}
	f.wt.changed = map[string][]string{"commit1..commit2": {"docs/guide.md"}}
	f.claude.turns = []agent.Turn{
		withFullRun(readyTurn(), fullRun("commit1")),
		// The fix ran focused checks only and reuses the run on commit1.
		reusing(focusedOnly(readyTurn()), fullRun("commit1", doc("docs/guide.md", "no test, build, or CI step reads docs/ (Makefile, .github/workflows/ci.yml)"))),
		prTurn("sid-1"),
	}
	f.codex.reviews = []agent.ReviewOutcome{findings("should_fix")}
	opensPR(t, f, "commit2", proposedTitle, proposedBody, 2)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || got.Delegation.PRHead != "commit2" || countResumed(f, "## Final full check") != 0 {
		t.Fatalf("state %s head %s final checks %d", got.State, got.Delegation.PRHead, countResumed(f, "## Final full check"))
	}
	if r := got.FullResult; r == nil || r.Revision != "commit1" || !r.Covers("commit2") {
		t.Fatalf("full result = %+v", r)
	}
}

func TestAFinalCheckThatFixesAFailureSendsTheNewCommitThroughReviewAgain(t *testing.T) {
	f := newFixture()
	key := gatedLocalTask(t, f)
	f.wt.checkpointHeads = []string{"commit1", "commit2", "commit3"}
	f.claude.turns = []agent.Turn{
		withFullRun(readyTurn(), fullRun("commit1")),
		focusedOnly(readyTurn()),
		// The full suite failed on commit2; the agent fixed it in commit3 and reran it.
		withFullRun(readyTurn(), fullRun("commit3")),
		prTurn("sid-1"),
	}
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking")}
	opensPR(t, f, "commit3", proposedTitle, proposedBody, 3)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || got.Delegation.PRHead != "commit3" {
		t.Fatalf("state %s head %s outcome %+v", got.State, got.Delegation.PRHead, got.LastOutcome)
	}
	last := got.LatestCompeting()
	if len(f.codex.reviewed) != 3 || last.Head != "commit3" || !last.Clean {
		t.Fatalf("competing reviews %d, latest %+v", len(f.codex.reviewed), last)
	}
}

func TestAFinalCheckThatStillFailsIsNotHandedOff(t *testing.T) {
	f := newFixture()
	key := gatedLocalTask(t, f)
	f.wt.checkpointHeads = []string{"commit1", "commit2", "commit2", "commit2"}
	failing := withFullRun(readyTurn(), fullRun("commit2"))
	failing.Outcome.Validation = []task.Check{fullCheck(false)}
	f.claude.turns = []agent.Turn{withFullRun(readyTurn(), fullRun("commit1")), focusedOnly(readyTurn()), failing, failing}
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking")}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "full checks failed") || got.Delegation.PR != 0 || openTurns(f) != 0 {
		t.Fatalf("state %s outcome %+v pr %d", got.State, got.LastOutcome, got.Delegation.PR)
	}
}

func TestTheFirstReviewWaitsForACoveringFullResult(t *testing.T) {
	t.Run("one request supplies it", func(t *testing.T) {
		f := newFixture()
		key := gatedLocalTask(t, f)
		f.wt.checkpointHeads = []string{"commit1", "commit1"}
		f.claude.turns = []agent.Turn{focusedOnly(readyTurn()), withFullRun(readyTurn(), fullRun("commit1")), prTurn("sid-1")}
		opensPR(t, f, "commit1", proposedTitle, proposedBody, 1)
		if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
			t.Fatal(err)
		}
		got := f.store.recs[key]
		if got.State != task.WaitingForCI || len(f.codex.reviewed) != 1 || !strings.Contains(f.claude.resumed[0], "no full-suite result was reported") {
			t.Fatalf("state %s reviews %d resumed %q", got.State, len(f.codex.reviewed), f.claude.resumed)
		}
	})
	t.Run("a second miss fails the task before any review", func(t *testing.T) {
		f := newFixture()
		key := gatedLocalTask(t, f)
		f.wt.checkpointHeads = []string{"commit1", "commit1"}
		f.claude.turns = []agent.Turn{focusedOnly(readyTurn()), focusedOnly(readyTurn())}
		if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
			t.Fatal(err)
		}
		got := f.store.recs[key]
		if got.State != task.Failed || len(got.Reviews) != 0 || !strings.Contains(got.LastOutcome.Summary, "after one request") {
			t.Fatalf("state %s reviews %d outcome %+v", got.State, len(got.Reviews), got.LastOutcome)
		}
	})
}

func TestARepositoryWithoutAFullSuiteIsHandedOffWithItsExplanation(t *testing.T) {
	f := newFixture()
	key := gatedLocalTask(t, f)
	f.wt.checkpointHeads = []string{"commit1"}
	turn := focusedOnly(readyTurn())
	turn.Outcome.FullSuite = &agent.FullSuite{Status: agent.FullSuiteUnavailable, Explanation: "the repository documents no test command; ran the linter only"}
	f.claude.turns = []agent.Turn{turn, prTurn("sid-1")}
	opensPR(t, f, "commit1", proposedTitle, proposedBody, 1)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || got.FullResult == nil || got.FullResult.Unverified != "the repository documents no test command; ran the linter only" {
		t.Fatalf("state %s full result %+v", got.State, got.FullResult)
	}
	if countResumed(f, "## Final full check") != 0 || len(f.claude.resumed) != 1 {
		t.Fatalf("resumed %q", f.claude.resumed)
	}
	if !strings.Contains(f.claude.resumed[0], "No full suite was run: the repository documents no test command") {
		t.Errorf("the open turn lacks the explanation:\n%s", f.claude.resumed[0])
	}
	if !strings.Contains(f.claude.prompts[0], "## Validation") {
		t.Errorf("the implementer was not told how to report the full suite:\n%s", f.claude.prompts[0])
	}
}

func TestFinalChecksStopAfterTheBound(t *testing.T) {
	f := newFixture()
	key := gatedLocalTask(t, f)
	rec := f.store.recs[key]
	rec.Delegation.LocalHead, rec.Delegation.ProposedTitle, rec.Delegation.ProposedBody = "commit2", proposedTitle, proposedBody
	rec.Delegation.Implementer.ID = "sid-1"
	rec.Delegation.FinalChecks = maxFinalChecks
	rec.FullResult = &task.FullResult{Head: "commit1", Revision: "commit1", Covered: true}
	rec.Reviews = []task.Review{{Round: 1, Reviewer: task.Codex, Head: "commit2", Proposal: task.ProposalDigest(proposedTitle, proposedBody), Clean: true, At: now}}
	rec.Reviews[0].Scope, rec.Reviews[0].Plan = rec.Delegation.ScopeDigest, rec.Delegation.PlanDigest
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "3 final checks") || len(f.claude.resumed) != 0 || openTurns(f) != 0 {
		t.Fatalf("state %s outcome %+v resumed %d", got.State, got.LastOutcome, len(f.claude.resumed))
	}
}

func TestAPRReviewFixGetsAFinalCheckBeforeWaitingForCI(t *testing.T) {
	f := newFixture()
	f.app.FullSuiteGate = true
	key := started(t, f, startReq(t))
	full := withFullRun(prTurn("sid-1"), fullRun("c0ffee1"))
	fix := focusedOnly(prTurn("sid-1"))
	final := withFullRun(prTurn("sid-1"), fullRun("c0ffee2"))
	f.claude.turns = []agent.Turn{full, fix, final}
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking")}
	resumes := 0
	f.claude.onResume = func() {
		resumes++
		if resumes == 1 {
			pr := f.gh.prs[912]
			pr.HeadSHA = "c0ffee2"
			f.gh.prs[912] = pr
		}
	}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || !got.FullResult.Covers("c0ffee2") || len(f.codex.reviewed) != 2 || countResumed(f, "## Final full check") != 1 {
		t.Fatalf("state %s full %+v reviews %d resumed %q", got.State, got.FullResult, len(f.codex.reviewed), f.claude.resumed)
	}
	if !strings.Contains(f.claude.resumed[1], `End with "pr_opened" for PR #912`) {
		t.Errorf("final check prompt:\n%s", f.claude.resumed[1])
	}
}

func TestTheGateRefusesReadinessWithoutACoveringFullResult(t *testing.T) {
	f := newFixture()
	f.app.FullSuiteGate = true
	key := started(t, f, startReq(t))
	f.claude.turn = withFullRun(prTurn("sid-1"), fullRun("c0ffee1"))
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	rec := f.store.recs[key]
	if rec.State != task.WaitingForCI {
		t.Fatalf("setup: state %s", rec.State)
	}
	// A runner that stopped after the clean review left a result for an earlier head.
	rec.FullResult.Head = "c0ffee0"
	setCI(f, task.CIStatus{State: task.CIPassing})
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Implementing || got.NextStage() != task.Implementing || got.LastOutcome.Status != "final_check" {
		t.Fatalf("state %s next %s outcome %+v", got.State, got.NextStage(), got.LastOutcome)
	}
	if got.Delegation.Launch == nil {
		t.Fatal("the gate did not start the runner for the final check")
	}
}

func TestAReopenedPRWithAPendingFinalCheckIsNotHandedOff(t *testing.T) {
	f := newFixture()
	f.app.FullSuiteGate = true
	key := started(t, f, startReq(t))
	f.claude.turn = withFullRun(prTurn("sid-1"), fullRun("c0ffee1"))
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	// The final check was pending when the developer closed the PR.
	f.store.recs[key].FullResult.Head = "c0ffee0"
	setPR(f, "closed")
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key].State; got != task.PRClosed {
		t.Fatalf("setup: state %s", got)
	}
	setPR(f, "open")
	setCI(f, task.CIStatus{State: task.CIPassing})
	spawned := len(f.runners.spawned)
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Implementing || !got.FinalCheckPending() || !strings.Contains(got.LastOutcome.Summary, "needs a final full check") {
		t.Fatalf("state %s outcome %+v", got.State, got.LastOutcome)
	}
	if len(f.runners.spawned) != spawned+1 {
		t.Fatalf("runners spawned %d, want the final check started", len(f.runners.spawned)-spawned)
	}
}

// An answer turn that pushes nothing is handed over only when the full result it leaves
// covers the head. Its own report is judged first, so a failed run blocks the handoff.
func TestAnAnswerTurnWithAFailedFullRunGoesToTheFinalCheck(t *testing.T) {
	f := newFixture()
	key := waiting(t, f)
	rec := f.store.recs[key]
	rec.Delegation.FullSuiteGate = true
	rec.FullResult = &task.FullResult{Head: "c0ffee1", Revision: "c0ffee1", Covered: true}
	answer := withFullRun(prTurn("sid-1"), fullRun("c0ffee1"))
	answer.Outcome.Validation = []task.Check{fullCheck(false)}
	f.claude.turns = []agent.Turn{answer, withFullRun(prTurn("sid-1"), fullRun("c0ffee1"))}
	got := resumeRun(t, f, key)
	if countResumed(f, "## Final full check") != 1 || !strings.Contains(f.claude.resumed[1], "full checks failed") {
		t.Fatalf("resumed %q", f.claude.resumed)
	}
	if got.State != task.WaitingForCI && got.State != task.ReadyForHuman || !got.FullResult.Covers("c0ffee1") {
		t.Fatalf("state %s full %+v", got.State, got.FullResult)
	}
}

func TestHandsOverNowWaitsForAPendingFinalCheck(t *testing.T) {
	rec := &task.Record{Delegation: &task.Delegation{FullSuiteGate: true, PR: 912, PRHead: "c0ffee1"},
		Reviews: []task.Review{{Round: 1, Reviewer: task.Codex, PR: 912, Head: "c0ffee1", Clean: true}}}
	if handsOverNow(rec, &task.CIStatus{State: task.CIPassing}) {
		t.Fatal("a change with no covering full result was handed over")
	}
	rec.FullResult = &task.FullResult{Head: "c0ffee1", Revision: "c0ffee1", Covered: true}
	if !handsOverNow(rec, &task.CIStatus{State: task.CIPassing}) {
		t.Fatal("a covered change was held back")
	}
}

func TestAFinalCheckReusesAnEarlierRunForDocumentationOnlyPathsWithoutRerunningIt(t *testing.T) {
	f := newFixture()
	key := gatedLocalTask(t, f)
	f.wt.checkpointHeads = []string{"commit1", "commit2", "commit2"}
	f.wt.changed = map[string][]string{"commit1..commit2": {"docs/guide.md"}}
	f.claude.turns = []agent.Turn{
		withFullRun(readyTurn(), fullRun("commit1")),
		focusedOnly(readyTurn()),
		reusing(focusedOnly(readyTurn()), fullRun("commit1", doc("docs/guide.md", "no test, build, or CI step reads docs/"))),
		prTurn("sid-1"),
	}
	f.codex.reviews = []agent.ReviewOutcome{findings("should_fix")}
	opensPR(t, f, "commit2", proposedTitle, proposedBody, 2)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || got.Delegation.PRHead != "commit2" || countResumed(f, "## Final full check") != 1 {
		t.Fatalf("state %s head %s outcome %+v final checks %d", got.State, got.Delegation.PRHead, got.LastOutcome, countResumed(f, "## Final full check"))
	}
	if r := got.FullResult; !r.Covers("commit2") || len(r.Checks) != 1 || r.Checks[0].Scope != task.FullCheck {
		t.Fatalf("full result = %+v", r)
	}
}
