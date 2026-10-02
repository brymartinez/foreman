package app

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// conflict is the gate's look at a handed-over PR that GitHub reports as conflicting
// with its base. It sends the implementer back to merge the base, once per PR head and
// base head, so a conflict the agent could not fix does not start a run on every
// refresh (D130). Unknown mergeability waits for a later refresh.
func (a App) conflict(ctx context.Context, rec *task.Record, pr PullRequest) (*task.Record, string, error) {
	d := rec.Delegation
	if pr.State != "open" || pr.Mergeable != Conflicting || rec.State != task.ReadyForHuman || !a.handsOver(rec) || pr.HeadSHA == "" {
		return rec, "", nil
	}
	base, err := a.GitHub.BranchHead(ctx, rec.Repo, pr.BaseRefName)
	if err != nil {
		return rec, "", fmt.Errorf("refresh %s#%d base %s: %w", rec.Repo, rec.Number, pr.BaseRefName, err)
	}
	if d.ConflictAttempted(pr.HeadSHA, base) {
		return rec, "", nil
	}
	var note string
	updated, err := a.update(ctx, rec.Key, func(cur *task.Record) (*task.Record, error) {
		// Read again under the lock: a resume or another refresh since the gate's list
		// owns the task now, and the pair is recorded here so only one run starts.
		if cur == nil || cur.State != task.ReadyForHuman || cur.Delegation == nil || cur.Delegation.PR != pr.Key.Number ||
			a.liveRunner(cur) || cur.Delegation.ConflictAttempted(pr.HeadSHA, base) {
			return cur, nil
		}
		cd := cur.Delegation
		cd.ConflictAttempts = append(cd.ConflictAttempts, task.ConflictAttempt{Head: pr.HeadSHA, Base: base})
		cd.ConflictPending = true
		cd.PRHead = pr.HeadSHA
		cur.State, cur.CI = task.Working, nil
		note = fmt.Sprintf("PR #%d conflicts with %s; %s is repairing it", cd.PR, pr.BaseRefName, cd.Implementer.Agent)
		cur.LastOutcome = &task.Outcome{Status: "conflict_repair", Summary: note, At: a.Now()}
		return cur, nil
	})
	if err != nil || note == "" {
		return updated, "", err
	}
	updated, err = a.launch(ctx, rec.Key)
	return updated, note, err
}

// repairConflict runs the implementer turn that merges the base into the PR branch.
// The agent's pr_opened is not taken on trust: the PR must be the same one, its head
// must have moved, the new head must contain the base head the gate saw, and every
// check the agent reported must have passed.
func (a App) repairConflict(ctx, final context.Context, rec *task.Record, log io.Writer) (*task.Record, error) {
	d, attempt := rec.Delegation, rec.Delegation.LastConflict()
	fmt.Fprintf(log, "foreman: PR #%d conflicts with %s at %s; asking %s to merge it\n", d.PR, strings.TrimPrefix(d.BaseRef, "origin/"), short(attempt.Base), d.Implementer.Agent)
	turn, err := a.implement(ctx, rec, nil, nil, log)
	var pr *PullRequest
	if err == nil && turn.Outcome.Status == agent.PROpened {
		if pr, err = a.checkRepair(ctx, rec, turn.Outcome); err == nil {
			fmt.Fprintf(log, "foreman: PR #%d now includes %s at %s\n", pr.Key.Number, short(attempt.Base), short(pr.HeadSHA))
		}
	}
	return a.update(final, rec.Key, existing(func(cur *task.Record) {
		cur.Delegation.ConflictPending = false
		if err != nil && ctx.Err() != nil {
			// A cancelled run is FAILED like any other, and the attempt stays spent.
			applyTurn(cur, turn, nil, err, a.Now())
			applyStop(ctx, cur, "merge conflict repair", log, a.Now())
		} else {
			applyConflictTurn(cur, turn, pr, err, a.Now())
		}
		addUsage(&cur.Usage, turn.Usage)
	}))
}

