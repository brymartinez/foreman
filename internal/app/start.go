package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

type StartRequest struct {
	Key         task.Key  // source-qualified inbox identity; legacy callers use Repo and Issue
	Repo        task.Repo // optional with Dir; required when starting from a managed checkout
	Issue       int
	Agent       task.Agent
	ContextFile string // absolute path, or ""
	BranchName  string // confirmed name for a new task; existing tasks keep their branch
	// BranchType overrides the type inferred from the issue title and labels. It only
	// applies to a task's first start; a stored branch is always reused.
	BranchType task.BranchType
	Dir        string // explicit source checkout; empty uses the managed repository checkout
	// NewOnly refuses an issue foreman has delegated before, in any state. The
	// dashboard's auto mode sets it, because its inbox can be a refresh old.
	NewOnly bool
	// Profile is the routed profile, with RouteReason saying why it was chosen. Agent
	// may be left empty; it then comes from the profile. A start without a profile
	// keeps the task's stored profile when the agent is the same.
	Profile     *task.Profile
	RouteReason string
	// managed is set when Dir is foreman's managed checkout rather than one the
	// caller supplied. Only a managed checkout is synced before a proposal.
	managed bool
}

// ErrDelegated is a NewOnly start for an issue that already has a delegation.
var ErrDelegated = errors.New("already delegated")

