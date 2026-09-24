package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
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

	return a.launch(ctx, key)
}

// launch spawns the runner and records its log, or marks the task FAILED if it can't.
func (a App) launch(ctx context.Context, key task.Key) (*task.Record, error) {
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
	var pr *PullRequest
	if runErr == nil && turn.Outcome.Status == agent.PROpened {
		if pr, runErr = a.verifyPR(ctx, rec, turn.Outcome); runErr == nil {
			fmt.Fprintf(log, "foreman: verified PR #%d %s\n", pr.Key.Number, pr.URL)
		}
	}
	_, err = a.Store.Update(context.WithoutCancel(ctx), key, func(cur *task.Record) (*task.Record, error) {
		applyTurn(cur, turn, pr, runErr, a.Now())
		return cur, nil
	})
	return err
}

// verifyPR checks the agent's pr_opened claim against GitHub: the PR must be open,
// in this repo, from the task branch into the task's base. Without a number, the
// open PR for the branch is looked up.
func (a App) verifyPR(ctx context.Context, rec *task.Record, o agent.Outcome) (*PullRequest, error) {
	d := rec.Delegation
	var pr PullRequest
	if o.PRNumber > 0 {
		got, err := a.GitHub.GetPullRequest(ctx, task.Key{Repo: rec.Repo, Number: o.PRNumber})
		if err != nil {
			return nil, fmt.Errorf("agent reported PR #%d, but it could not be loaded: %w", o.PRNumber, err)
		}
		pr = got
	} else {
		got, err := a.GitHub.OpenPullRequestForBranch(ctx, rec.Repo, d.Branch)
		if err != nil {
			return nil, err
		}
		if got == nil {
			return nil, fmt.Errorf("agent reported a PR, but no open PR has head %s", d.Branch)
		}
		pr = *got
	}
	base := strings.TrimPrefix(d.BaseRef, "origin/")
	switch {
	case pr.State != "open":
		return nil, fmt.Errorf("PR #%d is %s, not open", pr.Key.Number, pr.State)
	case pr.IsCrossRepository:
		return nil, fmt.Errorf("PR #%d comes from a fork, not %s", pr.Key.Number, rec.Repo)
	case pr.HeadRefName != d.Branch:
		return nil, fmt.Errorf("PR #%d is from %s, not the task branch %s", pr.Key.Number, pr.HeadRefName, d.Branch)
	case pr.BaseRefName != base:
		return nil, fmt.Errorf("PR #%d targets %s, not %s", pr.Key.Number, pr.BaseRefName, base)
	}
	return &pr, nil
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
	in := agent.StartInput{Worktree: d.Worktree, Log: log}
	if dec := rec.Undelivered(); dec != nil && d.Implementer.ID != "" {
		fmt.Fprintf(log, "foreman: resuming %s session %s with the decision\n", adapter.Name(), d.Implementer.ID)
		in.Prompt = agent.ResumeMessage(*dec)
		turn, err := adapter.Resume(ctx, d.Implementer, in)
		if !errors.Is(err, agent.ErrNoSession) {
			return turn, err
		}
		fmt.Fprintf(log, "foreman: session is gone (%v); starting a fresh turn rebuilt from the task file\n", err)
	}
	in.Prompt = agent.ImplementationPrompt(agent.TaskBrief{
		Key:        rec.Key,
		Title:      issue.Title,
		URL:        issue.URL,
		IssueBody:  issue.Body,
		Context:    string(contextText),
		Branch:     d.Branch,
		BaseBranch: d.BaseRef[len("origin/"):],
		Worktree:   d.Worktree,
		Decisions:  rec.Decisions,
	})
	fmt.Fprintf(log, "foreman: starting %s in %s\n", adapter.Name(), d.Worktree)
	return adapter.Start(ctx, in, func(s task.Session) {
		_, _ = a.Store.Update(ctx, rec.Key, func(cur *task.Record) (*task.Record, error) {
			cur.Delegation.Implementer = s
			return cur, nil
		})
	})
}

// applyTurn maps a finished turn onto the task. A completed turn has received every
// answered decision. pr is the verified PR for pr_opened; Stage 5 reviews it.
func applyTurn(rec *task.Record, turn agent.Turn, pr *PullRequest, runErr error, now time.Time) {
	rec.Delegation.RunnerPID = 0
	if turn.Session.ID != "" {
		rec.Delegation.Implementer = turn.Session
	}
	if runErr != nil {
		rec.State = task.Failed
		rec.LastOutcome = &task.Outcome{Status: "failed", Summary: runErr.Error(), At: now}
		return
	}
	for i := range rec.Decisions {
		if d := &rec.Decisions[i]; d.Answer != "" && d.DeliveredAt.IsZero() {
			d.DeliveredAt = now
		}
	}
	o := turn.Outcome
	rec.LastOutcome = &task.Outcome{Status: string(o.Status), Summary: o.Summary, At: now}
	switch o.Status {
	case agent.PROpened:
		rec.State = task.Reviewing
		rec.Delegation.PR, rec.Delegation.PRURL = pr.Key.Number, pr.URL
		rec.Validation = o.Validation
	case agent.Blocked:
		rec.State = task.NeedsDecision
		rec.Decisions = append(rec.Decisions, task.Decision{AskedAt: now, Question: *o.Question})
	case agent.FailedRun:
		rec.State = task.Failed
	}
}