func (a App) checkRepair(ctx context.Context, rec *task.Record, o agent.Outcome) (*PullRequest, error) {
	d, attempt := rec.Delegation, rec.Delegation.LastConflict()
	pr, err := a.verifyPR(ctx, rec, o)
	if err != nil {
		return nil, err
	}
	if pr.Key.Number != d.PR {
		return nil, fmt.Errorf("the agent reported PR #%d, not PR #%d", pr.Key.Number, d.PR)
	}
	if pr.HeadSHA == attempt.Head {
		return nil, fmt.Errorf("PR #%d head is still %s; nothing was pushed", d.PR, short(pr.HeadSHA))
	}
	merged, err := a.Worktrees.Contains(ctx, d.Worktree, attempt.Base, pr.HeadSHA)
	if err != nil {
		return nil, fmt.Errorf("check that PR #%d includes %s: %w", d.PR, short(attempt.Base), err)
	}
	if !merged {
		return nil, fmt.Errorf("PR #%d head %s does not include base commit %s", d.PR, short(pr.HeadSHA), short(attempt.Base))
	}
	// A merge the agent's own checks fail on is not a repair (D131).
	var failed []string
	for _, c := range o.Validation {
		if !c.Passed {
			failed = append(failed, c.Command)
		}
	}
	if len(failed) > 0 {
		return nil, fmt.Errorf("validation failed after the merge: %s", strings.Join(failed, ", "))
	}
	return pr, nil
}

// applyConflictTurn records the repair. A verified merge goes to review like any new
// head. A question from the agent is asked as it is. Anything else asks the developer
// how to go on, because the gate will not try the same heads again.
func applyConflictTurn(rec *task.Record, turn agent.Turn, pr *PullRequest, err error, now time.Time) {
	if err == nil && turn.Outcome.Status == agent.PROpened {
		applyTurn(rec, turn, pr, nil, now)
		return
	}
	if err == nil && turn.Outcome.Status == agent.Blocked {
		applyTurn(rec, turn, nil, nil, now)
		rec.LastOutcome.Summary = fmt.Sprintf("merge conflict on PR #%d needs a decision: %s", rec.Delegation.PR, turn.Outcome.Summary)
		return
	}
	if turn.Session.ID != "" {
		rec.Delegation.Implementer = turn.Session
	}
	reason := strings.TrimSpace(turn.Outcome.Summary)
	if err != nil {
		reason = err.Error()
	}
	if reason == "" {
		reason = fmt.Sprintf("the agent ended with %q", turn.Outcome.Status)
	}
	d := rec.Delegation
	base := strings.TrimPrefix(d.BaseRef, "origin/")
	rec.State, rec.CI = task.NeedsDecision, nil
	summary := fmt.Sprintf("merge conflict on PR #%d was not repaired: %s", d.PR, reason)
	rec.LastOutcome = &task.Outcome{Status: "conflict_unresolved", Summary: summary, At: now}
	rec.Decisions = append(rec.Decisions, task.Decision{AskedAt: now, Question: task.Question{
		Issue: fmt.Sprintf("PR #%d still conflicts with %s. How should %s continue?", d.PR, base, d.Implementer.Agent),
		Context: fmt.Sprintf("foreman asked %s to merge origin/%s (%s) into %s, and the repair failed: %s. The branch and worktree are kept.",
			d.Implementer.Agent, base, short(d.LastConflict().Base), d.Branch, reason),
		Options: []task.Option{
			{Label: "A", Description: fmt.Sprintf("Merge origin/%s into the branch again, resolve the conflicts, run the checks, and push without force", base)},
			{Label: "B", Description: "I resolved the conflict myself; check the branch and continue"},
		},
		Recommendation: "A",
		ImpactIfWrong:  "A spends another agent turn on the merge; B sends the PR to review as it is now.",
	}})
}
