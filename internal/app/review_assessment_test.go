package app

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

func TestImplementerCanRejectSelfReviewWithoutAnotherReview(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	postedPlan(f, f.store.recs[key])
	next := prTurn("implementation-session")
	next.Outcome.ReviewAssessment = &task.ReviewAssessment{Round: 1, Rejected: []task.RejectedFinding{{Finding: 1, Reason: "The caller already guards this input in retry.go:20."}}}
	f.claude.turns = []agent.Turn{prTurn("implementation-session"), next}
	f.claude.reviews = []agent.ReviewOutcome{selfFindings()}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || kinds(got) != "self! competing" || f.claude.starts != 1 || len(f.claude.resumed) != 1 {
		t.Fatalf("state %s reviews %s starts %d resumes %d", got.State, kinds(got), f.claude.starts, len(f.claude.resumed))
	}
	if got.Reviews[0].Clean || !got.Reviews[0].AllFindingsRejected() {
		t.Fatal("assessment lost or reviewer verdict overwritten")
	}
	if strings.Contains(f.codex.reviewed[0], "caller already guards") {
		t.Fatal("competing review received a prior assessment")
	}
}

func TestAssessmentCannotWaiveAnotherRoundOrAdversarialReview(t *testing.T) {
	for _, tc := range []struct {
		name           string
		kind           task.ReviewKind
		round, finding int
		head           string
	}{
		{"stale round", task.SelfReview, 2, 1, "c0ffee1"},
		{"unknown finding", task.SelfReview, 1, 2, "c0ffee1"},
		{"adversarial", task.CompetingReview, 1, 1, "c0ffee1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()
			key := started(t, f, startReq(t))
			rec := f.store.recs[key]
			rec.Delegation.PR = 912
			rec.Delegation.PRHead = "c0ffee1"
			rec.Reviews = []task.Review{{Round: 1, Kind: tc.kind, Head: tc.head, Findings: []task.Finding{{Summary: "defect"}}}}
			res := turnResult{turn: prTurn("sid"), pr: &PullRequest{HeadSHA: "c0ffee1"}}
			res.turn.Outcome.ReviewAssessment = &task.ReviewAssessment{Round: tc.round, Rejected: []task.RejectedFinding{{Finding: tc.finding, Reason: "evidence"}}}
			if err := validateReviewAssessment(rec, res); err == nil {
				t.Fatal("invalid assessment accepted")
			}
		})
	}
}

func TestPartialRejectionOrChangedCommitStillNeedsSelfReview(t *testing.T) {
	for _, changed := range []bool{false, true} {
		rec := &task.Record{Delegation: &task.Delegation{PR: 7, PRHead: "h1"}, Reviews: []task.Review{{Round: 1, Kind: task.SelfReview, PR: 7, Head: "h1", Findings: []task.Finding{{Summary: "one"}, {Summary: "two"}}, RejectedFindings: []task.RejectedFinding{{Finding: 1, Reason: "guard exists"}}}}}
		if changed {
			rec.Reviews[0].RejectedFindings = append(rec.Reviews[0].RejectedFindings, task.RejectedFinding{Finding: 2, Reason: "approved decision"})
			rec.Delegation.PRHead = "h2"
		}
		if rec.SelfReviewPassed() {
			t.Fatal("partial rejection or changed head bypassed review")
		}
	}
}

func TestBothCodeReviewsOmitAnsweredDecisionsAndPlan(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	rec := f.store.recs[key]
	postedPlan(f, rec)
	rec.Decisions = []task.Decision{{Question: task.Question{Issue: "Hide scoped frequency?"}, Answer: "Hide it.", AnsweredAt: now, DeliveredAt: now}}
	f.claude.turn = prTurn("sid")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{f.claude.reviewed[0], f.codex.reviewed[0]} {
		if strings.Contains(p, "Hide it.") || strings.Contains(p, "Posted plan.") {
			t.Fatal("task context leaked into code review")
		}
	}
}

func TestLocalImplementerCanRejectSelfReviewAndOpenReviewedCommit(t *testing.T) {
	f := newFixture()
	key := gatedLocalTask(t, f)
	first := withFullRun(readyTurn(), fullRun("commit1"))
	next := first
	next.Outcome.ReviewAssessment = &task.ReviewAssessment{Round: 1, Rejected: []task.RejectedFinding{{Finding: 1, Reason: "The approved behavior requires this guard."}}}
	f.wt.checkpointHeads = []string{"commit1", "commit1"}
	f.claude.turns = []agent.Turn{first, next, prTurn("sid")}
	f.claude.reviews = []agent.ReviewOutcome{selfFindings()}
	opensPR(t, f, "commit1", proposedTitle, proposedBody, 1)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || kinds(got) != "self! competing" || got.Delegation.PR != 912 {
		t.Fatalf("state %s reviews %s outcome %+v", got.State, kinds(got), got.LastOutcome)
	}
}

