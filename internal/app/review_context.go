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
	self := kind == task.SelfReview
	stage := task.StageReview
	if self {
		stage = task.StageSelfReview
	}
	instructions, err := a.Stages.Resolve(d.Worktree, stage)
	if err != nil {
		return agent.ReviewOutcome{}, err
	}
	purpose := "competing_review"
	if self {
		purpose = "self_review"
	}
	if err := a.startAttempt(ctx, rec); err != nil {
		return agent.ReviewOutcome{}, err
	}
	fmt.Fprintf(log, "foreman: %s reviewing %s (round %d, %s)\n", reviewer.Name(), reviewTarget(rec), len(rec.Reviews)+1, purpose)
	action := task.Action{Kind: task.SelfReviewAction, BaseRef: d.BaseRef, BaseHead: base, Head: head}
	if !self {
		action.Kind = task.AdversarialAction
	}
	if d.Action != nil && d.Action.Kind == action.Kind &&
		d.Action.Head == head && d.Action.BaseHead == base && d.Action.BaseRef == d.BaseRef &&
		d.Action.FromHead != "" && d.Action.FromHead != head {
		if paths, err := a.Worktrees.ChangedPaths(ctx, d.Worktree, d.Action.FromHead, head); err == nil && len(paths) > 0 {
			action.FromHead = d.Action.FromHead
		}
	}
	fullRevision := ""
	if !self && rec.Delegation.FullSuiteGate && !rec.FullResult.Covers(head) && rec.FullResult != nil && rec.FullResult.Covered {
		fullRevision = rec.FullResult.Revision
	}
	in := agent.StartInput{
		Worktree: d.Worktree, Log: log, MaxBudgetUSD: a.ReviewerBudgetUSD,
		Prompt: agent.ReviewPrompt(agent.ReviewBrief{
			BaseHead: base, FromHead: action.FromHead, Head: head, Self: self, Passes: a.reviewRounds(),
			StageInstructions: instructions.Content(), FullSuiteRevision: fullRevision,
		}),
		Purpose: purpose, Step: agent.StepReview,
	}
	_, err = a.beginAction(ctx, rec, action)
	if err != nil {
		return agent.ReviewOutcome{}, err
	}
	var out agent.ReviewOutcome
	if err = a.checkQuota(ctx, chosen, log); err == nil {
		out, err = reviewer.Review(ctx, in)
	}
	if out.Skipped && err == nil {
		err = errors.New("review must complete before the task can advance")
	}
	for _, limitation := range out.Limitations {
		if limitation.Blocking && err == nil {
			err = fmt.Errorf("review incomplete for %s: %s", limitation.Behavior, limitation.Reason)
		}
	}
	out.BaseRef, out.BaseHead, out.FromHead = d.BaseRef, base, action.FromHead
	return out, err
}
