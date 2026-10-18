package app

import (
	"context"
	"io"
	"slices"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// recordStages collects each state the task file is written with, in order, with
// repeats collapsed.
func recordStages(f fixture) *[]task.State {
	seen := &[]task.State{}
	f.store.failUpdate = func(next *task.Record) bool {
		if n := len(*seen); n == 0 || (*seen)[n-1] != next.State {
			*seen = append(*seen, next.State)
		}
		return false
	}
	return seen
}

func TestAPRRunEntersEachStageBeforeItsTurn(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	if got := f.store.recs[key].State; got != task.Planning {
		t.Fatalf("a new start is %s, want PLANNING", got)
	}
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2"), prTurn("sid-2")}
	f.claude.reviews = []agent.ReviewOutcome{selfFindings(), {Clean: true}}
	// Each turn sees the stage it runs in already saved.
	var during []task.State
	f.claude.during = func() { during = append(during, f.store.recs[key].State) }
	f.claude.onResume = f.claude.during
	seen := recordStages(f)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := []task.State{task.Planning, task.Implementing, task.SelfReviewing, task.Implementing, task.SelfReviewing, task.AdversarialReviewing, task.WaitingForCI}
	if !slices.Equal(*seen, want) {
		t.Fatalf("stages = %v\nwant %v", *seen, want)
	}
	if !slices.Equal(during, []task.State{task.Planning, task.Implementing, task.Implementing}) {
		t.Errorf("implementer turns ran in %v, want planning then two implementing turns", during)
	}
	setCI(f, task.CIStatus{State: task.CIPassing})
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key].State; got != task.ReadyForHuman {
		t.Errorf("after CI: %s", got)
	}
}

func TestALocalReviewRunEntersEachStageAndOpensThePRLast(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	rec := f.store.recs[key]
	rec.State = rec.NextStage()
	updated := "Retry 5xx only, never 4xx.\n\nCloses #825"
	f.wt.checkpointHeads = []string{"commit1", "commit2"}
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking")}
	f.claude.turns = []agent.Turn{reviewReadyTurn("sid-1", proposedBody), reviewReadyTurn("sid-1", updated), prTurn("sid-1")}
	opensPR(t, f, "commit2", proposedTitle, updated, 2)
	seen := recordStages(f)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := []task.State{task.Implementing, task.SelfReviewing, task.AdversarialReviewing, task.Implementing,
		task.SelfReviewing, task.AdversarialReviewing, task.OpeningPR, task.WaitingForCI}
	if !slices.Equal(*seen, want) {
		t.Fatalf("stages = %v\nwant %v", *seen, want)
	}
}

func TestARestartPicksUpAtTheStageTheRecordStillNeeds(t *testing.T) {
	f := newFixture()
	f.app.ReviewBeforePR = true
	head, _ := f.wt.Head(context.Background(), "")
	key := task.Key{Repo: "testorg/example", Number: 825}
	digest := task.ProposalDigest(proposedTitle, proposedBody)
	rec := &task.Record{Key: key, Kind: task.Issue, State: task.Failed,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude, ID: "sid-1"}, Branch: "feat/825-provider-retries",
			ReviewBeforePR: true, LocalHead: head, ProposedTitle: proposedTitle, ProposedBody: proposedBody},
		Reviews: []task.Review{{Round: 1, Kind: task.SelfReview, Reviewer: task.Claude, Head: head, Proposal: digest, Clean: true, At: now}}}
	postedPlan(rec)
	f.store.recs[key] = rec
	delete(f.gh.prs, 912)
	if got := started(t, f, startReq(t)); f.store.recs[got].State != task.AdversarialReviewing {
		t.Fatalf("the restart is %s, want ADVERSARIAL_REVIEW after a clean self-review", f.store.recs[got].State)
	}
	f.claude.turns = []agent.Turn{prTurn("sid-1")}
	// The restart starts a fresh session, whose turn opens the PR.
	f.claude.during = func() {
		pr := fixturePR()
		pr.HeadSHA, pr.Title, pr.Body = head, proposedTitle, proposedBody
		f.gh.prs[912] = pr
	}
	seen := recordStages(f)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := []task.State{task.AdversarialReviewing, task.OpeningPR, task.WaitingForCI}
	if !slices.Equal(*seen, want) || len(f.claude.reviewed) != 0 || len(f.codex.reviewed) != 1 {
		t.Fatalf("stages = %v, self-reviews %d, competing reviews %d; want %v with only the competing review", *seen, len(f.claude.reviewed), len(f.codex.reviewed), want)
	}
}
