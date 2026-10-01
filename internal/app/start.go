package app

import (
	"context"
	"fmt"
	"os"

	"github.com/brymartinez/foreman/internal/task"
)

type StartRequest struct {
	Repo        task.Repo // optional with Dir; required when starting from a managed checkout
	Issue       int
	Agent       task.Agent
	ContextFile string // absolute path, or ""
	// BranchType overrides the type inferred from the issue title and labels. It only
	// applies to a task's first start; a stored branch is always reused.
	BranchType task.BranchType
	Dir        string // explicit source checkout; empty uses the managed repository checkout
}

// Start prepares the worktree and task file, then launches the detached runner.
// Rerunning it for a FAILED task, or one whose runner died, resumes from durable state.
func (a App) Start(ctx context.Context, req StartRequest) (*task.Record, error) {
	info, err := a.sourceRepo(ctx, &req)
	if err != nil {
		return nil, err
	}
	key := task.Key{Repo: info.Repo, Number: req.Issue}
	existing, err := a.Store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if err := a.startable(existing); err != nil {
		return nil, err
	}
	if _, ok := a.Agents[req.Agent]; !ok {
		return nil, fmt.Errorf("agent %s is not configured", req.Agent)
	}
	if req.ContextFile != "" {
		if _, err := os.Stat(req.ContextFile); err != nil {
			return nil, fmt.Errorf("context file: %w", err)
		}
	}
	issue, err := a.GitHub.GetIssue(ctx, key)
	if err != nil {
		return nil, err
	}
	if issue.State != "open" {
		return nil, fmt.Errorf("%s#%d is %s", key.Repo, key.Number, issue.State)
	}

	branchType := req.BranchType
	if branchType == "" {
		branchType = task.BranchTypeFor(issue.Title, issue.Labels)
	}
	branch := task.BranchName(branchType, task.WorkName(key.Number, issue.Title))
	if existing != nil && existing.Delegation != nil && existing.Delegation.Branch != "" {
		branch = existing.Delegation.Branch
	}
	workName := task.WorkNameOf(branch)
	wt := WorktreeRequest{
		RepoDir: req.Dir,
		Path:    task.WorktreePath(a.WorktreesDir, key.Repo, workName),
		Branch:  branch,
		BaseRef: "origin/" + info.DefaultBranch,
	}
	if err := a.Worktrees.Ensure(ctx, wt); err != nil {
		return nil, err
	}

	now := a.Now()
	_, err = a.update(ctx, key, func(cur *task.Record) (*task.Record, error) {
		if cur == nil {
			cur = &task.Record{Key: key, State: task.Discovered, Body: fmt.Sprintf("# #%d %s\n\n## Notes\n\n", key.Number, issue.Title)}
		}
		if err := a.startable(cur); err != nil {
			return nil, err
		}
		was := cur.State
		cur.Kind, cur.Title, cur.URL, cur.State = task.Issue, issue.Title, issue.URL, task.Working
		if cur.GitHub == nil {
			cur.GitHub = &task.Snapshot{UpdatedAt: issue.UpdatedAt, State: issue.State}
			cur.LastGitHubSync = now
		}
		prev := cur.Delegation
		cur.Delegation = &task.Delegation{
			Implementer: task.Session{Agent: req.Agent},
			StartedAt:   now,
			Reviewer:    req.Agent.Reviewer(),
			Branch:      wt.Branch,
			BaseRef:     wt.BaseRef,
			Worktree:    wt.Path,
			ContextFile: req.ContextFile,
		}
		// The same branch has the same PR, so a restart can pick up at its review (D63).
		// Another implementer starts over: its reviewer wrote the existing PR. A
		// PR_CLOSED task's PR cannot be picked up, and keeping its number would have
		// the readiness gate end this run on its next refresh (D107). Comments still
		// waiting on the PR keep it for any agent, because the run reads them through
		// that number and the new agent has to answer them first (D126).
		sameAgent := prev != nil && prev.Implementer.Agent == req.Agent
		if was != task.PRClosed && prev != nil && prev.Branch == wt.Branch && (sameAgent || len(cur.UnhandledFeedback()) > 0) {
			cur.Delegation.PR, cur.Delegation.PRURL, cur.Delegation.PRHead = prev.PR, prev.PRURL, prev.PRHead
			// A kept PR keeps the base it targets, which a retarget may have changed (D130).
			if prev.BaseRef != "" {
				cur.Delegation.BaseRef = prev.BaseRef
			}
		}
		cur.LastOutcome = &task.Outcome{Status: "started", Summary: fmt.Sprintf("dispatched to %s", req.Agent), At: now}
		return cur, nil
	})
	if err != nil {
		return nil, err
	}

	return a.launch(ctx, key)
}

func (a App) sourceRepo(ctx context.Context, req *StartRequest) (RepoInfo, error) {
	if req.Dir != "" {
		info, err := a.GitHub.RepoAt(ctx, req.Dir)
		if err != nil {
			return RepoInfo{}, fmt.Errorf("run foreman start from a checkout of the issue's repository: %w", err)
		}
		if req.Repo != "" && req.Repo != info.Repo {
			return RepoInfo{}, fmt.Errorf("this checkout is %s, not %s", info.Repo, req.Repo)
		}
		return info, nil
	}
	if req.Repo == "" {
		return RepoInfo{}, fmt.Errorf("repository is required when no checkout directory is provided")
	}
	if a.Repositories == nil {
		return RepoInfo{}, fmt.Errorf("managed repository checkout is not configured")
	}
	path := task.RepositoryPath(a.RepositoriesDir, req.Repo)
	info, err := a.Repositories.Ensure(ctx, req.Repo, path)
	if err != nil {
		return RepoInfo{}, fmt.Errorf("prepare checkout for %s: %w", req.Repo, err)
	}
	req.Dir = path
	return info, nil
}

// launch spawns the runner and records its log, or marks the task FAILED if it can't.
func (a App) launch(ctx context.Context, key task.Key) (*task.Record, error) {
	pid, logPath, spawnErr := a.Runners.Spawn(key)
	return a.update(ctx, key, func(cur *task.Record) (*task.Record, error) {
		if spawnErr != nil {
			cur.State = task.Failed
			cur.LastOutcome = &task.Outcome{Status: "failed", Summary: "could not launch runner: " + spawnErr.Error(), At: a.Now()}
			return cur, nil
		}
		cur.Delegation.RunnerLog = logPath
		if cur.State == task.Working && cur.Delegation.RunnerPID == 0 {
			cur.Delegation.RunnerPID = pid
		}
		return cur, nil
	})
}

func (a App) startable(rec *task.Record) error {
	if rec == nil {
		return nil
	}
	ref := fmt.Sprintf("%s#%d", rec.Repo, rec.Number)
	switch rec.State {
	case task.Discovered, task.Failed, task.PRClosed, task.Working, task.Reviewing:
		// A second runner would share the worktree and the task file with the first,
		// and the first stops being held off by its state the moment this start
		// changes it.
		if a.liveRunner(rec) {
			return fmt.Errorf("%s is already running (pid %d)", ref, rec.Delegation.RunnerPID)
		}
		return nil
	case task.NeedsDecision:
		return fmt.Errorf("%s is waiting for your decision; use foreman respond %d", ref, rec.Number)
	case task.Reviewed:
		// Starting would rebuild the delegation and lose the session that wrote the PR
		// the comments are on, so the task continues instead (D121).
		return fmt.Errorf("%s has PR feedback to answer; use foreman resume %d", ref, rec.Number)
	}
	return fmt.Errorf("%s is %s", ref, rec.State)
}