// Start prepares the worktree and task file, then launches the detached runner.
// Rerunning it for a FAILED task, or one whose runner died, resumes from durable state.
func (a App) Start(ctx context.Context, req StartRequest) (*task.Record, error) {
	info, err := a.sourceRepo(ctx, &req)
	if err != nil {
		return nil, err
	}
	key := req.Key
	if key == (task.Key{}) {
		key = task.Key{Repo: info.Repo, Number: req.Issue}
	}
	if key.Repo != info.Repo {
		return nil, fmt.Errorf("issue belongs to %s, not %s", key.Repo, info.Repo)
	}
	req.Repo, req.Key = info.Repo, key
	existing, err := a.Store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if err := a.startable(existing); err != nil {
		return nil, err
	}
	if err := newOnly(req, existing); err != nil {
		return nil, err
	}
	if p := req.Profile; p != nil {
		if req.Agent == "" {
			req.Agent = p.Agent
		}
		if req.Agent != p.Agent {
			return nil, fmt.Errorf("profile %s runs %s, not %s", p.Name, p.Agent, req.Agent)
		}
	}
	if _, ok := a.Agents[req.Agent]; !ok {
		return nil, fmt.Errorf("agent %s is not configured", req.Agent)
	}
	if req.ContextFile != "" {
		if _, err := os.Stat(req.ContextFile); err != nil {
			return nil, fmt.Errorf("context file: %w", err)
		}
	}
	issue, err := a.issue(ctx, key)
	if err != nil {
		return nil, err
	}
	if issue.State != "open" {
		return nil, fmt.Errorf("%s is %s", issueLabel(key, issue.Identifier), issue.State)
	}
	if req.NewOnly && !slices.Contains(issue.Labels, "triaged") {
		return nil, fmt.Errorf("%s no longer has the triaged label", issueLabel(key, issue.Identifier))
	}

	prior, continuePR, err := a.priorPR(ctx, existing)
	if err != nil {
		return nil, err
	}
	branch := ""
	if existing != nil && existing.Delegation != nil && existing.Delegation.Branch != "" {
		branch = existing.Delegation.Branch
	} else if continuePR {
		branch = prior.HeadRefName
	} else {
		branch = req.BranchName
		// A confirmed name skips the proposal, but the checkout and its guidance can
		// change while a person edits it, so the same checks still have to pass.
		if branch != "" {
			if err := a.refreshGuidance(ctx, req, info.DefaultBranch); err != nil {
				return nil, err
			}
			if _, err := readBranchGuide(req.Dir); err != nil {
				return nil, err
			}
		}
		if branch == "" {
			proposal, err := a.proposeBranch(ctx, req, info.DefaultBranch, issue)
			if err != nil {
				return nil, err
			}
			if proposal.Uncertain {
				return nil, fmt.Errorf("branch needs a choice: %s", proposal.Reason)
			}
			branch = proposal.Name
		}
		if !task.ValidBranchName(branch) {
			return nil, fmt.Errorf("invalid branch name %q", branch)
		}
		if err := priorBranch(prior, branch); err != nil {
			return nil, err
		}
	}
	path := task.WorktreePath(a.WorktreesDir, key.Repo, branch)
	if existing != nil && existing.Delegation != nil && existing.Delegation.Worktree != "" {
		path = existing.Delegation.Worktree
	}
	wt := WorktreeRequest{
		RepoDir: req.Dir,
		Path:    path,
		Branch:  branch,
		BaseRef: "origin/" + info.DefaultBranch,
	}
	base := wt.BaseRef
	if continuePR {
		// The worktree starts at the PR's head; the PR keeps the base it targets.
		wt.BaseRef, base = "origin/"+branch, "origin/"+prior.BaseRefName
	}
	// owner is this start's claim on a reset issue; it holds other starts off until
	// the run is saved, or is released when this start gives up first.
	owner, saved := "", false
	if existing != nil && existing.Delegation == nil && existing.Reset != nil {
		claimed, token, err := a.claimReset(ctx, existing)
		if err != nil {
			return nil, err
		}
		owner, existing = token, claimed
		defer func() {
			if !saved {
				a.releaseReset(ctx, key, owner)
			}
		}()
	}
	if err := a.finishReset(ctx, existing, wt, continuePR); err != nil {
		return nil, err
	}
	if err := a.Worktrees.Ensure(ctx, wt); err != nil {
		return nil, err
	}
	head, err := a.Worktrees.Head(ctx, wt.Path)
	if err != nil {
		return nil, fmt.Errorf("read worktree head: %w", err)
	}

	now := a.Now()
	_, err = a.update(ctx, key, func(cur *task.Record) (*task.Record, error) {
		if cur == nil {
			cur = &task.Record{Key: key, State: task.Discovered, Body: fmt.Sprintf("# %s %s\n\n## Notes\n\n", issueLabel(key, issue.Identifier), issue.Title)}
		}
		if err := a.startableBy(cur, owner); err != nil {
			return nil, err
		}
		// A reset since the record was read removes the worktree this start prepared,
		// and only the start that claimed a reset issue may save its run.
		if cur.Reset != nil && (owner == "" || cur.Reset.Cleanup == nil || cur.Reset.Cleanup.Owner != owner) {
			return nil, fmt.Errorf("%s was reset while it started; start it again", taskRef(cur))
		}
		// Checked again under the task lock: another command may have started the
		// issue while this one prepared the worktree.
		if err := newOnly(req, cur); err != nil {
			return nil, err
		}
		was := cur.State
		cur.Kind, cur.Title, cur.URL, cur.State = task.Issue, issue.Title, issue.URL, task.Working
		cur.Identifier = issue.Identifier
		if cur.GitHub == nil {
			cur.GitHub = &task.Snapshot{UpdatedAt: issue.UpdatedAt, State: issue.State}
			cur.LastGitHubSync = now
		}
		prev := cur.Delegation
		cur.Delegation = &task.Delegation{
			Implementer:    task.Session{Agent: req.Agent},
			StartedAt:      now,
			Reviewer:       req.Agent.Reviewer(),
			Branch:         wt.Branch,
			BaseRef:        base,
			Worktree:       wt.Path,
			ContextFile:    req.ContextFile,
			Profile:        req.Profile,
			RouteReason:    req.RouteReason,
			CheckpointHead: head,
		}
		// The pin belongs to the task, not the agent, so every restart keeps it.
		if prev != nil {
			cur.Delegation.Account, cur.Delegation.AccountEmail = prev.Account, prev.AccountEmail
			cur.Delegation.PriorPR = prev.PriorPR
		}
		reset := cur.Reset != nil
		if reset {
			cur.Delegation.PriorPR = cur.Reset.PR
			if continuePR {
				cur.Delegation.PR, cur.Delegation.PRURL = prior.Key.Number, prior.URL
			}
			cur.Reset = nil
		}
		// The same branch has the same PR, so a restart can pick up at its review.
		// Another implementer starts over: its reviewer wrote the existing PR. A
		// PR_CLOSED task's PR cannot be picked up, and keeping its number would have
		// the readiness gate end this run on its next refresh. When feedback replies
		// are enabled, waiting comments keep the PR for any agent.
		sameAgent := prev != nil && prev.Implementer.Agent == req.Agent
		if sameAgent && prev.Reviewer == req.Agent {
			cur.Delegation.Reviewer = prev.Reviewer
		}
		if sameAgent && prev.Branch == wt.Branch && prev.CheckpointHead == head {
			cur.Delegation.CheckpointNext = prev.CheckpointNext
			cur.Delegation.Checkpoints = prev.Checkpoints
		}
		// A retry with the same agent keeps its route. Choosing another agent drops it.
		if req.Profile == nil && sameAgent && prev.Profile != nil {
			cur.Delegation.Profile, cur.Delegation.RouteReason, cur.Delegation.RouteLogged = prev.Profile, prev.RouteReason, prev.RouteLogged
		}
		if was != task.PRClosed && prev != nil && prev.Branch == wt.Branch && (sameAgent || (a.ReplyToPRFeedback && len(cur.UnhandledFeedback()) > 0)) {
			cur.Delegation.PR, cur.Delegation.PRURL, cur.Delegation.PRHead = prev.PR, prev.PRURL, prev.PRHead
			// The same PR keeps its conflict repair: a spent one is not repeated, and one a
			// dead runner left pending is finished by this run.
			cur.Delegation.ConflictAttempts, cur.Delegation.ConflictPending = prev.ConflictAttempts, prev.ConflictPending
			// A kept PR keeps the base it targets, which a retarget may have changed.
			if prev.BaseRef != "" {
				cur.Delegation.BaseRef = prev.BaseRef
			}
		}
		// A kept PR continues its attempt, or stays with the older record when it
		// predates attempts. So does an attempt that has no PR yet: the earlier runs
		// were work toward the PR this one opens. Anything else is a new attempt.
		// A reset discarded the attempts and the log, so the run starts the first one,
		// even when it continues a PR.
		switch at := cur.CurrentAttempt(); {
		case reset:
			cur.Attempts = []task.Attempt{{Agent: req.Agent}}
		case cur.Delegation.PR != 0:
		case at != nil && at.PR == 0:
			at.Agent = req.Agent
		default:
			cur.Attempts = append(cur.Attempts, task.Attempt{Agent: req.Agent, LogStart: logSize(prev)})
		}
		summary := fmt.Sprintf("dispatched to %s", req.Agent)
		if p := cur.Delegation.Profile; p != nil {
			summary = fmt.Sprintf("dispatched to %s with profile %s", req.Agent, p.Name)
		}
		cur.LastOutcome = &task.Outcome{Status: "started", Summary: summary, At: now}
		return cur, nil
	})
	if err != nil {
		return nil, err
	}
	saved = true

	return a.launch(ctx, key)
}

