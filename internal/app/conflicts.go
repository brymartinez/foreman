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

// maxMergeAttempts is how many merge turns run before a PR that still conflicts goes
// to the developer (D129).
const maxMergeAttempts = 2

// resolveConflicts runs right before every review pass. It checks the PR against its
// latest base and, when they conflict, has the implementer merge the base and push. A
// clean merge changes nothing, even when the base moved. The merge turn records no
// review, so it does not use a fix round. ready is true when the review may start.
func (a App) resolveConflicts(ctx, final context.Context, rec *task.Record, log io.Writer) (_ *task.Record, ready bool, err error) {
	d := rec.Delegation
	for attempt := 0; ; attempt++ {
		base, files, checkErr := a.mergeConflicts(ctx, rec)
		if checkErr != nil {
			rec, err = a.update(final, rec.Key, existing(func(cur *task.Record) {
				cur.State = task.Failed
				cur.LastOutcome = &task.Outcome{Status: "failed", Summary: "conflict check failed: " + checkErr.Error(), At: a.Now()}
				applyStop(ctx, cur, "the conflict check", log, a.Now())
			}))
			return rec, false, err
		}
		// A maintainer can retarget an open PR. The task follows it, so later checks,
		// prompts, and the review use the base the PR has now.
		if ref := "origin/" + base; ref != rec.Delegation.BaseRef {
			fmt.Fprintf(log, "foreman: PR #%d now targets %s, not %s\n", d.PR, base, strings.TrimPrefix(rec.Delegation.BaseRef, "origin/"))
			if rec, err = a.update(final, rec.Key, existing(func(cur *task.Record) {
				cur.Delegation.BaseRef = ref
			})); err != nil {
				return rec, false, err
			}
		}
		if len(files) == 0 {
			return rec, true, nil
		}
		if attempt == maxMergeAttempts {
			fmt.Fprintf(log, "foreman: PR #%d still conflicts with origin/%s after %d merge turns; asking the developer\n", d.PR, base, attempt)
			rec, err = a.update(final, rec.Key, existing(func(cur *task.Record) {
				escalateConflict(cur, base, files, attempt, a.Now())
			}))
			return rec, false, err
		}
		fmt.Fprintf(log, "foreman: PR #%d conflicts with origin/%s in %s; merging\n", d.PR, base, strings.Join(files, ", "))
		turn, runErr := a.implement(ctx, rec, nil, nil, &agent.MergeBrief{Base: base, Files: files}, log)
		var pr *PullRequest
		if runErr == nil && turn.Outcome.Status == agent.PROpened {
			if pr, runErr = a.verifyPR(ctx, rec, turn.Outcome); runErr == nil {
				fmt.Fprintf(log, "foreman: verified PR #%d %s at %s\n", pr.Key.Number, pr.URL, short(pr.HeadSHA))
			}
		}
		if runErr == nil && turn.Outcome.Status == agent.Blocked {
			turn.Outcome.Questions = withConflicts(turn.Outcome.Questions, d.Worktree, base, files)
		}
		if rec, err = a.update(final, rec.Key, existing(func(cur *task.Record) {
			applyTurn(cur, turn, pr, runErr, a.Now())
			if runErr != nil {
				applyStop(ctx, cur, "merging the base branch", log, a.Now())
			}
			addUsage(&cur.Usage, turn.Usage)
		})); err != nil || rec.State != task.Reviewing {
			return rec, false, err
		}
	}
}

// withConflicts adds every conflicting file and where the merge waits to each question
// from a blocked merge turn. It always adds them, because the agent's text can leave a
// file out in ways a text search can't tell apart, such as a.go next to dir/a.go.
func withConflicts(qs []task.Question, worktree, base string, files []string) []task.Question {
	out := make([]task.Question, len(qs))
	for i, q := range qs {
		q.Context += fmt.Sprintf("\n\nMerging origin/%s left conflicts in %s. The merge is in progress in the worktree %s.", base, strings.Join(files, ", "), worktree)
		out[i] = q
	}
	return out
}

// mergeConflicts checks the open PR against the base GitHub reports for it now.
func (a App) mergeConflicts(ctx context.Context, rec *task.Record) (base string, files []string, err error) {
	d := rec.Delegation
	pr, err := a.verifyPR(ctx, rec, agent.Outcome{PRNumber: d.PR})
	if err != nil {
		return "", nil, err
	}
	files, err = a.Worktrees.MergeConflicts(ctx, MergeCheck{Dir: d.Worktree, Branch: d.Branch, Base: pr.BaseRefName})
	return pr.BaseRefName, files, err
}

// escalateConflict asks the developer to resolve a merge the implementer could not.
// The answer reaches the implementer like any decision, and the next pass checks again.
func escalateConflict(rec *task.Record, base string, files []string, attempts int, now time.Time) {
	d := rec.Delegation
	var ctx strings.Builder
	fmt.Fprintf(&ctx, "PR #%d still conflicts with origin/%s after %d merge %s by %s. Conflicting files:", d.PR, base, attempts, plural(attempts, "turn", "turns"), d.Implementer.Agent)
	for _, f := range files {
		fmt.Fprintf(&ctx, "\n- %s", f)
	}
	fmt.Fprintf(&ctx, "\nThe worktree is %s. To resolve by hand, run `git merge origin/%s` there unless a merge is already in progress, fix and stage the files, then answer A.", d.Worktree, base)
	rec.State = task.NeedsDecision
	rec.LastOutcome = &task.Outcome{Status: "blocked", Summary: fmt.Sprintf("PR #%d conflicts with origin/%s", d.PR, base), At: now}
	rec.Decisions = append(rec.Decisions, task.Decision{AskedAt: now, Question: task.Question{
		Issue:   fmt.Sprintf("Merging origin/%s into %s leaves conflicts %s could not resolve. How should it proceed?", base, d.Branch, d.Implementer.Agent),
		Context: ctx.String(),
		Options: []task.Option{
			{Label: "A", Description: "I resolved the files in the worktree; verify the resolution, commit the merge, and push"},
			{Label: "B", Description: "Try the merge again"},
		},
		Recommendation: "A",
		ImpactIfWrong:  "A with files still unresolved sends the agent to finish a merge that is not done; B spends more agent time on a merge it already failed.",
	}})
}
