package app

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/task"
)

type fakeEvaluator struct {
	out   agent.Evaluation
	err   error
	calls []agent.EvaluateInput
}

func (f *fakeEvaluator) Evaluate(_ context.Context, in agent.EvaluateInput) (agent.Evaluation, error) {
	f.calls = append(f.calls, in)
	return f.out, f.err
}

var learnKey = task.Key{Repo: "acme/api", Number: 41}

func proposal(text string) knowledge.Proposal {
	return knowledge.Proposal{Kind: knowledge.Convention, Text: text, Scope: knowledge.Reusable, Why: "seen in review", Source: knowledge.Observed}
}

// learnFixture is a task that finished with a clean review on its second round.
func learnFixture(t *testing.T) (fixture, *fakeEvaluator, knowledge.Store) {
	t.Helper()
	f := newFixture()
	ks := knowledge.Store{Dir: t.TempDir()}
	ev := &fakeEvaluator{out: agent.Evaluation{Assessment: "went fine", Proposals: []knowledge.Proposal{proposal("Run make lint before pushing.")}}}
	f.app.Knowledge, f.app.Evaluator, f.app.LearningBudgetUSD, f.app.Learning = ks, ev, 0.75, true
	f.store.recs[learnKey] = &task.Record{
		Key: learnKey, Kind: task.Issue, Title: "Add retries", State: task.ReadyForHuman,
		Delegation: &task.Delegation{
			Implementer: task.Session{Agent: task.Codex, ID: "th-1"},
			Profile:     &task.Profile{Name: "deep", Agent: task.Codex, PlanModel: "gpt-big", PlanReasoningEffort: "high"},
			StartedAt:   t0, PR: 77, PRHead: "head2",
		},
		Decisions: []task.Decision{{
			Question: task.Question{
				Issue: "Which backoff should retries use?", Recommendation: "A",
				Options: []task.Option{{Label: "A", Description: "fixed delay"}, {Label: "B", Description: "exponential delay"}},
			},
			Answer: "B", AnsweredAt: t0.Add(10 * time.Minute),
		}},
		Reviews: []task.Review{
			{Round: 1, PR: 77, Head: "head1", Findings: []task.Finding{{Severity: "blocking", Summary: "missing test for the retry cap", Detail: "DETAIL-TEXT-NOT-FOR-EVALUATOR"}}},
			{Round: 2, PR: 77, Head: "head2", Clean: true},
		},
		Feedback:   []task.Feedback{{Kind: task.IssueComment, ID: 5, Digest: task.Digest("please keep the old flag")}},
		CI:         &task.CIStatus{State: task.CIFailing, Failing: []task.CheckRun{{Name: "ci-check-lint-job"}}},
		Validation: []task.Check{{Command: "go test ./VALIDATION-COMMAND", Passed: true}},
		Usage:      task.Usage{CodexInputTokens: 4200, CodexOutputTokens: 900, CodexTurns: 3, ToolCalls: map[string]int{"command_execution": 12, "file_change": 4}},
		Attempts: []task.Attempt{{
			StartedAt: t0, HandoffAt: t0.Add(60 * time.Minute),
			Marks: []task.Mark{{At: t0, Phase: task.PhasePlanning}, {At: t0.Add(20 * time.Minute), Phase: task.PhaseImplementation}, {At: t0.Add(45 * time.Minute), Phase: task.PhaseReview}},
		}},
	}
	f.gh.prComments = map[int][]PRComment{77: {
		{Kind: task.IssueComment, ID: 5, Body: "please keep the old flag"},
		{Kind: task.IssueComment, ID: 6, Body: "Done. " + agent.ReplyMarker},
	}}
	return f, ev, ks
}