func (a App) sourceRepo(ctx context.Context, req *StartRequest) (RepoInfo, error) {
	if req.Dir != "" {
		info, err := a.GitHub.RepoAt(ctx, req.Dir)
		if err != nil {
			return RepoInfo{}, fmt.Errorf("read the repository of the checkout at %s: %w", req.Dir, err)
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
	req.Dir, req.managed = path, true
	return info, nil
}

// launch spawns the runner and records its log, or marks the task FAILED if it can't.
func (a App) launch(ctx context.Context, key task.Key) (*task.Record, error) {
	var pid int
	var logPath string
	spawnErr := a.restoreWorktree(ctx, key)
	if spawnErr == nil {
		spawnErr = a.launchAccount(ctx, key)
	}
	if spawnErr == nil {
		pid, logPath, spawnErr = a.Runners.Spawn(key)
	}
	return a.update(ctx, key, func(cur *task.Record) (*task.Record, error) {
		if spawnErr != nil && cur.Delegation.ConflictPending {
			failRepair(cur, "could not launch runner: "+spawnErr.Error(), a.Now())
			return cur, nil
		}
		if spawnErr != nil {
			failTask(cur, "could not launch runner: "+spawnErr.Error(), a.Now())
			return cur, nil
		}
		cur.Delegation.RunnerLog = logPath
		if cur.State == task.Working && cur.Delegation.RunnerPID == 0 {
			cur.Delegation.RunnerPID = pid
		}
		return cur, nil
	})
}

// restoreWorktree recreates a worktree that cleanup removed. Cleanup removes a
// PR_CLOSED task's worktree, and reopening the PR can hand the task back to a runner.
// The local branch was kept, so the worktree comes back on the same commits. It is
// looked up in the managed checkout without a base ref: a task started from another
// checkout has its branch there, and creating a fresh one would drop its commits.
func (a App) restoreWorktree(ctx context.Context, key task.Key) error {
	rec, err := a.Store.Get(ctx, key)
	if err != nil || rec == nil || rec.Delegation == nil || rec.Delegation.WorktreeRemovedAt.IsZero() {
		return err
	}
	d := rec.Delegation
	req := StartRequest{Repo: key.Repo}
	if _, err := a.sourceRepo(ctx, &req); err != nil {
		return fmt.Errorf("restore worktree: %w", err)
	}
	if err := a.Worktrees.Ensure(ctx, WorktreeRequest{RepoDir: req.Dir, Path: d.Worktree, Branch: d.Branch}); err != nil {
		return fmt.Errorf("restore worktree: %w", err)
	}
	_, err = a.update(ctx, key, func(cur *task.Record) (*task.Record, error) {
		if cur.Delegation != nil && cur.Delegation.Worktree == d.Worktree {
			cur.Delegation.WorktreeRemovedAt = time.Time{}
		}
		return cur, nil
	})
	return err
}

// launchAccount checks the task's account before a runner starts, so a mismatch
// fails the task without spending a runner on it.
func (a App) launchAccount(ctx context.Context, key task.Key) error {
	rec, err := a.Store.Get(ctx, key)
	if err != nil || rec == nil || rec.Delegation == nil {
		return err
	}
	return a.checkAccount(ctx, rec)
}

// logSize is where the next attempt's lines start in the task log. The runner has not
// been spawned yet, so nothing else is writing it.
func logSize(prev *task.Delegation) int64 {
	if prev == nil || prev.RunnerLog == "" {
		return 0
	}
	info, err := os.Stat(prev.RunnerLog)
	if err != nil {
		return 0
	}
	return info.Size()
}

func newOnly(req StartRequest, rec *task.Record) error {
	if req.NewOnly && rec != nil && rec.Reset != nil {
		return fmt.Errorf("%s was reset and waits for a manual start: %w", taskRef(rec), ErrDelegated)
	}
	if req.NewOnly && rec != nil && rec.Delegation != nil {
		return fmt.Errorf("%s#%d is %s: %w", rec.Repo, rec.Number, rec.State, ErrDelegated)
	}
	return nil
}

func (a App) startable(rec *task.Record) error { return a.startableBy(rec, "") }

// startableBy is startable for the start that claimed a reset issue as owner.
func (a App) startableBy(rec *task.Record, owner string) error {
	if rec == nil {
		return nil
	}
	ref := fmt.Sprintf("%s#%d", rec.Repo, rec.Number)
	if a.resetting(rec) && (owner == "" || rec.Reset.Cleanup.Owner != owner) {
		if rec.Reset.Cleanup.Owner == resetOwner {
			return fmt.Errorf("%s is being reset; start it once the reset finishes", ref)
		}
		return fmt.Errorf("%s is being started by another command", ref)
	}
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
		return fmt.Errorf("%s is waiting for your decision; answer it from the dashboard's delegated work", ref)
	case task.Reviewed:
		// Starting would rebuild the delegation and lose the session that wrote the PR
		// the comments are on, so the task continues instead.
		return fmt.Errorf("%s has PR feedback to answer; select it in the inbox to resume it", ref)
	}
	return fmt.Errorf("%s is %s", ref, rec.State)
}

// finishReset removes what a reset left of the old run before a start makes its
// worktree. A continued PR's worktree goes at the old run's path, where a start that
// raced the reset can also have left one, so that path is cleared too. Nothing else
// owns either: the record has no delegation, and a running removal holds starts off.
func (a App) finishReset(ctx context.Context, rec *task.Record, wt WorktreeRequest, continuePR bool) error {
	if rec == nil || rec.Delegation != nil || rec.Reset == nil {
		return nil
	}
	if c := rec.Reset.Cleanup; c != nil {
		left := *c
		if left.RepoDir == "" {
			// The worktree had the branch when the reset ran; without it, only a
			// checkout of the repository finds the branch.
			left.RepoDir = wt.RepoDir
		}
		if left := a.cleanUp(ctx, left); len(left) > 0 {
			return fmt.Errorf("finish the reset: could not remove %s", strings.Join(left, "; "))
		}
	}
	if !continuePR {
		return nil
	}
	if err := a.Worktrees.Remove(ctx, WorktreeRemoval{Path: wt.Path, Branch: wt.Branch, DeleteBranch: true, Force: true, RepoDir: wt.RepoDir}); err != nil {
		return fmt.Errorf("clear %s for PR #%d: %w", wt.Path, rec.Reset.PR, err)
	}
	return nil
}
