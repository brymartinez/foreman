package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

func setHead(f fixture, sha string) {
	pr := f.gh.prs[912]
	pr.HeadSHA = sha
	f.gh.prs[912] = pr
}

func TestReviewRecordsTheHeadItCovered(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	got := f.store.recs[key]
	if got.Delegation.PRHead != "c0ffee1" || got.LatestReview().Head != "c0ffee1" || got.ReviewPending() {
		t.Fatalf("pr head %q review head %q pending %t", got.Delegation.PRHead, got.LatestReview().Head, got.ReviewPending())
	}
}

func TestRestartAfterAnInterruptedReviewSkipsImplementing(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	f.codex.reviewErr = errors.New("killed")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.Failed || !got.ReviewPending() {
		t.Fatalf("setup: state %s pending %t", got.State, got.ReviewPending())
	}

	f.codex.reviewErr = nil
	starts := f.claude.starts
	key = started(t, f, startReq(t))
	if err := f.run(context.Background(), key, 4243, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if f.claude.starts != starts {
		t.Errorf("the implementer ran again (%d starts)", f.claude.starts-starts)
	}
	cr := got.CompetingReviews()
	if got.State != task.WaitingForCI || got.Delegation.PR != 912 || len(cr) != 1 || !cr[0].Clean || cr[0].Head != "c0ffee1" || len(f.claude.reviewed) != 1 {
		t.Fatalf("state %s pr %d reviews %+v", got.State, got.Delegation.PR, got.Reviews)
	}
}

func TestNewCommitsAfterACleanReviewAreReviewedBeforeHandover(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	setHead(f, "beef002")
	setCI(f, task.CIStatus{State: task.CIPassing})
	f.gh.items = []InboxItem{item(825, "open", t0, task.Assigned)}
	report, err := f.app.Inbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.SelfReviewing || got.Delegation.PRHead != "beef002" || got.Delegation.RunnerPID != 4242 || len(f.runners.spawned) != 2 {
		t.Fatalf("state %s head %s pid %d spawned %d", got.State, got.Delegation.PRHead, got.Delegation.RunnerPID, len(f.runners.spawned))
	}
	if !strings.Contains(inboxText(report), "PR #912 has new commits since its clean review; codex is reviewing them") {
		t.Errorf("inbox didn't say why:\n%s", inboxText(report))
	}

	starts := f.claude.starts
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got = f.store.recs[key]
	if f.claude.starts != starts || got.State != task.WaitingForCI || got.LatestReview().Head != "beef002" {
		t.Fatalf("implementer starts %d state %s review head %s", f.claude.starts-starts, got.State, got.LatestReview().Head)
	}
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got = f.store.recs[key]; got.State != task.ReadyForHuman {
		t.Errorf("after the re-review: %s", got.State)
	}
}

func TestAnEditedDescriptionAfterACleanReviewIsSelfReviewedAndReviewedBeforeHandover(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	pr := f.gh.prs[912]
	pr.Body = "Retry 5xx and 429.\n\nCloses #825"
	f.gh.prs[912] = pr
	setCI(f, task.CIStatus{State: task.CIPassing})
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.SelfReviewing || got.Delegation.PRDigest != task.ProposalDigest(pr.Title, pr.Body) || len(f.runners.spawned) != 2 {
		t.Fatalf("state %s digest %s spawned %d", got.State, got.Delegation.PRDigest, len(f.runners.spawned))
	}
	if err := f.run(context.Background(), key, 4243, io.Discard); err != nil {
		t.Fatal(err)
	}
	got = f.store.recs[key]
	if kinds(got) != "self competing self competing" || got.State != task.WaitingForCI || f.claude.starts != 1 {
		t.Fatalf("state %s reviews %s implementer starts %d", got.State, kinds(got), f.claude.starts)
	}
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got = f.store.recs[key]; got.State != task.ReadyForHuman {
		t.Errorf("after the re-review: %s", got.State)
	}
}

func TestNewCommitsOnAHandedOverPRAreFlaggedNotTakenBack(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	setCI(f, task.CIStatus{State: task.CIPassing})
	f.gh.items = []InboxItem{item(825, "open", t0, task.Assigned)}
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	setHead(f, "beef002")
	if _, err := f.app.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.ReadyForHuman || len(f.runners.spawned) != 1 || !got.CommitsSinceReview() {
		t.Fatalf("state %s spawned %d flagged %t", got.State, len(f.runners.spawned), got.CommitsSinceReview())
	}
}

func inboxText(r InboxReport) string {
	var b strings.Builder
	for _, e := range append(r.Changed, r.New...) {
		fmt.Fprintf(&b, "%s#%d %s\n", e.Item.Key.Repo, e.Item.Key.Number, strings.Join(e.Changes, "; "))
	}
	return b.String()
}

func TestRestartImplementsWhenThePendingReviewsPRIsNoLongerOpen(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	f.codex.reviewErr = errors.New("killed")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	f.codex.reviewErr = nil
	setPR(f, "closed")
	starts := f.claude.starts
	key = started(t, f, startReq(t))
	f.claude.turn = agent.Turn{Session: task.Session{Agent: task.Claude, ID: "sid-2"}, Outcome: agent.Outcome{Status: agent.FailedRun, Summary: "gave up"}}
	if err := f.run(context.Background(), key, 4243, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.claude.starts != starts+1 {
		t.Errorf("the implementer didn't take over from a closed PR (%d starts)", f.claude.starts-starts)
	}
}

func TestRestartWithTheOtherAgentDoesNotKeepThePR(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	f.codex.reviewErr = errors.New("killed")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	req := startReq(t)
	req.Agent = task.Codex
	key = started(t, f, req)
	if got := f.store.recs[key]; got.Delegation.PR != 0 || got.ReviewPending() {
		t.Fatalf("kept PR #%d for a different implementer", got.Delegation.PR)
	}
}
