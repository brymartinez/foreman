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

// resumeRun plays the developer selecting a REVIEWED task in INBOX.
func resumeRun(t *testing.T, f fixture, key task.Key) *task.Record {
	t.Helper()
	if _, err := f.app.Resume(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if err := f.run(context.Background(), key, 4243, io.Discard); err != nil {
		t.Fatal(err)
	}
	return f.store.recs[key]
}

// movesHead plays the agent committing a change: the PR head reported next differs.
func movesHead(f fixture, sha string) {
	pr := f.gh.prs[912]
	pr.HeadSHA = sha
	f.gh.prs[912] = pr
}

func TestDisabledPRFeedbackDoesNotStartReplyTurnAtHandover(t *testing.T) {
	f := newFixture()
	f.app.ReplyToPRFeedback = false
	f.gh.addPRComment(912, prComment(4401, "<!-- linear-linkback --> linked issue"))
	key := started(t, f, StartRequest{Repo: "testorg/example", Issue: 825, Agent: task.Claude})
	f.claude.turn = prTurn("sid-1")
	if err := f.run(context.Background(), key, 4243, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(f.claude.resumed) != 0 || f.gh.prCommentReads != 1 {
		t.Fatalf("resumed turns %d, comment reads %d", len(f.claude.resumed), f.gh.prCommentReads)
	}
}

func TestDisabledPRFeedbackRestartsFailedReplyWithoutAgentTurn(t *testing.T) {
	f := newFixture()
	key := waiting(t, f)
	f.store.recs[key].State = task.Failed
	f.app.ReplyToPRFeedback = false
	if _, err := f.app.Start(context.Background(), StartRequest{Repo: key.Repo, Issue: key.Number, Agent: task.Claude}); err != nil {
		t.Fatal(err)
	}
	if err := f.run(context.Background(), key, 4243, io.Discard); err != nil {
		t.Fatal(err)
	}
	rec := f.store.recs[key]
	if rec.State != task.ReadyForHuman || len(rec.Feedback) != 0 || len(f.claude.resumed) != 0 {
		t.Fatalf("state %s, feedback %+v, resumed turns %d", rec.State, rec.Feedback, len(f.claude.resumed))
	}
}

func TestDisabledPRFeedbackKeepsCIUnverifiedAfterFailedReply(t *testing.T) {
	f := newFixture()
	key := waiting(t, f)
	f.store.recs[key].State = task.Failed
	f.store.recs[key].CI = nil
	f.app.ReplyToPRFeedback = false
	if _, err := f.app.Start(context.Background(), StartRequest{Repo: key.Repo, Issue: key.Number, Agent: task.Claude}); err != nil {
		t.Fatal(err)
	}
	if err := f.run(context.Background(), key, 4243, io.Discard); err != nil {
		t.Fatal(err)
	}
	rec := f.store.recs[key]
	if rec.State != task.Reviewing || len(rec.Feedback) != 0 || len(f.claude.resumed) != 0 {
		t.Fatalf("state %s, feedback %+v, resumed turns %d", rec.State, rec.Feedback, len(f.claude.resumed))
	}
}

func TestFeedbackTurnWithoutCommitsReturnsReady(t *testing.T) {
	f := newFixture()
	key := waiting(t, f)
	before := f.store.recs[key]
	reviews := len(before.Reviews)
	f.claude.turn = prTurn("sid-1")

	rec := resumeRun(t, f, key)
	if rec.State != task.ReadyForHuman {
		t.Fatalf("state = %s, want READY_FOR_HUMAN", rec.State)
	}
	if n := len(rec.UnhandledFeedback()); n != 0 {
		t.Fatalf("%d comments still unanswered", n)
	}
	if len(rec.Reviews) != reviews {
		t.Errorf("an unchanged head was reviewed again: %d rounds, want %d", len(rec.Reviews), reviews)
	}
	if rec.CI == nil || rec.CI.State != task.CIPassing {
		t.Errorf("CI result lost: %+v", rec.CI)
	}
	if len(f.claude.resumed) != 1 {
		t.Fatalf("resumed %d times: %q", len(f.claude.resumed), f.claude.resumed)
	}
	prompt := f.claude.resumed[0]
	for _, want := range []string{"This retries 4xx too.", agent.ReplyMarker, "issues/912/comments"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the resumed prompt is missing %q:\n%s", want, prompt)
		}
	}
	if rec.LastOutcome.Status != "feedback_answered" {
		t.Errorf("outcome = %+v", rec.LastOutcome)
	}
}

func TestFeedbackTurnWithCommitsIsReviewed(t *testing.T) {
	f := newFixture()
	key := waiting(t, f)
	reviews := len(f.store.recs[key].CompetingReviews())
	f.claude.turn = prTurn("sid-1")
	movesHead(f, "d00d002")

	rec := resumeRun(t, f, key)
	if got := len(rec.CompetingReviews()); got != reviews+1 || !rec.SelfReviewPassed() {
		t.Fatalf("%d competing review rounds, want %d after a self-review of the new head", got, reviews+1)
	}
	if rec.State != task.Reviewing || rec.CI != nil {
		t.Fatalf("state = %s ci = %+v; the gate must check CI for the new head", rec.State, rec.CI)
	}
	if n := len(rec.UnhandledFeedback()); n != 0 {
		t.Errorf("%d comments still unanswered", n)
	}
}

func TestFailedFeedbackTurnKeepsTheCommentsWaiting(t *testing.T) {
	f := newFixture()
	key := waiting(t, f)
	f.claude.resumeErr = errors.New("usage limit reached")

	rec := resumeRun(t, f, key)
	if rec.State != task.Failed {
		t.Fatalf("state = %s", rec.State)
	}
	if n := len(rec.UnhandledFeedback()); n != 1 {
		t.Fatalf("%d comments waiting, want 1 kept for the next run", n)
	}
}

// A comment deleted between the refresh and the resume leaves nothing to answer, and
// the clean review still covers the head, so no agent turn runs.
func TestResumeWithTheCommentGoneHandsBackWithoutATurn(t *testing.T) {
	f := newFixture()
	key := waiting(t, f)
	reviews := len(f.store.recs[key].Reviews)
	f.gh.prComments[912] = nil

	rec := resumeRun(t, f, key)
	if rec.State != task.ReadyForHuman {
		t.Fatalf("state = %s, want READY_FOR_HUMAN", rec.State)
	}
	if f.claude.starts != 1 || len(f.claude.resumed) != 0 || len(rec.Reviews) != reviews {
		t.Errorf("starts %d resumes %d reviews %d; nothing should have run", f.claude.starts, len(f.claude.resumed), len(rec.Reviews))
	}
	if len(rec.Feedback) != 0 {
		t.Errorf("feedback = %+v", rec.Feedback)
	}
}

func TestResumeWithUnreadableCommentsStaysReviewed(t *testing.T) {
	f := newFixture()
	key := waiting(t, f)
	f.gh.prCommentErr = errors.New("HTTP 502")

	rec := resumeRun(t, f, key)
	if rec.State != task.Reviewed || len(rec.UnhandledFeedback()) != 1 {
		t.Fatalf("state = %s unanswered = %d; the comment must wait for the next resume", rec.State, len(rec.UnhandledFeedback()))
	}
	if f.claude.starts != 1 || len(f.claude.resumed) != 0 {
		t.Errorf("starts %d resumes %d; no turn can answer comments it cannot read", f.claude.starts, len(f.claude.resumed))
	}
}

// Restarting failed feedback with the other agent hands it the comments first.
func TestRestartWithAnotherAgentAnswersTheWaitingComments(t *testing.T) {
	f := newFixture()
	key := waiting(t, f)
	f.claude.resumeErr = errors.New("usage limit reached")
	if rec := resumeRun(t, f, key); rec.State != task.Failed {
		t.Fatalf("setup: state %s", rec.State)
	}
	req := startReq(t)
	req.Agent = task.Codex
	if _, err := f.app.Start(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	f.codex.turn = prTurn("cx-1")
	f.codex.turn.Session.Agent = task.Codex
	if err := f.run(context.Background(), key, 4244, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.codex.starts != 1 || !strings.Contains(f.codex.prompt, "This retries 4xx too.") {
		t.Fatalf("codex started %d times; its prompt lacks the comment:\n%s", f.codex.starts, f.codex.prompt)
	}
	if rec := f.store.recs[key]; len(rec.UnhandledFeedback()) != 0 {
		t.Errorf("%d comments still unanswered", len(rec.UnhandledFeedback()))
	}
}

// A question from a feedback turn is the developer's to answer, and the comments stay
// waiting until a turn actually answers them.
func TestBlockedFeedbackTurnKeepsTheCommentsWaiting(t *testing.T) {
	f := newFixture()
	key := waiting(t, f)
	f.claude.turn = agent.Turn{Session: task.Session{Agent: task.Claude, ID: "sid-1"}, Outcome: agent.Outcome{
		Status: agent.Blocked, Summary: "which cap?", Questions: []task.Question{{Issue: "Cap retries at 3?",
			Options: []task.Option{{Label: "A", Description: "3"}, {Label: "B", Description: "no cap"}}}},
	}}
	rec := resumeRun(t, f, key)
	if rec.State != task.NeedsDecision || len(rec.UnhandledFeedback()) != 1 {
		t.Fatalf("state = %s unanswered = %d", rec.State, len(rec.UnhandledFeedback()))
	}
}

func TestCommentsDuringARunAreAnsweredBeforeHandover(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	// The comment lands while the implementer turn is still going.
	f.claude.during = func() {
		f.gh.addPRComment(912, prComment(4401, "One more thing."))
		f.claude.during = nil
	}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	rec := f.store.recs[key]
	if n := len(rec.UnhandledFeedback()); n != 0 {
		t.Fatalf("%d comments left for the developer, want 0", n)
	}
	if len(f.claude.resumed) != 1 || !strings.Contains(f.claude.resumed[0], "One more thing.") {
		t.Fatalf("the run did not answer the comment: %q", f.claude.resumed)
	}
	// The implementer was never interrupted: it ran to its own end first.
	if f.claude.starts != 1 {
		t.Errorf("implementer started %d times", f.claude.starts)
	}
	if rec.State != task.Reviewing || len(rec.CompetingReviews()) != 1 {
		t.Errorf("state = %s reviews = %d; the clean review still stands", rec.State, len(rec.CompetingReviews()))
	}
}

func TestFeedbackRoundsAreCappedPerRun(t *testing.T) {
	f := newFixture()
	key := waiting(t, f)
	f.claude.turn = prTurn("sid-1")
	// An agent that forgets the reply marker: every answer reads as another comment.
	id := int64(4401)
	f.claude.onResume = func() {
		id++
		f.gh.addPRComment(912, prComment(id, "and another thing"))
	}

	rec := resumeRun(t, f, key)
	if len(f.claude.resumed) != maxFeedbackRounds {
		t.Fatalf("answered %d times, want the cap of %d", len(f.claude.resumed), maxFeedbackRounds)
	}
	if rec.State != task.Reviewed || len(rec.UnhandledFeedback()) == 0 {
		t.Fatalf("state = %s unanswered = %d; the rest waits for the developer", rec.State, len(rec.UnhandledFeedback()))
	}
}

func TestCancelledFeedbackTurnIsRecordedAsCancelled(t *testing.T) {
	f := newFixture()
	key := waiting(t, f)
	if _, err := f.app.Resume(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	f.claude.onResume = func() { cancel(errors.New("terminated signal received")) }
	f.claude.resumeErr = errors.New("claude printed no result (signal: terminated)")
	var log strings.Builder
	if err := f.run(ctx, key, 4243, &log); err != nil {
		t.Fatal(err)
	}
	rec := f.store.recs[key]
	if rec.State != task.Failed || rec.LastOutcome.Status != Cancelled || rec.LastOutcome.Summary != "run cancelled during answering PR feedback (terminated signal received)" {
		t.Fatalf("state %s outcome %+v", rec.State, rec.LastOutcome)
	}
	if n := len(rec.UnhandledFeedback()); n != 1 {
		t.Errorf("%d comments waiting, want 1 kept for the next run", n)
	}
	if !strings.Contains(log.String(), "foreman: stopped during answering PR feedback: terminated signal received") {
		t.Errorf("log = %q", log.String())
	}
}
