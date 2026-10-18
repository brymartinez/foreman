package app

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/task"
)

// stalledTask makes the learnFixture task one whose self-review loop reached the fix
// limit, with diag on the limit decision and no competing review.
func stalledTask(rec *task.Record, diag task.DiagnosisKind, state task.State, answer string) {
	rec.Reviews = []task.Review{
		{Round: 1, Kind: task.SelfReview, PR: 77, Head: "head1", Findings: []task.Finding{{Severity: "blocking", Summary: "no test covers the error path"}}},
		{Round: 2, Kind: task.SelfReview, PR: 77, Head: "head2", Findings: []task.Finding{{Severity: "blocking", Summary: "no test covers the error path"}}},
	}
	limit := task.Decision{AskedAt: t0.Add(30 * time.Minute), Diagnosis: &task.Diagnosis{Kind: diag}, Question: task.Question{
		Issue:   "The implementer's own review still requests changes after 1 round of fixes. How should codex proceed?",
		Options: []task.Option{{Label: "A", Description: "Address the remaining findings in another round"}, stopAtLimitOption},
	}}
	if answer != "" {
		limit.Answer, limit.AnsweredAt = answer, t0.Add(40*time.Minute)
	}
	rec.Decisions = []task.Decision{limit}
	rec.State = state
}

var stopReply = optionText(task.Option{Label: "B", Description: stopAtLimitOption.Description})

func TestLearnLabelsSelfReviewRoundsAsTheImplementersOwn(t *testing.T) {
	f, ev, _ := learnFixture(t)
	rec := f.store.recs[learnKey]
	rec.Reviews = append([]task.Review{{Round: 0, Kind: task.SelfReview, PR: 77, Head: "head0", Findings: []task.Finding{{Severity: "blocking", Summary: "no test covers the error path"}}}}, rec.Reviews...)
	if err := f.app.Learn(context.Background(), learnKey, io.Discard); err != nil {
		t.Fatal(err)
	}
	p := ev.calls[0].Prompt
	if !strings.Contains(p, "Round 0 (the implementer's own review): changes requested") || !strings.Contains(p, "no test covers the error path") || !strings.Contains(p, "Round 1: changes requested") {
		t.Errorf("prompt does not label the self-review:\n%s", p)
	}
}

func TestLearningEligibilityOfATaskStalledAtTheSelfReviewLimit(t *testing.T) {
	for name, tc := range map[string]struct {
		diag   task.DiagnosisKind
		state  task.State
		answer string
		want   bool
	}{
		"stopped on a practice gap":            {task.DiagnosedPracticeGap, task.Failed, stopReply, true},
		"waiting on a practice gap":            {task.DiagnosedPracticeGap, task.NeedsDecision, "", true},
		"another round after a practice gap":   {task.DiagnosedPracticeGap, task.Working, "A. Address the remaining findings in another round", false},
		"failed later after another round":     {task.DiagnosedPracticeGap, task.Failed, "A. Address the remaining findings in another round", false},
		"stopped with no cause found":          {task.DiagnosedNone, task.Failed, stopReply, false},
		"waiting on a conflict in the context": {task.DiagnosedConflict, task.NeedsDecision, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			f, ev, _ := learnFixture(t)
			stalledTask(f.store.recs[learnKey], tc.diag, tc.state, tc.answer)
			due, err := f.app.LearningDue(f.store.recs[learnKey])
			if err != nil || due != tc.want {
				t.Fatalf("due = %v, %v; want %v", due, err, tc.want)
			}
			err = f.app.Learn(context.Background(), learnKey, io.Discard)
			if (err == nil) != tc.want || (len(ev.calls) == 1) != tc.want {
				t.Fatalf("learn err %v evaluator calls %d; want evaluated %v", err, len(ev.calls), tc.want)
			}
		})
	}
}

