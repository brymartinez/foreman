package app

import (
	"context"
	"io"
	"slices"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
)

func TestPlanFixAndReviewTurnsCarryTheirSteps(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2"), prTurn("sid-2")}
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking")}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	// The implementer's self-review of the PR comes before the competing review. The fix
	// keeps the head, so its self-review still covers it.
	if want := []string{agent.StepPlan, agent.StepImplement, agent.StepReview, agent.StepImplement}; !slices.Equal(f.claude.steps, want) {
		t.Errorf("implementer steps = %q, want %q", f.claude.steps, want)
	}
	if want := []string{agent.StepReview, agent.StepReview}; !slices.Equal(f.codex.steps, want) {
		t.Errorf("reviewer steps = %q, want %q", f.codex.steps, want)
	}
}

func TestFeedbackTurnCarriesTheFeedbackStep(t *testing.T) {
	f := newFixture()
	key := waiting(t, f)
	f.claude.steps = nil
	f.claude.turn = prTurn("sid-1")
	resumeRun(t, f, key)
	if want := []string{agent.StepFeedback}; !slices.Equal(f.claude.steps, want) {
		t.Errorf("implementer steps = %q, want %q", f.claude.steps, want)
	}
}

func TestMergeGateTurnCarriesTheMergeStep(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	f.wt.conflicts = [][]string{{"go.mod", "retry.go"}}
	mergesTo(f, "merge01")
	run(t, f, key)
	// The first turn opens the PR; the merge turn follows it, then the self-review.
	if len(f.claude.steps) != 3 || f.claude.steps[1] != agent.StepMerge {
		t.Errorf("implementer steps = %q, want the second to be %q", f.claude.steps, agent.StepMerge)
	}
}
