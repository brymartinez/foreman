package app

import (
	"context"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func TestStartCapturesReviewBeforePR(t *testing.T) {
	for _, on := range []bool{false, true} {
		f := newFixture()
		f.app.ReviewBeforePR = on
		rec, err := f.app.Start(context.Background(), startReq(t))
		if err != nil {
			t.Fatal(err)
		}
		if rec.Delegation.ReviewBeforePR != on {
			t.Errorf("config %t: task captured %t", on, rec.Delegation.ReviewBeforePR)
		}
	}
}

func TestRestartCapturesTheCurrentReviewBeforePR(t *testing.T) {
	for _, on := range []bool{false, true} {
		f := newFixture()
		f.app.ReviewBeforePR = on
		key := task.Key{Repo: "ridezoomo/armada-backend", Number: 825}
		f.store.recs[key] = &task.Record{Key: key, Kind: task.Issue, State: task.Failed,
			Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Branch: "feat/825-provider-retries", ReviewBeforePR: !on}}
		rec, err := f.app.Start(context.Background(), startReq(t))
		if err != nil {
			t.Fatal(err)
		}
		if rec.Delegation.ReviewBeforePR != on {
			t.Errorf("config %t: restarted task captured %t", on, rec.Delegation.ReviewBeforePR)
		}
	}
}

func TestRestartKeepsTheLocalCommitAndProposalAtTheSameHead(t *testing.T) {
	f := newFixture()
	f.app.ReviewBeforePR = true
	head, _ := f.wt.Head(context.Background(), "")
	key := task.Key{Repo: "ridezoomo/armada-backend", Number: 825}
	f.store.recs[key] = &task.Record{Key: key, Kind: task.Issue, State: task.Failed,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Branch: "feat/825-provider-retries",
			ReviewBeforePR: true, LocalHead: head, ProposedTitle: "feat: retries", ProposedBody: "Closes #825"},
		Reviews: []task.Review{{Head: head, Clean: true}}}
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	d := rec.Delegation
	if d.LocalHead != head || d.ProposedTitle != "feat: retries" || d.ProposedBody != "Closes #825" || !rec.OpenPending() {
		t.Fatalf("delegation = %+v; the restart lost the reviewed commit", d)
	}
}

func TestRestartDropsTheLocalCommitWhenTheHeadMoved(t *testing.T) {
	f := newFixture()
	f.app.ReviewBeforePR = true
	key := task.Key{Repo: "ridezoomo/armada-backend", Number: 825}
	f.store.recs[key] = &task.Record{Key: key, Kind: task.Issue, State: task.Failed,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Branch: "feat/825-provider-retries",
			ReviewBeforePR: true, LocalHead: "0ld0001", ProposedTitle: "feat: retries", ProposedBody: "Closes #825"}}
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if d := rec.Delegation; d.LocalHead != "" || d.ProposedBody != "" {
		t.Fatalf("delegation = %+v; a commit that is not the head was kept", d)
	}
}
