package app

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// turnRequest is the work an implementer turn resumes with when no answered decision
// or conflict repair comes first. At most one field is set.
type turnRequest struct {
	findings *task.Review      // open findings from the latest review
	feedback []PRComment       // PR comments to answer
	merge    *agent.MergeBrief // a base branch to merge before the next review
}

// turnResult is a finished implementer turn. pr is the verified PR when the turn
// reported pr_opened and the check passed; err is the turn's or the check's error.
type turnResult struct {
	turn           agent.Turn
	pr             *PullRequest
	checkpointHead string
	err            error
}

const maxImplementationCheckpoints = 8

// prCheck verifies a turn's pr_opened claim and logs what it found.
type prCheck func(ctx context.Context, rec *task.Record, o agent.Outcome) (*PullRequest, error)

// verifiedPR is the check every turn but a conflict repair uses.
func (a App) verifiedPR(log io.Writer) prCheck {
	return func(ctx context.Context, rec *task.Record, o agent.Outcome) (*PullRequest, error) {
		pr, err := a.verifyPR(ctx, rec, o)
		if err == nil {
			fmt.Fprintf(log, "foreman: verified PR #%d %s at %s\n", pr.Key.Number, pr.URL, short(pr.HeadSHA))
		}
		return pr, err
	}
}

// runTurn runs one implementer turn and checks the PR it reports. Nothing is written
// to the task file here; finishTurn records the result.
func (a App) runTurn(ctx context.Context, rec *task.Record, req turnRequest, check prCheck, log io.Writer) turnResult {
	turn, err := a.implement(ctx, rec, req, log)
	if err != nil {
		return turnResult{turn: turn, err: err}
	}
	if turn.Outcome.Status == agent.Checkpoint {
		if a.needsPlan(rec) || rec.Delegation.PR != 0 || req.findings != nil || len(req.feedback) > 0 || req.merge != nil || rec.Delegation.ConflictPending {
			return turnResult{turn: turn, err: fmt.Errorf("checkpoint is only allowed during initial implementation")}
		}
		if rec.Delegation.Checkpoints >= maxImplementationCheckpoints {
			return turnResult{turn: turn, err: fmt.Errorf("implementation exceeded %d checkpoints", maxImplementationCheckpoints)}
		}
		head, err := a.Worktrees.Checkpoint(ctx, rec.Delegation.Worktree, rec.Delegation.Branch, rec.Delegation.CheckpointHead)
		if err != nil {
			return turnResult{turn: turn, err: fmt.Errorf("verify implementation checkpoint: %w", err)}
		}
		fmt.Fprintf(log, "foreman: verified implementation checkpoint %s\n", short(head))
		return turnResult{turn: turn, checkpointHead: head}
	}
	if turn.Outcome.Status != agent.PROpened {
		return turnResult{turn: turn}
	}
	if a.needsPlan(rec) {
		fmt.Fprintf(log, "foreman: %s opened a PR without posting a plan\n", rec.Delegation.Implementer.Agent)
	}
	pr, err := check(ctx, rec, turn.Outcome)
	return turnResult{turn: turn, pr: pr, err: err}
}

// finishTurn writes a turn's result through final, so a cancelled run still records
// it. apply maps the result onto the task the way its caller needs. A failed turn
// that was cancelled is recorded as cancelled during the named activity, and the
// turn's usage always counts.
func (a App) finishTurn(ctx, final context.Context, key task.Key, during string, res turnResult, apply func(cur *task.Record), log io.Writer) (*task.Record, error) {
	return a.update(final, key, existing(func(cur *task.Record) {
		apply(cur)
		if res.err != nil {
			applyStop(ctx, cur, during, log, a.Now())
		}
		addUsage(&cur.Usage, res.turn.Usage)
	}))
}

// failTask ends a task as FAILED with the reason the developer sees.
func failTask(rec *task.Record, summary string, now time.Time) {
	rec.State = task.Failed
	rec.LastOutcome = &task.Outcome{Status: "failed", Summary: summary, At: now}
}
