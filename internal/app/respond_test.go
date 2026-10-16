package app

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

var retryQuestion = task.Question{
	Issue:          "Should retries apply to 4xx responses?",
	Context:        "Current code retries 5xx only.",
	Options:        []task.Option{{Label: "A", Description: "5xx only"}, {Label: "B", Description: "4xx + 5xx"}},
	Recommendation: "A",
	ImpactIfWrong:  "Repeated client errors.",
}

// blockedTask runs start + one blocked turn, leaving the task in NEEDS_DECISION.
func blockedTask(t *testing.T) (fixture, task.Key) {
	f := newFixture()
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	q := retryQuestion
	f.claude.turn = agent.Turn{Session: task.Session{Agent: task.Claude, ID: "sid-1"}, Outcome: agent.Outcome{Status: agent.Blocked, Summary: "need input", Questions: []task.Question{q}}}
	if err := f.run(context.Background(), rec.Key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.store.recs[rec.Key].State != task.NeedsDecision {
		t.Fatalf("setup: state %s", f.store.recs[rec.Key].State)
	}
	return f, rec.Key
}

func TestRespondRecordsAnswerAndResumesSession(t *testing.T) {
	f, key := blockedTask(t)
	f.runners.spawned = nil
	rec, err := f.app.Respond(context.Background(), key, "")
	if err != nil {
		t.Fatal(err)
	}
	d := rec.Decisions[0]
	if rec.State != task.Working || d.Answer != "A. 5xx only" || !d.AnsweredAt.Equal(now) || !d.DeliveredAt.IsZero() {
		t.Fatalf("after respond: state %s decision %+v", rec.State, d)
	}
	if len(f.runners.spawned) != 1 {
		t.Fatal("runner not relaunched")
	}
	if !strings.Contains(rec.Body, "## Decisions\n\n- ") || !strings.Contains(rec.Body, "Should retries apply to 4xx responses? → A. 5xx only") {
		t.Errorf("body = %q", rec.Body)
	}

	f.claude.turn = agent.Turn{Session: task.Session{Agent: task.Claude, ID: "sid-1"}, Outcome: agent.Outcome{Status: agent.PROpened, Summary: "done", PRNumber: 912}}
	starts := f.claude.starts
	if err := f.run(context.Background(), key, 4243, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(f.claude.resumed) != 1 || !strings.HasPrefix(f.claude.resumed[0], "sid-1: ") || !strings.Contains(f.claude.resumed[0], "Decision: A. 5xx only") {
		t.Errorf("resumed = %q", f.claude.resumed)
	}
	if f.claude.starts != starts {
		t.Error("started a fresh turn although the session resumed")
	}
	got := f.store.recs[key]
	if got.State != task.Reviewing || got.Decisions[0].DeliveredAt.IsZero() {
		t.Errorf("after resume: %s %+v", got.State, got.Decisions[0])
	}
}

func TestRunTaskFallsBackToFreshTurnWhenSessionIsGone(t *testing.T) {
	f, key := blockedTask(t)
	if _, err := f.app.Respond(context.Background(), key, "B"); err != nil {
		t.Fatal(err)
	}
	f.claude.resumeErr = fmt.Errorf("claude printed no result: %w", agent.ErrNoSession)
	f.claude.turn = agent.Turn{Session: task.Session{Agent: task.Claude, ID: "sid-2"}, Outcome: agent.Outcome{Status: agent.FailedRun, Summary: "x"}}
	starts := f.claude.starts
	if err := f.run(context.Background(), key, 4243, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.claude.starts != starts+1 || !strings.Contains(f.claude.prompt, "## Decisions already made") ||
		!strings.Contains(f.claude.prompt, "A: B. 4xx + 5xx") {
		t.Errorf("fresh turn prompt lacks the decision:\n%s", f.claude.prompt)
	}
	if f.store.recs[key].Delegation.Implementer.ID != "sid-2" {
		t.Error("new session not recorded")
	}
}

func TestRunTaskDoesNotFallBackOnOtherResumeErrors(t *testing.T) {
	f, key := blockedTask(t)
	f.app.Respond(context.Background(), key, "A")
	f.claude.resumeErr = fmt.Errorf("claude exited: usage limit")
	starts := f.claude.starts
	f.run(context.Background(), key, 4243, io.Discard)
	got := f.store.recs[key]
	if f.claude.starts != starts || got.State != task.Failed || !got.Decisions[0].DeliveredAt.IsZero() {
		t.Errorf("starts %d state %s decision %+v", f.claude.starts-starts, got.State, got.Decisions[0])
	}
}

func TestRespondRefusesTaskNotWaiting(t *testing.T) {
	f := newFixture()
	rec, _ := f.app.Start(context.Background(), startReq(t))
	if _, err := f.app.Respond(context.Background(), rec.Key, "A"); err == nil || !strings.Contains(err.Error(), "not waiting") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveAnswer(t *testing.T) {
	for raw, want := range map[string]string{
		"":                             "A. 5xx only",
		"b":                            "B. 4xx + 5xx",
		" A ":                          "A. 5xx only",
		"Keep retries limited to 5xx.": "Keep retries limited to 5xx.",
	} {
		if got, err := ResolveAnswer(retryQuestion, raw); err != nil || got != want {
			t.Errorf("ResolveAnswer(%q) = %q, %v", raw, got, err)
		}
	}
	noRec := retryQuestion
	noRec.Recommendation = "keep it simple"
	if _, err := ResolveAnswer(noRec, ""); err == nil {
		t.Error("empty answer accepted without a resolvable recommendation")
	}
}

func TestAppendDecisionNote(t *testing.T) {
	d := task.Decision{Question: task.Question{Issue: "Q?"}, Answer: "A. x", AnsweredAt: now}
	line := "- " + now.Local().Format("2006-01-02") + ": Q? → A. x\n"
	cases := map[string]string{
		"":                       "\n## Decisions\n\n" + line,
		"# #1 T\n\n## Notes\n\n": "# #1 T\n\n## Notes\n\n\n## Decisions\n\n" + line,
		"# T\n\n## Decisions\n\n- old\n\n## Notes\n\nmine\n": "# T\n\n## Decisions\n\n- old\n" + line + "\n## Notes\n\nmine\n",
		"# T\n\n## Decisions\n":                              "# T\n\n## Decisions\n\n" + line,
	}
	for body, want := range cases {
		if got := appendDecisionNote(body, d); got != want {
			t.Errorf("appendDecisionNote(%q)\n got %q\nwant %q", body, got, want)
		}
	}
}

func TestRespondWaitsForEveryQuestionThenResumesOnce(t *testing.T) {
	f := newFixture()
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	capQuestion := retryQuestion
	capQuestion.Issue = "Cap the retry count?"
	f.claude.turn = agent.Turn{Session: task.Session{Agent: task.Claude, ID: "sid-1"}, Outcome: agent.Outcome{Status: agent.Blocked, Summary: "need input", Questions: []task.Question{retryQuestion, capQuestion}}}
	if err := f.run(context.Background(), rec.Key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	f.runners.spawned = nil

	first, err := f.app.Respond(context.Background(), rec.Key, "B")
	if err != nil {
		t.Fatal(err)
	}
	if first.State != task.NeedsDecision || first.Pending().Question.Issue != "Cap the retry count?" || len(f.runners.spawned) != 0 {
		t.Fatalf("after one answer: state %s pending %+v spawned %d", first.State, first.Pending(), len(f.runners.spawned))
	}
	second, err := f.app.Respond(context.Background(), rec.Key, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.State != task.Working || len(f.runners.spawned) != 1 {
		t.Fatalf("after both answers: state %s spawned %d", second.State, len(f.runners.spawned))
	}

	f.claude.turn = prTurn("sid-1")
	if err := f.run(context.Background(), rec.Key, 4243, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(f.claude.resumed) != 1 {
		t.Fatalf("resumed %d times, want once: %q", len(f.claude.resumed), f.claude.resumed)
	}
	msg := f.claude.resumed[0]
	for _, want := range []string{"answered your questions", "Decision: B. 4xx + 5xx", "Cap the retry count?\nDecision: A. 5xx only"} {
		if !strings.Contains(msg, want) {
			t.Errorf("resume message lacks %q:\n%s", want, msg)
		}
	}
}
