package app

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

func (a App) codeReview(ctx context.Context, rec *task.Record, kind task.ReviewKind, chosen task.Agent, log io.Writer) (agent.ReviewOutcome, error) {
	d := rec.Delegation
	reviewer, ok := a.Agents[chosen]
	if !ok {
		return agent.ReviewOutcome{}, fmt.Errorf("reviewer %s is not configured", chosen)
	}
	base, err := a.Worktrees.ReviewBase(ctx, d.Worktree, d.BaseRef)
	if err != nil {
		return agent.ReviewOutcome{}, fmt.Errorf("read the review base: %w", err)
	}
	head, _ := rec.CurrentChange()
	if base == "" || head == "" {
		return agent.ReviewOutcome{}, errors.New("code review requires a verified base and head")
	}
	instructions, err := a.Stages.Resolve(d.Worktree, task.StageReview)
	if err != nil {
		return agent.ReviewOutcome{}, err
	}
	self := kind == task.SelfReview
	purpose := "competing_review"
	if self {
		purpose = "self_review"
	}
	if err := a.startAttempt(ctx, rec); err != nil {
		return agent.ReviewOutcome{}, err
	}
	fmt.Fprintf(log, "foreman: %s reviewing %s (round %d, %s)\n", reviewer.Name(), reviewTarget(rec), len(rec.Reviews)+1, purpose)
	var out agent.ReviewOutcome
	if !a.metered {
		err = a.checkQuota(ctx, chosen, log)
	}
	if err == nil {
		out, err = reviewer.Review(ctx, agent.StartInput{
			Worktree:     d.Worktree,
			Log:          log,
			MaxBudgetUSD: a.ReviewerBudgetUSD,
			Prompt: agent.ReviewPrompt(agent.ReviewBrief{
				BaseHead: base, Head: head, Self: self, Passes: a.reviewRounds(),
				StageInstructions: instructions.Content(),
			}),
			Purpose: purpose,
			Step:    agent.StepReview,
		})
	}
	var unavailable quotaUnavailable
	if !self && errors.As(err, &unavailable) && unavailable.Status == QuotaExhausted {
		out = agent.ReviewOutcome{Skipped: true, Summary: fmt.Sprintf("Adversarial review skipped: %s quota exhausted", chosen)}
		fmt.Fprintf(log, "foreman: %s\n", out.Summary)
		err = nil
	}
	for _, limitation := range out.Limitations {
		if limitation.Blocking && err == nil {
			err = fmt.Errorf("review incomplete for %s: %s", limitation.Behavior, limitation.Reason)
		}
	}
	out.BaseRef, out.BaseHead = d.BaseRef, base
	return out, err
}
