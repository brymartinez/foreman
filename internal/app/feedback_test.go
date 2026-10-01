package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// prComment is one comment on PR #912, as GitHub would report it.
func prComment(id int64, body string) PRComment {
	return PRComment{Kind: task.IssueComment, ID: id, Author: "brymartinez", Body: body,
		URL: "https://github.com/ridezoomo/armada-backend/pull/912#issuecomment-1", UpdatedAt: t1}
}

// ready runs a task to READY_FOR_HUMAN: a clean review, then a passing CI gate.
func ready(t *testing.T, f fixture) task.Key {
	t.Helper()
	key := reviewed(t, f)
	setCI(f, task.CIStatus{State: task.CIPassing})
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.ReadyForHuman {
		t.Fatalf("setup: state %s", got.State)
	}
	// The hand-over itself reads the conversation, so each test counts its own reads.
	f.gh.prCommentReads = 0
	return key
}

func refresh(t *testing.T, f fixture) {
	t.Helper()
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestGateRaisesReviewedOnNewComment(t *testing.T) {
	f := newFixture()
	key := ready(t, f)
	f.notes.sent = nil
	f.gh.addPRComment(912, prComment(4401, "This retries 4xx too."))
	refresh(t, f)

	got := f.store.recs[key]
	if got.State != task.Reviewed {
		t.Fatalf("state = %s, want REVIEWED", got.State)
	}
	if n := len(got.UnhandledFeedback()); n != 1 {
		t.Fatalf("%d comments waiting, want 1", n)
	}
	if len(f.notes.sent) != 1 || !strings.Contains(f.notes.sent[0], "feedback") {
		t.Errorf("notifications = %q", f.notes.sent)
	}
	// It stays REVIEWED, and nothing is announced twice.
	refresh(t, f)
	if got := f.store.recs[key]; got.State != task.Reviewed || len(f.notes.sent) != 1 {
		t.Errorf("second refresh: state %s notifications %q", got.State, f.notes.sent)
	}
}

func TestGateIgnoresForemanRepliesAndEmptyComments(t *testing.T) {
	f := newFixture()
	key := ready(t, f)
	f.gh.addPRComment(912, prComment(4401, agent.ReplyMarker+"\nNo change needed: the cap is already there."))
	f.gh.addPRComment(912, prComment(4402, "   "))
	refresh(t, f)

	got := f.store.recs[key]
	if got.State != task.ReadyForHuman || len(got.Feedback) != 0 {
		t.Fatalf("state = %s feedback = %+v", got.State, got.Feedback)
	}
}

func TestGateReRaisesAnEditedComment(t *testing.T) {
	f := newFixture()
	key := ready(t, f)
	f.gh.addPRComment(912, prComment(4401, "first"))
	refresh(t, f)
	// The agent answered it.
	rec := f.store.recs[key]
	rec.MarkFeedbackHandled(rec.UnhandledFeedback(), now)
	rec.State = task.ReadyForHuman
	refresh(t, f)
	if got := f.store.recs[key]; got.State != task.ReadyForHuman {
		t.Fatalf("an answered comment raised the task again: %s", got.State)
	}

	f.gh.prComments[912] = []PRComment{prComment(4401, "first, edited")}
	refresh(t, f)
	if got := f.store.recs[key]; got.State != task.Reviewed || len(got.UnhandledFeedback()) != 1 {
		t.Fatalf("state = %s unanswered = %+v", got.State, got.UnhandledFeedback())
	}
}

func TestGateSkipsFeedbackWhileARunnerWorks(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	f.store.recs[key].Delegation.RunnerPID = 77
	f.runners.alive[77] = true
	f.gh.addPRComment(912, prComment(4401, "a thought"))
	refresh(t, f)
	if got := f.store.recs[key]; got.State != task.Reviewing || f.gh.prCommentReads != 0 {
		t.Fatalf("state = %s comment reads = %d", got.State, f.gh.prCommentReads)
	}
}

func TestGateSkipsFeedbackOnAClosedPR(t *testing.T) {
	f := newFixture()
	key := ready(t, f)
	setPR(f, "closed")
	f.gh.addPRComment(912, prComment(4401, "closing this"))
	refresh(t, f)
	if got := f.store.recs[key]; got.State != task.PRClosed || f.gh.prCommentReads != 0 {
		t.Fatalf("state = %s comment reads = %d", got.State, f.gh.prCommentReads)
	}
}

// A refresh that cannot read the conversation keeps the task where it is and warns,
// the way a failed PR or CI lookup does.
func TestGateWarnsWhenCommentsCannotBeRead(t *testing.T) {
	f := newFixture()
	key := ready(t, f)
	f.gh.prCommentErr = errors.New("gh: Server Error (HTTP 500)")
	_, warnings, err := f.app.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.ReadyForHuman {
		t.Fatalf("state = %s", got.State)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "comments") {
		t.Fatalf("warnings = %v", warnings)
	}
}

// A comment GitHub no longer returns must not hold a task in REVIEWED, so the task
// goes back to the developer with nothing left to answer.
func TestGateReleasesATaskWhoseCommentWasDeleted(t *testing.T) {
	f := newFixture()
	key := ready(t, f)
	f.gh.addPRComment(912, prComment(4401, "never mind"))
	refresh(t, f)
	if got := f.store.recs[key]; got.State != task.Reviewed {
		t.Fatalf("setup: state %s", got.State)
	}
	f.gh.prComments[912] = nil
	refresh(t, f)
	if got := f.store.recs[key]; len(got.Feedback) != 0 || got.State != task.ReadyForHuman {
		t.Fatalf("state = %s feedback = %+v", got.State, got.Feedback)
	}
}
