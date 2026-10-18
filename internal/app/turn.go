package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// turnRequest is the work an implementer turn resumes with when no answered decision
// or conflict repair comes first. At most one of findings, feedback, and merge is set.
type turnRequest struct {
	existingPlans map[task.PlanDoc]string // planning documents already on the issue
	findings      *task.Review            // open findings from the latest review
	feedback      []PRComment             // PR comments to answer
	merge         *agent.MergeBrief       // a base branch to merge before the next review
	// openBodyFile is set for the turn that pushes a reviewed local commit and opens
	// its PR. It holds the reviewed description.
	openBodyFile string
}

// turnResult is a finished implementer turn. pr is the verified PR when the turn
// reported pr_opened and the check passed; err is the turn's or the check's error.
type turnResult struct {
	turn           agent.Turn
	pr             *PullRequest
	checkpointHead string
	localHead      string // the verified commit of a review_ready turn
	err            error
}

const maxImplementationCheckpoints = 8
const maxCheckpointValidationRepairs = 2

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

// runTurn checks the task's account, runs one implementer turn, and checks the PR it
// reports. Apart from pinning an unpinned account, nothing is written to the task file
// here; finishTurn records the result.
func (a App) runTurn(ctx context.Context, rec *task.Record, req turnRequest, check prCheck, log io.Writer) turnResult {
	if err := a.checkAccount(ctx, rec); err != nil {
		return turnResult{err: err}
	}
	turn, err := a.implement(ctx, rec, req, log)
	turn, err = a.repairFailedCheckpoint(ctx, rec, req, turn, err, log)
	if err == nil {
		turn, err = a.requireClaims(ctx, rec, turn, req.openBodyFile != "", log)
	}
	// The open turn pushes the reviewed change, so it returns the reviewed claims.
	if d := rec.Delegation; err == nil && req.openBodyFile != "" && d.ClaimVerification && turn.Outcome.Status == agent.PROpened &&
		task.ClaimsDigest(turn.Outcome.Claims) != task.ClaimsDigest(d.Claims) {
		err = errors.New("the implementer opened the pull request with claims that are not the reviewed claims")
	}
	if err != nil {
		return turnResult{turn: turn, err: err}
	}
	if turn.Outcome.Status == agent.Checkpoint {
		if a.needsPlan(rec) || rec.Delegation.PR != 0 || req.findings != nil || len(req.feedback) > 0 || req.merge != nil || req.openBodyFile != "" || rec.Delegation.ConflictPending {
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
	if turn.Outcome.Status == agent.ReviewReady {
		return a.checkReviewReady(ctx, rec, req, turn, log)
	}
	if turn.Outcome.Status != agent.PROpened {
		return turnResult{turn: turn}
	}
	if rec.Delegation.BeforePR() && req.openBodyFile == "" {
		// The agent pushed and opened a PR that no review has passed. The task stops so
		// the developer sees it; nothing records the PR as reviewed.
		return turnResult{turn: turn, err: errors.New("the implementer opened a pull request before a clean review; review_before_pr requires review_ready")}
	}
	if a.needsPlan(rec) {
		fmt.Fprintf(log, "foreman: %s opened a PR without posting a plan\n", rec.Delegation.Implementer.Agent)
	}
	pr, err := check(ctx, rec, turn.Outcome)
	return turnResult{turn: turn, pr: pr, err: err}
}

// checkReviewReady verifies a review_ready turn: only a task reviewed before its PR
// returns it, outside planning, and it must leave a clean worktree on the task branch
// with commits after the last accepted checkpoint. Nothing is pushed yet, so the
// commit need not extend the one reviewed before, and a fix turn that keeps the
// commit is reviewed again like a PR whose head did not move.
func (a App) checkReviewReady(ctx context.Context, rec *task.Record, req turnRequest, turn agent.Turn, log io.Writer) turnResult {
	d := rec.Delegation
	if req.openBodyFile != "" {
		return turnResult{turn: turn, err: errors.New("the implementer returned review_ready when asked to open the reviewed pull request")}
	}
	if !d.BeforePR() || a.needsPlan(rec) {
		return turnResult{turn: turn, err: errors.New("review_ready is only allowed for a task reviewed before its pull request, once the plan is posted")}
	}
	head, err := a.Worktrees.Checkpoint(ctx, d.Worktree, d.Branch, d.CheckpointHead)
	if err != nil {
		return turnResult{turn: turn, err: fmt.Errorf("verify the change for review: %w", err)}
	}
	if err := a.checkUnpublished(ctx, rec); err != nil {
		return turnResult{turn: turn, err: err}
	}
	fmt.Fprintf(log, "foreman: verified local commit %s for review\n", short(head))
	return turnResult{turn: turn, localHead: head}
}

// checkUnpublished fails a review_ready turn that pushed the branch or opened a PR,
// because automated reviews on GitHub may already be running on unreviewed code.
func (a App) checkUnpublished(ctx context.Context, rec *task.Record) error {
	d := rec.Delegation
	pr, err := a.GitHub.OpenPullRequestForBranch(ctx, rec.Repo, d.Branch)
	if err != nil {
		return fmt.Errorf("check that no PR is open for %s: %w", d.Branch, err)
	}
	if pr != nil {
		return fmt.Errorf("the implementer opened PR #%d before a clean review; review_before_pr keeps the change local until then", pr.Key.Number)
	}
	remote, err := a.Worktrees.RemoteBranchHead(ctx, d.Worktree, d.Branch)
	if err != nil {
		return fmt.Errorf("check that %s is not pushed: %w", d.Branch, err)
	}
	if remote != "" {
		return fmt.Errorf("the implementer pushed %s at %s before a clean review; review_before_pr keeps the change local until then", d.Branch, short(remote))
	}
	return nil
}

// repairFailedCheckpoint gives the same implementation session a bounded chance
// to fix checks it reported as failing before Foreman accepts its checkpoint.
func (a App) repairFailedCheckpoint(ctx context.Context, rec *task.Record, req turnRequest, turn agent.Turn, err error, log io.Writer) (agent.Turn, error) {
	if a.needsPlan(rec) || rec.Delegation.PR != 0 || rec.Delegation.ConflictPending || req.findings != nil || len(req.feedback) > 0 || req.merge != nil {
		return turn, err
	}
	for attempt := 0; attempt < maxCheckpointValidationRepairs; attempt++ {
		var failed *agent.CheckpointValidationError
		if !errors.As(err, &failed) || turn.Outcome.Status != agent.Checkpoint || turn.Session.ID == "" {
			return turn, err
		}
		adapter := a.Agents[rec.Delegation.Implementer.Agent]
		if adapter == nil {
			return turn, fmt.Errorf("agent %s is not configured", rec.Delegation.Implementer.Agent)
		}
		// A repair is another implementer turn, and the account may have changed
		// during the one before it.
		if err := a.checkAccount(ctx, rec); err != nil {
			return turn, err
		}
		fmt.Fprintf(log, "foreman: checkpoint validation failed; resuming %s session %s for repair (%d/%d)\n", adapter.Name(), turn.Session.ID, attempt+1, maxCheckpointValidationRepairs)
		in := agent.StartInput{
			Worktree: rec.Delegation.Worktree,
			Prompt:   "Your checkpoint reported failed validation: " + strings.Join(failed.Failed, ", ") + ". Fix the underlying problem, rerun the failed checks, and continue the implementation. Report a checkpoint only when every validation check passes. If you cannot repair it, report why with status failed.",
			Stage:    task.StageImplement, Step: agent.StepImplement,
			Log: log, MaxBudgetUSD: a.ImplementerBudgetUSD, Profile: rec.Delegation.Profile,
		}
		next, resumeErr := adapter.Resume(ctx, turn.Session, in)
		addUsage(&turn.Usage, next.Usage)
		if next.Session.ID != "" {
			turn.Session = next.Session
		}
		turn.Outcome = next.Outcome
		err = resumeErr
	}
	return turn, err
}

// missingClaimsError fails a turn that did not supply its claims after one request.
// Every path fails the task on it, whatever else the turn did.
type missingClaimsError struct{ msg string }

func (e *missingClaimsError) Error() string { return e.msg }

// finishesChange reports a status that hands a change to review.
func finishesChange(s agent.Status) bool { return s == agent.PROpened || s == agent.ReviewReady }

// requireClaims gives a turn of a task with claim verification that finished a change
// without its claims one resume of the same session to add them. A second miss fails
// the turn, because Foreman does not review a change without its claims. The turn
// that opens a reviewed PR is asked for the reviewed claims, which it must return.
func (a App) requireClaims(ctx context.Context, rec *task.Record, turn agent.Turn, opening bool, log io.Writer) (agent.Turn, error) {
	d, status := rec.Delegation, turn.Outcome.Status
	if !d.ClaimVerification || !finishesChange(status) || len(turn.Outcome.Claims) > 0 {
		return turn, nil
	}
	missing := fmt.Sprintf("the implementer returned %s without the claim set for the PRD acceptance criteria", status)
	adapter := a.Agents[d.Implementer.Agent]
	if adapter == nil || turn.Session.ID == "" {
		return turn, &missingClaimsError{missing}
	}
	if err := a.checkAccount(ctx, rec); err != nil {
		return turn, err
	}
	fmt.Fprintf(log, "foreman: %s returned %s without claims; resuming session %s for them\n", adapter.Name(), status, turn.Session.ID)
	var reviewed []task.Claim
	if opening {
		reviewed = d.Claims
	}
	in := agent.StartInput{
		Worktree: d.Worktree, Prompt: agent.MissingClaimsMessage(status, reviewed),
		Stage: task.StageImplement, Step: agent.StepImplement,
		Log: log, MaxBudgetUSD: a.ImplementerBudgetUSD, Profile: d.Profile,
	}
	next, err := adapter.Resume(ctx, turn.Session, in)
	addUsage(&turn.Usage, next.Usage)
	if next.Session.ID != "" {
		turn.Session = next.Session
	}
	if err != nil {
		return turn, err
	}
	turn.Outcome = next.Outcome
	// The resume asked for the claims alone, so any other answer fails the turn.
	if turn.Outcome.Status != status || len(turn.Outcome.Claims) == 0 {
		return turn, &missingClaimsError{fmt.Sprintf("%s, and its answer to the request for them was %s without claims", missing, turn.Outcome.Status)}
	}
	return turn, nil
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
