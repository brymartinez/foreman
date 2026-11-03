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
	if !strings.Contains(f.codex.reviewed[0], "caller already guards") {
		t.Fatal("adversarial reviewer did not receive rejection evidence")
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
		{"stale head", task.SelfReview, 1, 1, "other"},
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

func TestAdversarialReviewReceivesAnsweredDecisionsAndPlan(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	rec := f.store.recs[key]
	postedPlan(f, rec)
	rec.Decisions = []task.Decision{{Question: task.Question{Issue: "Hide scoped frequency?"}, Answer: "Hide it.", AnsweredAt: now, DeliveredAt: now}}
	f.claude.turn = prTurn("sid")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.claude.reviewed[0], "Hide it.") || strings.Contains(f.claude.reviewed[0], "Posted plan.") {
		t.Fatal("context leaked to self-review")
	}
	for _, want := range []string{"Hide it.", "Posted plan.", "Later developer decisions override"} {
		if !strings.Contains(f.codex.reviewed[0], want) {
			t.Fatalf("adversarial prompt missing %q", want)
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

func TestLostCorrectionSessionRebuildsPlanAndPriorDecisions(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	rec := f.store.recs[key]
	postedPlan(f, rec)
	rec.Decisions = []task.Decision{{Question: task.Question{Issue: "Hide it?"}, Answer: "Hide it.", AnsweredAt: now, DeliveredAt: now}}
	f.claude.turns = []agent.Turn{prTurn("sid"), prTurn("new-session")}
	f.claude.reviews = []agent.ReviewOutcome{selfFindings()}
	f.claude.resumeErr = agent.ErrNoSession
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.store.recs[key].State != task.WaitingForCI || f.claude.starts != 2 {
		t.Fatalf("state %s starts %d", f.store.recs[key].State, f.claude.starts)
	}
	for _, want := range []string{"Posted plan.", "Hide it.", "no test covers the 4xx path", "reviewAssessment"} {
		if !strings.Contains(f.claude.prompts[1], want) {
			t.Errorf("replacement session lacks %q", want)
		}
	}
}

func TestLostSelfReviewLimitSessionKeepsTheAssessmentOnlyInstruction(t *testing.T) {
	f := newFixture()
	f.app.FixRounds = 1
	key := started(t, f, startReq(t))
	rec := f.store.recs[key]
	postedPlan(f, rec)
	rec.Delegation.Implementer.ID = "lost-session"
	rec.Delegation.PR = 912
	rec.Delegation.PRHead = "c0ffee1"
	for i := 1; i <= 2; i++ {
		rec.Reviews = append(rec.Reviews, task.Review{Round: i, Kind: task.SelfReview, PR: 912, Head: "c0ffee1", Findings: selfFindings().Findings, At: now})
	}
	f.claude.resumeErr = agent.ErrNoSession
	f.claude.turn = agent.Turn{Session: task.Session{Agent: task.Claude, ID: "replacement"}, Outcome: agent.Outcome{Status: agent.Blocked, Summary: "Need your decision"}}
	if _, err := f.app.implement(context.Background(), rec, turnRequest{findings: rec.LatestReview()}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.claude.starts != 1 || len(f.claude.resumed) != 1 {
		t.Fatalf("starts %d resumes %d", f.claude.starts, len(f.claude.resumed))
	}
	for _, want := range []string{"self-review fix limit is reached", "without changing code", "Posted plan."} {
		if !strings.Contains(f.claude.prompt, want) {
			t.Errorf("replacement session lost %q", want)
		}
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
	if res.err == nil || !strings.Contains(res.err.Error(), "current self-review round") {
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
