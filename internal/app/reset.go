package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// Reset discards a FAILED task's local run: its task file, runner log, worktree with
// any uncommitted work, and local branch. The issue goes back to the inbox for a
// manual start. Nothing on GitHub changes.
//
// The run's PR is resolved first, and any doubt about it stops the reset before
// anything is deleted. The record is then replaced by one that holds only that PR in
// Reset, so a failure while deleting files can never lose the PR link. That record
// also names the files still to remove and marks the removal as running, so a start
// waits instead of making a worktree the removal would delete.
func (a App) Reset(ctx context.Context, rec *task.Record) (*task.Record, error) {
	cur, err := a.Store.Get(ctx, rec.Key)
	if err != nil {
		return nil, err
	}
	if err := a.resettable(cur); err != nil {
		return nil, err
	}
	d := *cur.Delegation
	mark, err := a.resetPR(ctx, cur)
	if err != nil {
		return nil, fmt.Errorf("nothing was deleted: %w", err)
	}
	removal := WorktreeRemoval{Path: d.Worktree, Branch: d.Branch, DeleteBranch: d.Branch != "", Force: true}
	if _, err := os.Stat(d.Worktree); d.Worktree == "" || errors.Is(err, fs.ErrNotExist) {
		// Cleanup can remove the worktree and keep the branch, which only a checkout
		// of the repository can then find.
		req := StartRequest{Repo: cur.Repo}
		if _, err := a.sourceRepo(ctx, &req); err != nil {
			return nil, fmt.Errorf("nothing was deleted: %w", err)
		}
		removal.RepoDir = req.Dir
	}

	mark.At = a.Now()
	mark.Cleanup = &task.ResetCleanup{Worktree: d.Worktree, Branch: removal.Branch, RepoDir: removal.RepoDir, RunnerLog: d.RunnerLog, Since: mark.At, Owner: resetOwner}
	stub, err := a.update(ctx, cur.Key, func(cur *task.Record) (*task.Record, error) {
		// The task can change while GitHub is read; a run started since owns it.
		if err := a.resettable(cur); err != nil {
			return nil, err
		}
		if cur.Delegation.Branch != d.Branch || cur.Delegation.Worktree != d.Worktree {
			return nil, fmt.Errorf("%s changed while it was being reset; try again", taskRef(cur))
		}
		return &task.Record{
			Key: cur.Key, Identifier: cur.Identifier, Kind: cur.Kind, Title: cur.Title, URL: cur.URL,
			State:  task.Discovered,
			GitHub: cur.GitHub, LastGitHubSync: cur.LastGitHubSync,
			Reset: &mark,
			Body:  fmt.Sprintf("# %s %s\n\n## Notes\n\n", issueLabel(cur.Key, cur.Identifier), cur.Title),
		}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("nothing was deleted: %w", err)
	}

	left := a.cleanUp(ctx, *mark.Cleanup)
	// Finishing the removal lets a start through. What could not be removed stays
	// listed, and the next start removes it.
	done, err := a.update(ctx, cur.Key, func(cur *task.Record) (*task.Record, error) {
		if cur == nil || cur.Reset == nil || cur.Reset.Cleanup == nil || !cur.Reset.At.Equal(mark.At) {
			return nil, nil
		}
		if len(left) == 0 {
			cur.Reset.Cleanup = nil
		} else {
			cur.Reset.Cleanup.Since, cur.Reset.Cleanup.Owner = time.Time{}, ""
		}
		return cur, nil
	})
	if err != nil {
		return stub, fmt.Errorf("%s was reset, but its cleanup could not be recorded: %w", taskRef(cur), err)
	}
	if done != nil {
		stub = done
	}
	if len(left) > 0 {
		return stub, fmt.Errorf("%s was reset, but could not remove %s; the next start tries again", taskRef(cur), strings.Join(left, "; "))
	}
	return stub, nil
}

// cleanUp removes a reset run's worktree with its uncommitted work, its local branch,
// and its runner log. It returns what it could not remove, with the reason.
func (a App) cleanUp(ctx context.Context, c task.ResetCleanup) []string {
	var left []string
	if c.Worktree != "" || c.Branch != "" {
		removal := WorktreeRemoval{Path: c.Worktree, Branch: c.Branch, DeleteBranch: c.Branch != "", Force: true, RepoDir: c.RepoDir}
		if err := a.Worktrees.Remove(ctx, removal); err != nil {
			left = append(left, fmt.Sprintf("worktree %s and branch %s (%v)", c.Worktree, c.Branch, err))
		}
	}
	if c.RunnerLog != "" {
		if err := os.Remove(c.RunnerLog); err != nil && !errors.Is(err, fs.ErrNotExist) {
			left = append(left, fmt.Sprintf("runner log %s (%v)", c.RunnerLog, err))
		}
	}
	return left
}

// resetCleanupLimit is how long a start waits on a reset's removal, or on another
// start of the reset issue. One that runs longer belongs to a process that died.
const resetCleanupLimit = 10 * time.Minute

// resetOwner owns the removal App.Reset runs. A start owns it under a random token.
const resetOwner = "reset"

// resetting reports a reset issue whose old run is being removed, by the reset or by
// a start that claimed it.
func (a App) resetting(rec *task.Record) bool {
	if rec == nil || rec.Reset == nil || rec.Reset.Cleanup == nil || rec.Reset.Cleanup.Since.IsZero() {
		return false
	}
	return a.Now().Sub(rec.Reset.Cleanup.Since) < resetCleanupLimit
}

// resettable allows a reset only for a FAILED run whose runner has exited, because a
// live runner still owns the worktree and the task file.
func (a App) resettable(rec *task.Record) error {
	if rec == nil || rec.Delegation == nil {
		return errors.New("the task has no run to reset")
	}
	if rec.State != task.Failed {
		return fmt.Errorf("%s is %s; only a FAILED task can be reset", taskRef(rec), rec.State)
	}
	if a.liveRunner(rec) {
		return fmt.Errorf("%s is still running (pid %d); reset it once the runner exits", taskRef(rec), rec.Delegation.RunnerPID)
	}
	return nil
}

// resetPR finds the run's PR on GitHub. A stored PR must be on the task's branch in
// the same repository. Without one, the branch may still have a PR the runner opened
// before it died, so every PR on the branch is listed and more than one is ambiguous.
func (a App) resetPR(ctx context.Context, rec *task.Record) (task.ResetMark, error) {
	d := rec.Delegation
	if d.PR != 0 {
		pr, err := a.GitHub.GetPullRequest(ctx, task.Key{Repo: rec.Repo, Number: d.PR})
		if err != nil {
			return task.ResetMark{}, fmt.Errorf("could not read PR #%d from GitHub: %w", d.PR, err)
		}
		if pr.IsCrossRepository || pr.HeadRefName != d.Branch {
			return task.ResetMark{}, fmt.Errorf("PR #%d is on branch %s, not the task's branch %s", d.PR, pr.HeadRefName, d.Branch)
		}
		return markFor(pr), nil
	}
	if d.Branch == "" {
		return task.ResetMark{}, nil
	}
	prs, err := a.GitHub.PullRequestsForBranch(ctx, rec.Repo, d.Branch)
	if err != nil {
		return task.ResetMark{}, fmt.Errorf("could not list the PRs on %s from GitHub: %w", d.Branch, err)
	}
	switch len(prs) {
	case 0:
		return task.ResetMark{}, nil
	case 1:
		return markFor(prs[0]), nil
	}
	nums := make([]string, len(prs))
	for i, pr := range prs {
		nums[i] = fmt.Sprintf("#%d", pr.Key.Number)
	}
	return task.ResetMark{}, fmt.Errorf("PRs %s share branch %s, so the run's PR is unclear", strings.Join(nums, ", "), d.Branch)
}

// priorPR reads the PR a reset kept, or returns nil when the record has none. The
// next run continues it only while it is open on the branch the reset recorded.
func (a App) priorPR(ctx context.Context, rec *task.Record) (*PullRequest, bool, error) {
	if rec == nil || rec.Delegation != nil || rec.Reset == nil || rec.Reset.PR == 0 {
		return nil, false, nil
	}
	pr, err := a.GitHub.GetPullRequest(ctx, task.Key{Repo: rec.Repo, Number: rec.Reset.PR})
	if err != nil {
		return nil, false, fmt.Errorf("read PR #%d of the reset run: %w", rec.Reset.PR, err)
	}
	open := pr.State == "open" && !pr.IsCrossRepository && pr.HeadRefName == rec.Reset.Branch
	return &pr, open, nil
}

// priorBranch refuses a closed or merged prior PR's branch for the new run. The
// branch still holds the old commits on GitHub, so a fresh branch of that name could
// not be pushed, and pushing to it would reopen the old work.
func priorBranch(prior *PullRequest, branch string) error {
	if prior == nil || prior.HeadRefName != branch {
		return nil
	}
	return fmt.Errorf("%s is the branch of %s PR #%d from the reset run; choose another name", branch, prior.State, prior.Key.Number)
}

// priorBrief reads the reset run's PR and its comments as they are now, for a fresh
// session's prompt.
func (a App) priorBrief(ctx context.Context, rec *task.Record) (*agent.PriorPR, error) {
	d := rec.Delegation
	if d.PriorPR == 0 {
		return nil, nil
	}
	key := task.Key{Repo: rec.Repo, Number: d.PriorPR}
	pr, err := a.GitHub.GetPullRequest(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("read prior PR #%d: %w", d.PriorPR, err)
	}
	comments, err := a.GitHub.PullRequestComments(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("read comments on prior PR #%d: %w", d.PriorPR, err)
	}
	return &agent.PriorPR{
		Number: d.PriorPR, URL: pr.URL, Title: pr.Title, State: pr.State, Body: pr.Body,
		Comments: feedbackItems(comments), Continue: d.PR == d.PriorPR,
	}, nil
}

func markFor(pr PullRequest) task.ResetMark {
	return task.ResetMark{PR: pr.Key.Number, PRURL: pr.URL, Branch: pr.HeadRefName, PRState: pr.State}
}

func taskRef(rec *task.Record) string {
	if rec.Source == task.Linear {
		return rec.Identifier
	}
	return fmt.Sprintf("%s#%d", rec.Repo, rec.Number)
}

// claimReset makes this start the only one working on a reset issue until it saves
// its run or gives up, because finishReset force-removes the path the new worktree
// takes. It returns the claimed record and its owner token.
func (a App) claimReset(ctx context.Context, read *task.Record) (*task.Record, string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, "", err
	}
	owner := hex.EncodeToString(b[:])
	claimed, err := a.update(ctx, read.Key, func(cur *task.Record) (*task.Record, error) {
		if err := a.startable(cur); err != nil {
			return nil, err
		}
		if cur == nil || cur.Delegation != nil || cur.Reset == nil || !cur.Reset.At.Equal(read.Reset.At) {
			return nil, fmt.Errorf("%s changed while it was starting; start it again", taskRef(read))
		}
		c := task.ResetCleanup{}
		if cur.Reset.Cleanup != nil {
			c = *cur.Reset.Cleanup
		}
		c.Since, c.Owner = a.Now(), owner
		cur.Reset.Cleanup = &c
		return cur, nil
	})
	return claimed, owner, err
}

// releaseReset lets the next start through after this one gave up. What is left of
// the old run stays listed for it.
func (a App) releaseReset(ctx context.Context, key task.Key, owner string) {
	_, _ = a.update(ctx, key, func(cur *task.Record) (*task.Record, error) {
		if cur == nil || cur.Reset == nil || cur.Reset.Cleanup == nil || cur.Reset.Cleanup.Owner != owner {
			return nil, nil
		}
		cur.Reset.Cleanup.Since, cur.Reset.Cleanup.Owner = time.Time{}, ""
		return cur, nil
	})
}
