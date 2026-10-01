// Package app orchestrates the task lifecycle. It depends only on these interfaces;
// gh, git, the Markdown store, the agent CLIs, and process spawning live behind them.
// Ports grow with the stage that first needs them (D17).
package app

import (
	"context"
	"errors"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

type TaskStore interface {
	Get(ctx context.Context, key task.Key) (*task.Record, error)
	Save(ctx context.Context, rec *task.Record) error
	List(ctx context.Context) ([]*task.Record, error)
	// Update re-reads the record under a lock, applies fn, and saves the result, so the
	// runner and inbox never overwrite each other. cur is nil when no file exists.
	Update(ctx context.Context, key task.Key, fn func(cur *task.Record) (*task.Record, error)) (*task.Record, error)
}

type InboxItem struct {
	Key       task.Key
	Kind      task.Kind
	Title     string
	URL       string
	State     string // open, closed, merged
	UpdatedAt time.Time
	Reasons   []task.InboxReason
}

type Issue struct {
	Key       task.Key
	Title     string
	Body      string
	Labels    []string
	URL       string
	State     string
	UpdatedAt time.Time
}

type PullRequest struct {
	Key               task.Key
	URL               string
	State             string // open, closed, merged
	HeadRefName       string
	BaseRefName       string
	HeadSHA           string // the commit at the PR's head
	IsCrossRepository bool
}

// PRComment is one comment on a pull request. Path, Line, and InReplyTo are set for
// an inline review comment, and are what tell the agent where to reply (D117).
type PRComment struct {
	Kind      task.CommentKind
	ID        int64
	Author    string
	Body      string
	URL       string
	UpdatedAt time.Time
	Path      string
	Line      int
	InReplyTo int64
}

type RepoInfo struct {
	Repo          task.Repo
	DefaultBranch string
}

// InboxSearch is one run of the inbox searches.
type InboxSearch struct {
	Items []InboxItem
	// Truncated describes each search that hit the result limit, so items past it are
	// missing and an item's absence says nothing about its reasons.
	Truncated []string
}

// ErrGone means GitHub reports an item deleted or out of reach (HTTP 404 or 410).
var ErrGone = errors.New("not found on GitHub, or no longer accessible")

type GitHubClient interface {
	InboxItems(ctx context.Context) (InboxSearch, error)
	// GetItem re-fetches a tracked item by key; it can drop out of search results once unassigned or closed.
	GetItem(ctx context.Context, key task.Key) (InboxItem, error)
	GetIssue(ctx context.Context, key task.Key) (Issue, error)
	// RepoAt identifies the GitHub repository checked out at dir.
	RepoAt(ctx context.Context, dir string) (RepoInfo, error)
	GetPullRequest(ctx context.Context, key task.Key) (PullRequest, error)
	// OpenPullRequestForBranch returns the open PR whose head is branch, or nil if none.
	OpenPullRequestForBranch(ctx context.Context, repo task.Repo, branch string) (*PullRequest, error)
	// CIStatus summarizes the checks on the PR's current head. CheckedAt is left zero.
	CIStatus(ctx context.Context, pr task.Key) (task.CIStatus, error)
	// PostIssueComment adds a comment to the issue, or edits comment id when id > 0.
	// Editing a comment that no longer exists returns ErrGone. It is foreman's only
	// write to GitHub (D90).
	PostIssueComment(ctx context.Context, issue task.Key, id int64, body string) (IssueComment, error)
	// GetIssueComment reads one comment. A missing comment returns ErrGone.
	GetIssueComment(ctx context.Context, repo task.Repo, id int64) (IssueComment, error)
	// PullRequestComments reads every comment on a pull request: the conversation
	// comments, the inline review comments and their replies, and the body of each
	// submitted review. A review with no body is left out (D117).
	PullRequestComments(ctx context.Context, pr task.Key) ([]PRComment, error)
}

type IssueComment struct {
	ID   int64
	URL  string
	Body string
}

// RepositoryManager provides a local source checkout for repository-independent
// commands such as inbox. Explicit start still uses the caller's checkout.
type RepositoryManager interface {
	Ensure(ctx context.Context, repo task.Repo, path string) (RepoInfo, error)
}

type WorktreeRequest struct {
	RepoDir string // any checkout of the repository
	Path    string
	Branch  string
	BaseRef string // e.g. origin/main; fetched before a new branch is created
}

// ErrWorktreeDirty means a worktree has uncommitted or untracked changes and was kept.
var ErrWorktreeDirty = errors.New("it has uncommitted or untracked changes")

// WorktreeRemoval names a task's worktree and, when DeleteBranch is set, its local branch.
type WorktreeRemoval struct {
	Path         string
	Branch       string
	DeleteBranch bool
}

type WorktreeManager interface {
	// Ensure is idempotent: it reuses the worktree when Path already has Branch checked out,
	// and reuses Branch when it exists without a worktree.
	Ensure(ctx context.Context, req WorktreeRequest) error
	// Remove deletes the worktree, and the branch if asked. It refuses a worktree with
	// uncommitted or untracked changes. A path that no longer exists is already removed.
	Remove(ctx context.Context, req WorktreeRemoval) error
}

type AgentAdapter interface {
	Name() task.Agent
	// Start runs one headless turn to completion. onSession fires as soon as the session
	// id is known, so it is recorded even if the turn later crashes.
	Start(ctx context.Context, in agent.StartInput, onSession func(task.Session)) (agent.Turn, error)
	// Resume continues s with a message. It returns agent.ErrNoSession when s no longer exists.
	Resume(ctx context.Context, s task.Session, in agent.StartInput) (agent.Turn, error)
	// Review runs a disposable, read-only session as the competing agent.
	Review(ctx context.Context, in agent.StartInput) (agent.ReviewOutcome, error)
}

// StageInstructionResolver loads the developer's instructions for a workflow stage:
// the global layer, then repoRoot/.foreman. Missing files resolve to empty (D47).
type StageInstructionResolver interface {
	Resolve(repoRoot string, stage task.Stage) (task.StageInstructions, error)
}

// Runners launches and observes the detached per-task runner process (D5).
type Runners interface {
	Spawn(key task.Key) (pid int, logPath string, err error)
	Alive(pid int) bool
	// Stop asks the runner to stop and waits for it to exit; false means it is still
	// running when the wait ends.
	Stop(key task.Key, pid int) (bool, error)
}

type Notifier interface {
	Notify(title, body string) error
}
