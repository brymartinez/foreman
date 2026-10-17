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

func reviewReadyTurn(id, body string) agent.Turn {
	return agent.Turn{Session: task.Session{Agent: task.Claude, ID: id}, Outcome: agent.Outcome{
		Status: agent.ReviewReady, Summary: "committed", PRTitle: "feat: retry 5xx", PRBody: body,
		Validation: []task.Check{{Command: "go test ./...", Passed: true}},
	}}
}

// localTask starts a task with review_before_pr on and its plan posted.
func localTask(t *testing.T, f fixture) task.Key {
	t.Helper()
	f.app.ReviewBeforePR = true
	key := started(t, f, startReq(t))
	postedPlan(f.store.recs[key])
	return key
}

func TestReviewReadyTurnRecordsTheCommitAndItsProposalTogether(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	f.wt.checkpointHeads = []string{"commit1"}
	f.claude.turn = reviewReadyTurn("sid-1", "Retry 5xx only.\n\nCloses #825")
	rec := f.store.recs[key]
	res := f.app.runTurn(context.Background(), rec, turnRequest{}, f.app.verifiedPR(io.Discard), io.Discard)
	if res.err != nil {
		t.Fatal(res.err)
	}
	applyTurnResult(rec, res, now)
	d := rec.Delegation
	if rec.State != task.Working || d.LocalHead != "commit1" || d.ProposedTitle != "feat: retry 5xx" || d.ProposedBody != "Retry 5xx only.\n\nCloses #825" || d.PR != 0 {
		t.Fatalf("state %s delegation %+v", rec.State, d)
	}
	if len(rec.Validation) != 1 || !rec.ReviewPending() {
		t.Fatalf("validation %v pending %t", rec.Validation, rec.ReviewPending())
	}
	p := f.claude.prompts[0]
	if !strings.Contains(p, `"review_ready"`) || strings.Contains(p, "gh pr create") {
		t.Fatalf("the implementer was told to open the PR:\n%s", p)
	}
}

func TestReviewReadyFailsWithoutAVerifiedCommit(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	f.wt.checkpointErr = errors.New("checkpoint has uncommitted or untracked files")
	f.claude.turn = reviewReadyTurn("sid-1", "Closes #825")
	rec := f.store.recs[key]
	res := f.app.runTurn(context.Background(), rec, turnRequest{}, f.app.verifiedPR(io.Discard), io.Discard)
	if res.err == nil || !strings.Contains(res.err.Error(), "uncommitted") {
		t.Fatalf("err = %v", res.err)
	}
	applyTurnResult(rec, res, now)
	if rec.State != task.Failed || rec.Delegation.LocalHead != "" {
		t.Fatalf("state %s local head %q", rec.State, rec.Delegation.LocalHead)
	}
}

func TestReviewReadyIsRejectedOutsideALocalReview(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	postedPlan(f.store.recs[key])
	f.wt.checkpointHeads = []string{"commit1"}
	f.claude.turn = reviewReadyTurn("sid-1", "Closes #825")
	res := f.app.runTurn(context.Background(), f.store.recs[key], turnRequest{}, f.app.verifiedPR(io.Discard), io.Discard)
	if res.err == nil || !strings.Contains(res.err.Error(), "review_ready") {
		t.Fatalf("err = %v", res.err)
	}
}

func TestPROpenedBeforeACleanLocalReviewFailsTheTurn(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	f.claude.turn = prTurn("sid-1")
	res := f.app.runTurn(context.Background(), f.store.recs[key], turnRequest{}, f.app.verifiedPR(io.Discard), io.Discard)
	if res.err == nil || !strings.Contains(res.err.Error(), "before a clean review") {
		t.Fatalf("err = %v", res.err)
	}
}
