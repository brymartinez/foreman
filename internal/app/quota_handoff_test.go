package app

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

func exhaustedClaude() fakeQuota {
	return fakeQuota{q: map[task.Agent]Quota{task.Claude: {Status: QuotaExhausted}, task.Codex: {Status: QuotaAvailable, Percent: 16}}}
}

func TestQuotaPreflightPreventsEveryRunnerCall(t *testing.T) {
	for _, method := range []string{"start", "resume", "review"} {
		t.Run(method, func(t *testing.T) {
			f := newFixture()
			key := started(t, f, startReq(t))
			f.app.Quota = exhaustedClaude()
			a := f.app.meterAgents(key)
			adapter := a.Agents[task.Claude]
			in := agent.StartInput{Step: agent.StepReview, Log: io.Discard}
			var err error
			switch method {
			case "start":
				_, err = adapter.Start(context.Background(), in, nil)
			case "resume":
				_, err = adapter.Resume(context.Background(), task.Session{Agent: task.Claude, ID: "old"}, in)
			case "review":
				_, err = adapter.Review(context.Background(), in)
			}
			if err == nil || !strings.Contains(err.Error(), "quota exhausted") || f.claude.starts != 0 || len(f.claude.resumed) != 0 || len(f.claude.reviewed) != 0 || len(f.store.recs[key].Calls) != 0 {
				t.Fatalf("quota preflight failed: err=%v starts=%d resumes=%d reviews=%d calls=%d", err, f.claude.starts, len(f.claude.resumed), len(f.claude.reviewed), len(f.store.recs[key].Calls))
			}
		})
	}
}

func TestQuotaRestartHandsLocalReviewToCodexWithoutReimplementing(t *testing.T) {
	f := newFixture()
	req := startReq(t)
	f.wt.head = "c0ffee1"
	key := started(t, f, req)
	rec := f.store.recs[key]
	postedPlan(f, rec)
	rec.State = task.Failed
	d := rec.Delegation
	d.ReviewBeforePR = true
	d.LocalHead, d.CheckpointHead = f.wt.head, f.wt.head
	d.ProposedTitle, d.ProposedBody = proposedTitle, proposedBody
	d.Implementer.ID = "claude-session"
	f.app.Quota = exhaustedClaude()
	f.codex.turn = prTurn("codex-new")
	f.codex.turn.Session.Agent = task.Codex
	pr := f.gh.prs[912]
	pr.Title, pr.Body = proposedTitle, proposedBody
	f.gh.prs[912] = pr
	got, err := f.app.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Delegation.Implementer.Agent != task.Codex || got.Delegation.Implementer.ID != "" || got.Delegation.LocalHead != f.wt.head || got.Delegation.ProposedBody != proposedBody || got.State != task.SelfReviewing {
		t.Fatalf("handoff lost work: %+v state=%s", got.Delegation, got.State)
	}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got = f.store.recs[key]
	if got.State != task.WaitingForCI || len(f.claude.reviewed) != 0 || f.claude.starts != 0 || len(f.codex.reviewed) != 1 || got.LatestCompeting() == nil || !got.LatestCompeting().Skipped || got.LatestCompeting().Clean {
		t.Fatalf("exhausted reviewer was called or skip mislabeled: state=%s reviews=%+v", got.State, got.Reviews)
	}
}

func TestSkippedAdversarialReviewStillRequiresOneFinalFullCheck(t *testing.T) {
	f := newFixture()
	key := gatedLocalTask(t, f)
	f.app.Quota = fakeQuota{q: map[task.Agent]Quota{task.Claude: {Status: QuotaAvailable}, task.Codex: {Status: QuotaExhausted}}}
	f.wt.checkpointHeads = []string{"commit1", "commit1"}
	rec := f.store.recs[key]
	rec.Delegation.LocalHead = "commit1"
	rec.Delegation.Implementer.ID = "sid-1"
	rec.Delegation.ProposedTitle, rec.Delegation.ProposedBody = proposedTitle, proposedBody
	rec.State = task.SelfReviewing
	f.claude.turns = []agent.Turn{withFullRun(readyTurn(), fullRun("commit1")), prTurn("sid-1")}
	opensPR(t, f, "commit1", proposedTitle, proposedBody, 0)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || got.LatestCompeting() == nil || !got.LatestCompeting().Skipped || got.LatestCompeting().Clean || !got.FullResult.Covers("commit1") || len(f.codex.reviewed) != 0 || countResumed(f, "## Final full check") != 1 {
		t.Fatalf("quota skip bypassed the final gate: state=%s outcome=%+v reviews=%+v full=%+v", got.State, got.LastOutcome, got.Reviews, got.FullResult)
	}
}

func TestUnknownQuotaDoesNotSwitchTheImplementerOrSkipReview(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.app.Quota = fakeQuota{q: map[task.Agent]Quota{}}
	f.claude.turn = prTurn("sid")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || got.Delegation.Implementer.Agent != task.Claude || got.LatestCompeting().Skipped || len(f.codex.reviewed) != 1 {
		t.Fatalf("unknown quota changed workflow: %+v", got)
	}
}

func TestQuotaExhaustionBetweenStagesHandsOffBeforeTheNextCall(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	rec := f.store.recs[key]
	rec.Delegation.LocalHead = "c0ffee1"
	rec.Delegation.ProposedTitle, rec.Delegation.ProposedBody = proposedTitle, proposedBody
	rec.State = task.SelfReviewing
	q := map[task.Agent]Quota{task.Claude: {Status: QuotaAvailable}, task.Codex: {Status: QuotaAvailable}}
	f.app.Quota = fakeQuota{q: q}
	f.claude.onReview = func() { q[task.Claude] = Quota{Status: QuotaExhausted} }
	f.codex.turn = prTurn("new-codex-session")
	f.codex.turn.Session.Agent = task.Codex
	pr := fixturePR()
	pr.Title, pr.Body = proposedTitle, proposedBody
	f.codex.during = func() { f.gh.prs[912] = pr }
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || got.Delegation.Implementer.Agent != task.Codex || !got.LatestCompeting().Clean || len(f.claude.reviewed) != 1 || len(f.codex.reviewed) != 1 || f.claude.starts != 0 || f.codex.starts != 1 {
		t.Fatalf("quota change between stages lost the handoff: state=%s outcome=%+v delegation=%+v", got.State, got.LastOutcome, got.Delegation)
	}
}

func TestBothProvidersExhaustedCanSkipTheOnlyRemainingAdversarialReview(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	rec := f.store.recs[key]
	postedPlan(f, rec)
	rec.Delegation.PR, rec.Delegation.PRHead = 912, "c0ffee1"
	rec.Reviews = []task.Review{{Kind: task.SelfReview, Reviewer: task.Claude, Head: "c0ffee1", PR: 912, Clean: true}}
	rec.State = task.Failed
	rec.Delegation.RunnerPID = 0
	rec.Delegation.Launch = nil
	f.app.Quota = fakeQuota{q: map[task.Agent]Quota{task.Claude: {Status: QuotaExhausted}, task.Codex: {Status: QuotaExhausted}}}
	if _, err := f.app.Start(context.Background(), startReq(t)); err != nil {
		t.Fatal(err)
	}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || !got.LatestCompeting().Skipped || len(f.claude.reviewed) != 0 || len(f.codex.reviewed) != 0 {
		t.Fatalf("unused implementation quota blocked the skip: state=%s outcome=%+v", got.State, got.LastOutcome)
	}
}