func TestLearnSendsTheEvidencePacketToTheImplementerAgent(t *testing.T) {
	f, ev, _ := learnFixture(t)
	if err := f.app.Learn(context.Background(), learnKey, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(ev.calls) != 1 {
		t.Fatalf("evaluator calls = %d", len(ev.calls))
	}
	in := ev.calls[0]
	if in.Agent != task.Codex || in.Model != "gpt-big" || in.ReasoningEffort != "high" || in.MaxBudgetUSD != 0.75 {
		t.Errorf("input = %+v", in)
	}
	for _, want := range []string{
		"Which backoff should retries use?", "B: exponential delay", "overrode the recommendation",
		"missing test for the retry cap", "commits followed", "please keep the old flag",
		"planning: 20m", "implementation: 25m", "review: 15m",
		"4200 input", "command_execution: 12", "PR outcome: open",
	} {
		if !strings.Contains(in.Prompt, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	for _, unwanted := range []string{"ci-check-lint-job", "DETAIL-TEXT-NOT-FOR-EVALUATOR", "VALIDATION-COMMAND", agent.ReplyMarker} {
		if strings.Contains(in.Prompt, unwanted) {
			t.Errorf("prompt holds %q", unwanted)
		}
	}
}

func TestLearnStoresTheEvaluationWithItsFingerprint(t *testing.T) {
	f, _, ks := learnFixture(t)
	if err := f.app.Learn(context.Background(), learnKey, io.Discard); err != nil {
		t.Fatal(err)
	}
	stored, err := ks.Evaluation("acme/api", "41")
	if err != nil || stored == nil {
		t.Fatalf("stored = %v, %v", stored, err)
	}
	if stored.Assessment != "went fine" || stored.Fingerprint == "" || !stored.TaskAt.Equal(t0) || stored.Agent != "codex" {
		t.Errorf("stored = %+v", stored)
	}
	ls, _ := ks.Learnings("acme/api")
	if len(ls) != 1 || ls[0].Status != knowledge.Candidate {
		t.Errorf("learnings = %+v", ls)
	}
	due, err := f.app.LearningDue(f.store.recs[learnKey])
	if err != nil || due {
		t.Errorf("due right after learning = %v, %v", due, err)
	}
}

func TestLearnFailedTurnLeavesStoreAndFingerprintUnchanged(t *testing.T) {
	f, ev, ks := learnFixture(t)
	ctx := context.Background()
	if err := f.app.Learn(ctx, learnKey, io.Discard); err != nil {
		t.Fatal(err)
	}
	before, _ := ks.Evaluation("acme/api", "41")
	rec := f.store.recs[learnKey]
	rec.Feedback = append(rec.Feedback, task.Feedback{Kind: task.ReviewComment, ID: 9, Digest: task.Digest("rename it")})
	ev.err = errors.New("evaluator crashed")
	if err := f.app.Learn(ctx, learnKey, io.Discard); err == nil {
		t.Fatal("want the evaluator's error")
	}
	after, _ := ks.Evaluation("acme/api", "41")
	if after.Fingerprint != before.Fingerprint || after.At != before.At {
		t.Errorf("evaluation changed after a failed turn: %+v", after)
	}
	if due, _ := f.app.LearningDue(rec); !due {
		t.Error("a failed turn should leave the task due")
	}
}

func TestLearnRerunReplacesTheTasksEvidence(t *testing.T) {
	f, ev, ks := learnFixture(t)
	ctx := context.Background()
	if err := f.app.Learn(ctx, learnKey, io.Discard); err != nil {
		t.Fatal(err)
	}
	ev.out.Proposals = []knowledge.Proposal{proposal("Keep old flags working.")}
	if err := f.app.Learn(ctx, learnKey, io.Discard); err != nil {
		t.Fatal(err)
	}
	ls, _ := ks.Learnings("acme/api")
	status := map[string]knowledge.Status{}
	for _, l := range ls {
		status[l.Text] = l.Status
	}
	if status["Run make lint before pushing."] != knowledge.Retired || status["Keep old flags working."] != knowledge.Candidate {
		t.Errorf("statuses = %v", status)
	}
}

func TestLearnGivesTheEvaluatorOnlyItsOwnRepositorysLearnings(t *testing.T) {
	f, ev, ks := learnFixture(t)
	other := knowledge.Evaluation{Task: "3", TaskAt: t0, At: t0, Assessment: "OTHER-REPO-ASSESSMENT", Proposals: []knowledge.Proposal{
		{Kind: knowledge.Fact, Text: "Other repository fact.", Scope: knowledge.Reusable, Source: knowledge.Maintainer},
	}}
	if err := ks.Apply("acme/web", other); err != nil {
		t.Fatal(err)
	}
	own := knowledge.Evaluation{Task: "40", TaskAt: t0.Add(-time.Hour), At: t0, Assessment: "earlier task in api", Proposals: []knowledge.Proposal{
		{Kind: knowledge.Fact, Text: "The api uses sqlc.", Scope: knowledge.Reusable, Source: knowledge.Maintainer},
	}}
	if err := ks.Apply("acme/api", own); err != nil {
		t.Fatal(err)
	}
	if err := f.app.Learn(context.Background(), learnKey, io.Discard); err != nil {
		t.Fatal(err)
	}
	p := ev.calls[0].Prompt
	if !strings.Contains(p, "The api uses sqlc.") || !strings.Contains(p, "earlier task in api") {
		t.Errorf("prompt lacks the repository's own knowledge:\n%s", p)
	}
	if strings.Contains(p, "OTHER-REPO-ASSESSMENT") || strings.Contains(p, "Other repository fact.") {
		t.Errorf("prompt holds another repository's knowledge:\n%s", p)
	}
}

func TestLearnStopsWhenTheRepositoryIsLocked(t *testing.T) {
	f, ev, ks := learnFixture(t)
	unlock, err := ks.Lock("acme/api")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := f.app.Learn(context.Background(), learnKey, io.Discard); !errors.Is(err, knowledge.ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
	if len(ev.calls) != 0 {
		t.Error("the evaluator ran while the repository was locked")
	}
}

func TestLearnRefusesATaskWithoutACleanReview(t *testing.T) {
	f, ev, _ := learnFixture(t)
	f.store.recs[learnKey].Reviews = f.store.recs[learnKey].Reviews[:1]
	if err := f.app.Learn(context.Background(), learnKey, io.Discard); err == nil {
		t.Fatal("want an error")
	}
	if len(ev.calls) != 0 {
		t.Error("the evaluator ran without a clean review")
	}
}

func TestLearningDue(t *testing.T) {
	ctx := context.Background()
	for name, change := range map[string]func(*task.Record){
		"answered decision": func(r *task.Record) { r.Decisions = append(r.Decisions, task.Decision{Answer: "A", AnsweredAt: now}) },
		"new human comment": func(r *task.Record) {
			r.Feedback = append(r.Feedback, task.Feedback{Kind: task.ReviewComment, ID: 9, Digest: "d"})
		},
		"edited human comment": func(r *task.Record) { r.Feedback[0].Digest = task.Digest("please drop the old flag") },
		"new review round":     func(r *task.Record) { r.Reviews = append(r.Reviews, task.Review{Round: 3, Clean: true}) },
		"new PR head":          func(r *task.Record) { r.Delegation.PRHead = "head3" },
		"PR merged":            func(r *task.Record) { r.State = task.Done; r.Attempts[0].EndedAt = now },
	} {
		t.Run("due after "+name, func(t *testing.T) {
			f, _, _ := learnFixture(t)
			if err := f.app.Learn(ctx, learnKey, io.Discard); err != nil {
				t.Fatal(err)
			}
			rec := f.store.recs[learnKey]
			change(rec)
			if due, err := f.app.LearningDue(rec); err != nil || !due {
				t.Errorf("due = %v, %v", due, err)
			}
		})
	}
	t.Run("due before any evaluation", func(t *testing.T) {
		f, _, _ := learnFixture(t)
		if due, err := f.app.LearningDue(f.store.recs[learnKey]); err != nil || !due {
			t.Errorf("due = %v, %v", due, err)
		}
	})
	t.Run("not due without a clean review", func(t *testing.T) {
		f, _, _ := learnFixture(t)
		rec := f.store.recs[learnKey]
		rec.Reviews = rec.Reviews[:1]
		if due, _ := f.app.LearningDue(rec); due {
			t.Error("due without a clean review")
		}
	})
	t.Run("not due after a CI change", func(t *testing.T) {
		f, _, _ := learnFixture(t)
		if err := f.app.Learn(ctx, learnKey, io.Discard); err != nil {
			t.Fatal(err)
		}
		rec := f.store.recs[learnKey]
		rec.CI = &task.CIStatus{State: task.CIPassing}
		if due, _ := f.app.LearningDue(rec); due {
			t.Error("a CI change made the task due")
		}
	})
	t.Run("not due without a knowledge store", func(t *testing.T) {
		f, _, _ := learnFixture(t)
		f.app.Knowledge = nil
		if due, _ := f.app.LearningDue(f.store.recs[learnKey]); due {
			t.Error("due without a store")
		}
	})
}

func TestLearnReadsPRCommentsTheTaskNeverRecorded(t *testing.T) {
	f, ev, _ := learnFixture(t)
	// The gate stops recording comments once the PR merges, so one posted just before
	// the merge is on GitHub but not in the task file.
	f.store.recs[learnKey].State = task.Done
	f.gh.addPRComment(77, PRComment{Kind: task.IssueComment, ID: 9, Body: "next time split the migration out"})
	if err := f.app.Learn(context.Background(), learnKey, io.Discard); err != nil {
		t.Fatal(err)
	}
	if p := ev.calls[0].Prompt; !strings.Contains(p, "next time split the migration out") || !strings.Contains(p, "please keep the old flag") {
		t.Errorf("prompt lacks a developer comment:\n%s", p)
	}
}

func TestLearnReadsPRCommentsWhenTheTaskRecordedNone(t *testing.T) {
	f, ev, _ := learnFixture(t)
	f.store.recs[learnKey].Feedback = nil
	if err := f.app.Learn(context.Background(), learnKey, io.Discard); err != nil {
		t.Fatal(err)
	}
	if p := ev.calls[0].Prompt; !strings.Contains(p, "please keep the old flag") || strings.Contains(p, agent.ReplyMarker) {
		t.Errorf("prompt comments wrong:\n%s", p)
	}
}

func TestLearnLeavesOutCommentsFromBots(t *testing.T) {
	f, ev, _ := learnFixture(t)
	f.gh.addPRComment(77, PRComment{Kind: task.ReviewComment, ID: 10, Author: "dependabot[bot]", Body: "BOT-SAYS-BUMP-DEPENDENCIES"})
	if err := f.app.Learn(context.Background(), learnKey, io.Discard); err != nil {
		t.Fatal(err)
	}
	if p := ev.calls[0].Prompt; strings.Contains(p, "BOT-SAYS") || !strings.Contains(p, "please keep the old flag") {
		t.Errorf("prompt comments wrong:\n%s", p)
	}
}

func TestLearnLabelsDecisionsAndCommentsAndStoresTheirIDs(t *testing.T) {
	f, ev, ks := learnFixture(t)
	if err := f.app.Learn(context.Background(), learnKey, io.Discard); err != nil {
		t.Fatal(err)
	}
	p := ev.calls[0].Prompt
	for _, want := range []string{"D1. Which backoff should retries use?", "C1: please keep the old flag"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	stored, _ := ks.Evaluation("acme/api", "41")
	if stored == nil || !slices.Equal(stored.MaintainerRefs, []string{"D1", "C1"}) {
		t.Errorf("stored = %+v, want refs D1 and C1", stored)
	}
}

func TestLearnTreatsAMaintainerClaimCitingNothingRealAsObserved(t *testing.T) {
	f, ev, ks := learnFixture(t)
	p := proposal("Claimed by the evaluator.")
	p.Source, p.Cites = knowledge.Maintainer, []string{"C4"}
	ev.out.Proposals = []knowledge.Proposal{p}
	if err := f.app.Learn(context.Background(), learnKey, io.Discard); err != nil {
		t.Fatal(err)
	}
	if ls, _ := ks.Learnings("acme/api"); len(ls) != 1 || ls[0].Status != knowledge.Candidate {
		t.Errorf("learnings = %+v, want a candidate", ls)
	}
}

func TestLearnDoesNothingWhileLearningIsOff(t *testing.T) {
	f, ev, ks := learnFixture(t)
	f.app.Learning = false
	var log strings.Builder
	if err := f.app.Learn(context.Background(), learnKey, &log); err != nil {
		t.Fatal(err)
	}
	if len(ev.calls) != 0 {
		t.Errorf("evaluator ran %d times with learning off", len(ev.calls))
	}
	if at, _ := ks.Attempt("acme/api", "41"); at != nil {
		t.Errorf("recorded an attempt with learning off: %+v", at)
	}
	if !strings.Contains(log.String(), "learning is off") {
		t.Errorf("log = %q", log.String())
	}
}
