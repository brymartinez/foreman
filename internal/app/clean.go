package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/brymartinez/foreman/internal/task"
)

type CleanResult struct {
	Rec *task.Record
	// BranchDeleted is set when the local branch went with the worktree.
	BranchDeleted bool
	Err           error
}

// cleanDone is the automatic cleanup inbox runs when CleanOnDone is set. It cleans
// tasks whose issue or PR has closed: DONE, PR_CLOSED, and tasks whose issue is in
// closed, which this refresh ends. It adds inbox notes and updates recs in place.
func (a App) cleanDone(ctx context.Context, recs []*task.Record, closed map[task.Key]bool, notes map[task.Key][]string) {
	for i, r := range recs {
		ended := r.State == task.Done || r.State == task.PRClosed || (closed[r.Key] && !keepsState(r))
		if !ended || !hasWorktree(r) || a.liveRunner(r) {
			continue
		}
		res := a.clean(ctx, r)
		recs[i] = res.Rec
		notes[r.Key] = append(notes[r.Key], cleanNote(res))
	}
}

func cleanNote(res CleanResult) string {
	switch {
	case errors.Is(res.Err, ErrWorktreeDirty):
		return fmt.Sprintf("worktree kept: %v; commit or discard them, and a later refresh removes it", res.Err)
	case errors.Is(res.Err, ErrBranchNotDeleted):
		return "worktree removed; branch remains; cleanup will retry"
	case res.Err != nil:
		return fmt.Sprintf("worktree kept: %v", res.Err)
	case res.BranchDeleted:
		return "worktree and merged branch removed"
	}
	return "worktree removed"
}

func hasWorktree(r *task.Record) bool {
	d := r.Delegation
	return d != nil && d.Worktree != "" && d.WorktreeRemovedAt.IsZero()
}

func (a App) clean(ctx context.Context, rec *task.Record) CleanResult {
	d := rec.Delegation
	res := CleanResult{Rec: rec}
	if a.DeleteMergedBranches {
		// Without an answer the worktree stays, so a later refresh can still delete a
		// merged branch; once the worktree is marked removed, nothing returns to it.
		merged, err := a.prMerged(ctx, rec)
		if err != nil {
			res.Err = fmt.Errorf("check whether PR #%d merged: %w", d.PR, err)
			return res
		}
		res.BranchDeleted = merged
	}
	if err := a.Worktrees.Remove(ctx, WorktreeRemoval{Path: d.Worktree, Branch: d.Branch, DeleteBranch: res.BranchDeleted, RepoDir: d.RepoDir}); err != nil {
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
// DONE too, and its branch may hold the only copy of the work. A task the gate could
// not read is still FAILED when its closed issue is cleaned, and its PR may have merged.
func (a App) prMerged(ctx context.Context, rec *task.Record) (bool, error) {
	if rec.Delegation.PR == 0 {
		return false, nil
	}
	pr, err := a.GitHub.GetPullRequest(ctx, task.Key{Repo: rec.Repo, Number: rec.Delegation.PR})
	return err == nil && pr.State == "merged", err
}
