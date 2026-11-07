package app

import (
	"context"
	"io"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func TestLegacyClaimFlagDoesNotGateImplementationOrHandoff(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	rec := f.store.recs[key]
	postedPlan(f, rec)
	rec.Delegation.ClaimVerification = true
	rec.Delegation.PRDDigest = "historical-prd-digest"
	f.claude.turn = prTurn("sid-1")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || len(f.claude.resumed) != 0 {
		t.Fatalf("state = %s, resumed = %q, outcome = %+v", got.State, f.claude.resumed, got.LastOutcome)
	}
	setCI(f, task.CIStatus{State: task.CIPassing})
	refresh(t, f)
	got = f.store.recs[key]
	if got.State != task.ReadyForHuman || !got.Delegation.ClaimVerification || got.Delegation.PRDDigest != "historical-prd-digest" {
		t.Fatalf("state = %s, delegation = %+v", got.State, got.Delegation)
	}
}

func TestHandoffRechecksEditedWorkPlanBeforeCI(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reopen bool
	}{
		{name: "waiting"},
		{name: "reopened", reopen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()
			key := reviewed(t, f)
			if tc.reopen {
				setPR(f, "closed")
				refresh(t, f)
				setPR(f, "open")
			}
			rec := f.store.recs[key]
			rec.Delegation.ClaimVerification = true
			id := rec.PlanComment(task.ImplementationPlan).CommentID
			comment := f.gh.comments[id]
			comment.Body += "\nAdd a test for retry exhaustion."
			f.gh.comments[id] = comment
			f.gh.ci = nil
			refresh(t, f)
			got := f.store.recs[key]
			if got.State != task.Implementing || got.Delegation.PlanDigest != "" || len(f.runners.spawned) != 2 {
				t.Fatalf("state = %s, plan digest = %q, launches = %d", got.State, got.Delegation.PlanDigest, len(f.runners.spawned))
			}
		})
	}
}
