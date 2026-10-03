package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/brymartinez/foreman/internal/task"
)

// CleanRequest selects one task by issue, or every DONE task when Issue is 0.
type CleanRequest struct {
	Repo   task.Repo
	Issue  int
	DryRun bool
}

type CleanResult struct {
	Rec *task.Record
	// BranchDeleted is set when the local branch went with the worktree (or would, on a dry run).
	BranchDeleted bool
	Err           error
}

// Clean removes the worktrees of finished tasks. A branch is deleted only when its PR
// is merged and DeleteMergedBranches is on; otherwise it stays, so a new start can pick
// the work up again. A worktree with uncommitted changes is kept and reported.
func (a App) Clean(ctx context.Context, req CleanRequest) ([]CleanResult, error) {
	var recs []*task.Record
	if req.Issue > 0 {
		rec, err := a.Find(ctx, req.Repo, req.Issue)
		if err != nil {
			return nil, err
		}
		ref := fmt.Sprintf("%s#%d", rec.Repo, rec.Number)
		switch {
		case !finished(rec.State):
			return nil, fmt.Errorf("%s is %s; only DONE, FAILED, or PR_CLOSED tasks are cleaned", ref, rec.State)
		case a.liveRunner(rec):
			// A PR_CLOSED task keeps its pid until the stop succeeds, and the
			// runner is still working in the worktree this would remove.
			return nil, fmt.Errorf("%s is still running (pid %d); cancel it first", ref, rec.Delegation.RunnerPID)
		case !hasWorktree(rec):
			return nil, fmt.Errorf("%s has no worktree to remove", ref)
		}
		recs = append(recs, rec)
	} else {
		all, err := a.Store.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range all {
			if r.State == task.Done && hasWorktree(r) {
				recs = append(recs, r)
			}
		}
	}
	results := make([]CleanResult, len(recs))
	for i, r := range recs {
		results[i] = a.clean(ctx, r, req.DryRun)
	}
	return results, nil
}

// cleanDone is the automatic cleanup inbox runs when CleanOnDone is set. It adds
// inbox notes and updates recs in place.
func (a App) cleanDone(ctx context.Context, recs []*task.Record, notes map[task.Key][]string) {
	for i, r := range recs {
		if r.State != task.Done || !hasWorktree(r) || a.liveRunner(r) {
			continue
		}
		res := a.clean(ctx, r, false)
		recs[i] = res.Rec
		notes[r.Key] = append(notes[r.Key], cleanNote(res))
	}
}

func cleanNote(res CleanResult) string {
	switch {
	case errors.Is(res.Err, ErrWorktreeDirty):
		return fmt.Sprintf("worktree kept: %v; commit or discard them, and a later refresh removes it", res.Err)
	case res.Err != nil:
		return fmt.Sprintf("worktree kept: %v", res.Err)
	case res.BranchDeleted:
		return "worktree and merged branch removed"
	}
	return "worktree removed"
}

// finished states have no agent left to run, so their worktree can be removed by
// name. Only DONE is cleaned automatically; the other two are the developer's call.
func finished(s task.State) bool {
	return s == task.Done || s == task.Failed || s == task.PRClosed
}

func hasWorktree(r *task.Record) bool {
	d := r.Delegation
	return d != nil && d.Worktree != "" && d.WorktreeRemovedAt.IsZero()
}

func (a App) clean(ctx context.Context, rec *task.Record, dryRun bool) CleanResult {
	d := rec.Delegation
	res := CleanResult{Rec: rec, BranchDeleted: a.DeleteMergedBranches && rec.State == task.Done && a.prMerged(ctx, rec)}
	if dryRun {
		return res
	}
	if err := a.Worktrees.Remove(ctx, WorktreeRemoval{Path: d.Worktree, Branch: d.Branch, DeleteBranch: res.BranchDeleted}); err != nil {
		res.Err, res.BranchDeleted = err, false
		return res
	}
	updated, err := a.update(ctx, rec.Key, func(cur *task.Record) (*task.Record, error) {
		cur.Delegation.WorktreeRemovedAt = a.Now()
		return cur, nil
	})
	if err != nil {
		res.Err = err
		return res
	}
	res.Rec = updated
	return res
}

// prMerged asks GitHub rather than trusting the task state: an issue closed by hand is
// DONE too, and its branch may hold the only copy of the work. An error keeps the branch.
func (a App) prMerged(ctx context.Context, rec *task.Record) bool {
	if rec.Delegation.PR == 0 {
		return false
	}
	pr, err := a.GitHub.GetPullRequest(ctx, task.Key{Repo: rec.Repo, Number: rec.Delegation.PR})
	return err == nil && pr.State == "merged"
}