func TestSelfReviewFindingsAloneNeverMakeAnActiveLearning(t *testing.T) {
	f, ev, ks := learnFixture(t)
	stalledTask(f.store.recs[learnKey], task.DiagnosedPracticeGap, task.Failed, stopReply)
	practice := knowledge.Proposal{Kind: knowledge.Practice, Text: "Test the error path of every new branch.", Scope: knowledge.Reusable, Why: "self-review", Source: knowledge.Maintainer, Cites: []string{"D1"}}
	oneOff := knowledge.Proposal{Kind: knowledge.Fact, Text: "The retry fixture needs a token.", Scope: knowledge.OneOff, Why: "this task", Source: knowledge.Observed}
	ev.out = agent.Evaluation{Assessment: "stalled", Proposals: []knowledge.Proposal{practice, oneOff}}
	if err := f.app.Learn(context.Background(), learnKey, io.Discard); err != nil {
		t.Fatal(err)
	}
	// The limit decision says nothing of the practice, so the evaluator gets no D1 to
	// cite and the claim counts as observed.
	if strings.Contains(ev.calls[0].Prompt, "D1.") {
		t.Errorf("the limit decision is offered as maintainer evidence:\n%s", ev.calls[0].Prompt)
	}
	ls, _ := ks.Learnings("acme/api")
	if len(ls) != 1 || ls[0].Status != knowledge.Candidate || ls[0].Text != practice.Text {
		t.Fatalf("learnings = %+v", ls)
	}
}

func TestSelfReviewPracticeGapsFromTwoTasksFollowTheUsualPromotionAndWithdrawal(t *testing.T) {
	f, ev, ks := learnFixture(t)
	practice := knowledge.Proposal{Kind: knowledge.Practice, Text: "Test the error path of every new branch.", Scope: knowledge.Reusable, Why: "self-review", Source: knowledge.Observed}
	second := task.Key{Repo: "acme/api", Number: 42}
	for i, key := range []task.Key{learnKey, second} {
		rec := *f.store.recs[learnKey]
		rec.Key = key
		rec.Delegation = &task.Delegation{Implementer: task.Session{Agent: task.Codex}, StartedAt: t0.Add(time.Duration(i) * time.Hour), PR: 77 + i}
		stalledTask(&rec, task.DiagnosedPracticeGap, task.Failed, stopReply)
		f.store.recs[key] = &rec
	}
	status := func() knowledge.Status {
		ls, _ := ks.Learnings("acme/api")
		if len(ls) != 1 {
			t.Fatalf("learnings = %+v", ls)
		}
		return ls[0].Status
	}
	ev.out = agent.Evaluation{Assessment: "stalled", Proposals: []knowledge.Proposal{practice}}
	for _, step := range []struct {
		key      task.Key
		proposes bool
		want     knowledge.Status
	}{
		{learnKey, true, knowledge.Candidate},
		{second, true, knowledge.Active},
		{learnKey, false, knowledge.Candidate},
		{second, false, knowledge.Retired},
		{learnKey, true, knowledge.Retired},
	} {
		ev.out.Proposals = nil
		if step.proposes {
			ev.out.Proposals = []knowledge.Proposal{practice}
		}
		if err := f.app.Learn(context.Background(), step.key, io.Discard); err != nil {
			t.Fatal(err)
		}
		if got := status(); got != step.want {
			t.Fatalf("after evaluating #%d (proposes %v): status %s, want %s", step.key.Number, step.proposes, got, step.want)
		}
	}
}

func TestRunnerStartsALearnerWhenTheSelfReviewLimitFindsAPracticeGap(t *testing.T) {
	for name, tc := range map[string]struct {
		raw      string
		learning bool
		want     bool
	}{
		"practice gap":               {diagnosedGap, true, true},
		"no cause found":             {diagnosedNone, true, false},
		"practice gap, learning off": {diagnosedGap, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			f.app.Knowledge, f.app.Learning = knowledge.Store{Dir: t.TempDir()}, tc.learning
			f.app.FixRounds = 1
			f.app.Diagnoser = &fakeDiagnoser{raw: tc.raw}
			key := started(t, f, startReq(t))
			f.claude.turns = []agent.Turn{prTurn("sid-1"), prTurn("sid-1")}
			f.claude.reviews = []agent.ReviewOutcome{selfFindings(), selfFindings()}
			if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
				t.Fatal(err)
			}
			if got := f.store.recs[key]; got.State != task.NeedsDecision {
				t.Fatalf("state %s", got.State)
			}
			if started := slices.Equal(f.runners.learned, []task.Key{key}); started != tc.want {
				t.Fatalf("learners started = %v; want started %v", f.runners.learned, tc.want)
			}
		})
	}
}
