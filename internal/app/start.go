package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

type StartRequest struct {
	Repo        task.Repo // optional; must match the checkout at Dir when set
	Issue       int
	Agent       task.Agent
	ContextFile string // absolute path, or ""
	Dir         string // the checkout foreman start was run from
}

// Start prepares the worktree and task file, then launches the detached runner.
// Rerunning it for a FAILED task, or one whose runner died, resumes from durable state.
func (a App) Start(ctx context.Context, req StartRequest) (*task.Record, error) {
	info, err := a.GitHub.RepoAt(ctx, req.Dir)
	if err != nil {
		return nil, fmt.Errorf("run foreman start from a checkout of the issue's repository: %w", err)
	}
	if req.Repo != "" && req.Repo != info.Repo {
		return nil, fmt.Errorf("this checkout is %s, not %s", info.Repo, req.Repo)
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

	branch := task.BranchName(task.WorkName(key.Number, issue.Title))
	if existing != nil && existing.Delegation != nil && existing.Delegation.Branch != "" {
		branch = existing.Delegation.Branch
	}
	workName := branch[len(task.BranchPrefix):]
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
	_, err = a.Store.Update(ctx, key, func(cur *task.Record) (*task.Record, error) {
		if cur == nil {
			cur = &task.Record{Key: key, State: task.Discovered, Body: fmt.Sprintf("# #%d %s\n\n## Notes\n\n", key.Number, issue.Title)}
		}
		if err := a.startable(cur); err != nil {
			return nil, err
		}
		cur.Kind, cur.Title, cur.URL, cur.State = task.Issue, issue.Title, issue.URL, task.Working
		if cur.GitHub == nil {
			cur.GitHub = &task.Snapshot{UpdatedAt: issue.UpdatedAt, State: issue.State}
			cur.LastGitHubSync = now
		}
		cur.Delegation = &task.Delegation{
			Implementer: task.Session{Agent: req.Agent},
			Reviewer:    req.Agent.Reviewer(),
			Branch:      wt.Branch,
			BaseRef:     wt.BaseRef,
			Worktree:    wt.Path,
			ContextFile: req.ContextFile,
		}
		cur.LastOutcome = &task.Outcome{Status: "started", Summary: fmt.Sprintf("dispatched to %s", req.Agent), At: now}
		return cur, nil
	})
	if err != nil {
		return nil, err
	}

	pid, logPath, spawnErr := a.Runners.Spawn(key)
	return a.Store.Update(ctx, key, func(cur *task.Record) (*task.Record, error) {
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
	case task.Discovered, task.Failed:
		return nil
	case task.Working, task.Reviewing:
		if rec.Delegation != nil && rec.Delegation.RunnerPID > 0 && a.Runners.Alive(rec.Delegation.RunnerPID) {
			return fmt.Errorf("%s is already running (pid %d)", ref, rec.Delegation.RunnerPID)
		}
		return nil
	case task.NeedsDecision:
		return fmt.Errorf("%s is waiting for your decision; use foreman respond %d", ref, rec.Number)
	}
	return fmt.Errorf("%s is %s", ref, rec.State)
}

// RunTask is the body of the detached runner: one implementation turn, then the
// outcome is written to the task file. It only returns an error it could not record.
func (a App) RunTask(ctx context.Context, key task.Key, pid int, log io.Writer) error {
	rec, err := a.Store.Update(ctx, key, func(cur *task.Record) (*task.Record, error) {
		if cur == nil || cur.Delegation == nil || cur.State != task.Working {
			return nil, errors.New("task is not WORKING; nothing to run")
		}
		cur.Delegation.RunnerPID = pid
		return cur, nil
	})
	if err != nil {
		return err
	}
	turn, runErr := a.implement(ctx, rec, log)
	_, err = a.Store.Update(context.WithoutCancel(ctx), key, func(cur *task.Record) (*task.Record, error) {
		applyTurn(cur, turn, runErr, a.Now())
		return cur, nil
	})
	return err
}

func (a App) implement(ctx context.Context, rec *task.Record, log io.Writer) (agent.Turn, error) {
	d := rec.Delegation
	adapter, ok := a.Agents[d.Implementer.Agent]
	if !ok {
		return agent.Turn{}, fmt.Errorf("agent %s is not configured", d.Implementer.Agent)
	}
	issue, err := a.GitHub.GetIssue(ctx, rec.Key)
	if err != nil {
		return agent.Turn{}, err
	}
	var contextText []byte
	if d.ContextFile != "" {
		if contextText, err = os.ReadFile(d.ContextFile); err != nil {
			return agent.Turn{}, fmt.Errorf("context file: %w", err)
		}
	}
	prompt := agent.ImplementationPrompt(agent.TaskBrief{
		Key:        rec.Key,
		Title:      issue.Title,
		URL:        issue.URL,
		IssueBody:  issue.Body,
		Context:    string(contextText),
		Branch:     d.Branch,
		BaseBranch: d.BaseRef[len("origin/"):],
		Worktree:   d.Worktree,
	})
	fmt.Fprintf(log, "foreman: starting %s in %s\n", adapter.Name(), d.Worktree)
	return adapter.Start(ctx, agent.StartInput{Worktree: d.Worktree, Prompt: prompt, Log: log}, func(s task.Session) {
		_, _ = a.Store.Update(ctx, rec.Key, func(cur *task.Record) (*task.Record, error) {
			cur.Delegation.Implementer = s
			return cur, nil
		})
	})
}

// applyTurn maps a finished turn onto the task. Stage 3 adds respond/resume for
// blocked, Stage 4 PR verification, Stage 5 the review that follows pr_opened.
func applyTurn(rec *task.Record, turn agent.Turn, runErr error, now time.Time) {
	rec.Delegation.RunnerPID = 0
	if turn.Session.ID != "" {
		rec.Delegation.Implementer = turn.Session
	}
	if runErr != nil {
		rec.State = task.Failed
		rec.LastOutcome = &task.Outcome{Status: "failed", Summary: runErr.Error(), At: now}
		return
	}
	o := turn.Outcome
	rec.LastOutcome = &task.Outcome{Status: string(o.Status), Summary: o.Summary, At: now}
	switch o.Status {
	case agent.PROpened:
		rec.State = task.Reviewing
		rec.Delegation.PR = o.PRNumber
	case agent.Blocked:
		rec.State = task.NeedsDecision
		rec.Decisions = append(rec.Decisions, task.Decision{AskedAt: now, Question: *o.Question})
	case agent.FailedRun:
		rec.State = task.Failed
	}
}