func TestFullCheckRepairCannotIntroduceAStaleReviewAssessment(t *testing.T) {
	f := newFixture()
	key := gatedLocalTask(t, f)
	rec := f.store.recs[key]
	rec.Delegation.LocalHead = "commit1"
	rec.Delegation.Implementer.ID = "sid"
	rec.Reviews = []task.Review{{Round: 1, Kind: task.SelfReview, Head: "commit1", Findings: selfFindings().Findings}}
	first := focusedOnly(readyTurn())
	second := withFullRun(readyTurn(), fullRun("commit1"))
	second.Outcome.ReviewAssessment = &task.ReviewAssessment{Round: 99, Rejected: []task.RejectedFinding{{Finding: 1, Reason: "already guarded"}}}
	f.claude.turns = []agent.Turn{first, second}
	f.wt.checkpointHeads = []string{"commit1", "commit1"}
	res := f.app.runTurn(context.Background(), rec, turnRequest{finalCheck: &agent.FinalCheck{Head: "commit1", Local: true}}, f.app.verifiedPR(io.Discard), io.Discard)
	if res.err == nil || !strings.Contains(res.err.Error(), "latest self-review round") {
		t.Fatalf("stale assessment from repair was accepted: %v", res.err)
	}
}

func TestAllFindingsFixedStillGetsAFreshSelfReview(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	fixed := reviewReadyTurn("sid", proposedBody)
	fixed.Outcome.ReviewAssessment = &task.ReviewAssessment{Round: 1, Rejected: []task.RejectedFinding{}}
	f.claude.turns = []agent.Turn{reviewReadyTurn("sid", proposedBody), fixed, prTurn("sid")}
	f.claude.reviews = []agent.ReviewOutcome{selfFindings()}
	f.wt.checkpointHeads = []string{"commit1", "commit2"}
	opensPR(t, f, "commit2", proposedTitle, proposedBody, 1)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || kinds(got) != "self! self competing" || got.Delegation.PRHead != "commit2" {
		t.Fatalf("state %s reviews %s head %s", got.State, kinds(got), got.Delegation.PRHead)
	}
	if got.Reviews[0].AllFindingsRejected() {
		t.Fatal("all-fixed assessment waived self-review")
	}
}

func TestRetryAfterRejectedOutputReviewsCommittedCorrectionWithoutAnotherCommit(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	rec := f.store.recs[key]
	rec.State = task.Failed
	rec.Delegation.LocalHead = "commit1"
	rec.Delegation.ProposedTitle = proposedTitle
	rec.Delegation.ProposedBody = proposedBody
	rec.Reviews = []task.Review{{Round: 1, Kind: task.SelfReview, Head: "commit1", Findings: selfFindings().Findings, At: now}}
	// The correction committed commit2 before Foreman rejected its output.
	f.wt.head = "commit2"
	f.wt.reviewBase = "basehead"
	f.wt.contains = func(_ string, ancestor, descendant string) (bool, error) {
		return ancestor == descendant || ancestor == "basehead", nil
	}
	if _, err := f.app.Start(context.Background(), startReq(t)); err != nil {
		t.Fatal(err)
	}
	retry := reviewReadyTurn("retry-session", proposedBody)
	retry.Outcome.ReviewAssessment = &task.ReviewAssessment{Round: 1, Rejected: []task.RejectedFinding{{Finding: 1, Reason: "Already fixed in commit2 before the failed output."}}}
	f.claude.turns = []agent.Turn{retry, prTurn("retry-session")}
	f.wt.checkpointHeads = []string{"commit2"}
	opensPR(t, f, "commit2", proposedTitle, proposedBody, 1)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || got.Delegation.PRHead != "commit2" || kinds(got) != "self! self competing" {
		t.Fatalf("state %s head %s reviews %s outcome %+v", got.State, got.Delegation.PRHead, kinds(got), got.LastOutcome)
	}
	if got.Reviews[0].Head != "commit1" || got.LatestSelf().Head != "commit2" {
		t.Fatal("the old finding assessment waived review of the new commit")
	}
}
