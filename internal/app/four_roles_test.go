package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

func TestFourRolesUseOneImplementerAndReturnEachFixToItsReviewer(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	fix := prTurn("implementation")
	fix.Outcome.ReviewAssessment = &task.ReviewAssessment{Round: 1}
	f.claude.turns = []agent.Turn{plannedTurn("planning"), prTurn("implementation"), fix, prTurn("implementation")}
	f.claude.reviews = []agent.ReviewOutcome{selfFindings()}
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking")}
	f.wt.changed = map[string][]string{"c0ffee1..c0ffee2": {"retry.go"}}
	f.claude.onResume = func() {
		switch len(f.claude.resumed) {
		case 1:
			setHead(f, "c0ffee2")
		case 2:
			setHead(f, "c0ffee3")
		}
	}

	got := run(t, f, key)
	if got.State != task.WaitingForCI || kinds(got) != "self! self competing! competing" {
		t.Fatalf("state %s, reviews %s, outcome %+v", got.State, kinds(got), got.LastOutcome)
	}
	d := got.Delegation
	if d.Planner.ID != "planning" || d.Implementer.ID != "implementation" || d.Adversary.ID != "retained-review" || d.Action != nil {
		t.Fatalf("saved sessions or next action are wrong: %+v", d)
	}
	if f.claude.starts != 2 || len(f.claude.scopeReviewed) != 0 || len(f.claude.resumed) != 2 {
		t.Fatalf("starts %d, plan-check calls %d, resumes %d", f.claude.starts, len(f.claude.scopeReviewed), len(f.claude.resumed))
	}
	for _, msg := range f.claude.resumed {
		if !strings.HasPrefix(msg, "implementation: ") {
			t.Fatalf("correction used a different session: %q", msg)
		}
	}
	if len(f.claude.reviewed) != 2 || !strings.Contains(f.claude.reviewed[0], "basehead...c0ffee1") ||
		!strings.Contains(f.claude.reviewed[1], "c0ffee1..c0ffee2") || got.Reviews[1].FromHead != "c0ffee1" {
		t.Fatalf("self-review ranges: %q", f.claude.reviewed)
	}
	if f.codex.reviewStarts != 1 || len(f.codex.reviewResumes) != 1 ||
		f.codex.reviewResumes[0].ID != d.Adversary.ID || !strings.Contains(f.codex.reviewed[1], "basehead...c0ffee3") {
		t.Fatalf("adversarial starts %d resumes %+v", f.codex.reviewStarts, f.codex.reviewResumes)
	}
}

func TestInterruptedAdversarialReviewResumesItsSavedSession(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("implementation")
	f.codex.reviewErr = errors.New("provider disconnected")
	stopped := run(t, f, key)
	action := stopped.Delegation.Action
	if stopped.State != task.Failed || action == nil || action.Kind != task.AdversarialAction || action.Head != "c0ffee1" ||
		stopped.Delegation.Adversary.ID != "retained-review" {
		t.Fatalf("saved action %+v, state %s", action, stopped.State)
	}
	f.codex.reviewErr = nil
	key = started(t, f, startReq(t))
	got := run(t, f, key)
	if got.State != task.WaitingForCI || f.codex.reviewStarts != 1 || len(f.codex.reviewResumes) != 1 || f.claude.starts != 1 {
		t.Fatalf("state %s, reviewer starts %d resumes %+v, implementation starts %d", got.State, f.codex.reviewStarts, f.codex.reviewResumes, f.claude.starts)
	}
}

func TestAnUncertainLaunchRequiresExplicitReplacement(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	rec := f.store.recs[key]
	postedPlan(f, rec)
	rec.Delegation.Action = &task.Action{ID: "pending", Kind: task.ImplementAction, Started: true}
	f.claude.turn = prTurn("replacement")
	got := run(t, f, key)
	if got.State != task.NeedsDecision || f.claude.starts != 0 || got.Pending().Question.Issue != replaceSessionQuestion {
		t.Fatalf("state %s, starts %d, outcome %+v", got.State, f.claude.starts, got.LastOutcome)
	}
	launches := len(f.runners.spawned)
	paused, err := f.app.Respond(context.Background(), key, "B")
	if err != nil || paused.State != task.Failed || paused.Delegation.Action.ID != "pending" || len(f.runners.spawned) != launches {
		t.Fatalf("pause did not preserve the pending launch: %+v, %v", paused, err)
	}
	key = started(t, f, startReq(t))
	got = run(t, f, key)
	if got.State != task.NeedsDecision || f.claude.starts != 0 {
		t.Fatalf("retry started an unapproved replacement: state %s, starts %d", got.State, f.claude.starts)
	}
	if _, err := f.app.Respond(context.Background(), key, "A"); err != nil {
		t.Fatal(err)
	}
	got = run(t, f, key)
	if got.State != task.WaitingForCI || f.claude.starts != 1 || got.Delegation.Implementer.ID != "replacement" {
		t.Fatalf("state %s, starts %d, implementer %+v", got.State, f.claude.starts, got.Delegation.Implementer)
	}
}

func TestInterruptedLocalCorrectionKeepsItsReviewedCommitOnRestart(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	f.wt.checkpointHeads = []string{"commit1"}
	f.claude.turns = []agent.Turn{reviewReadyTurn("implementation", proposedBody), {}}
	f.claude.turnErrs = []error{nil, errors.New("interrupted after commit")}
	f.claude.reviews = []agent.ReviewOutcome{selfFindings()}
	got := run(t, f, key)
	if got.State != task.Failed || got.Delegation.Action == nil || got.Delegation.Action.Kind != task.FixSelfAction {
		t.Fatalf("state %s, action %+v", got.State, got.Delegation.Action)
	}

	f.wt.head = "commit2"
	f.wt.checkpointHeads = []string{"commit2"}
	f.wt.changed = map[string][]string{"commit1..commit2": {"retry.go"}}
	key = started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{selfFix(reviewReadyTurn("implementation", proposedBody), 1), prTurn("implementation")}
	opensPR(t, f, "commit2", proposedTitle, proposedBody, 1)
	got = run(t, f, key)
	if got.State != task.WaitingForCI || len(f.claude.reviewed) != 2 ||
		!strings.Contains(f.claude.reviewed[1], "commit1..commit2") || f.claude.starts != 1 {
		t.Fatalf("state %s, starts %d, reviews %q, outcome %+v", got.State, f.claude.starts, f.claude.reviewed, got.LastOutcome)
	}
}
