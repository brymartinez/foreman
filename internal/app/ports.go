// Package app orchestrates the task lifecycle. It depends only on these interfaces;
// gh, git, the Markdown store, and the agent CLIs live behind them.
package app

import (
	"context"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

type TaskStore interface {
	Get(ctx context.Context, key task.Key) (*task.Record, error)
	Save(ctx context.Context, rec *task.Record) error
	List(ctx context.Context) ([]*task.Record, error)
}

type Issue struct {
	Key       task.Key
	Title     string
	Body      string
	URL       string
	State     string
	UpdatedAt time.Time
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

type PullRequest struct {
	Key            task.Key
	URL            string
	State          string
	HeadRefName    string
	ReviewDecision string
	UpdatedAt      time.Time
}

type CIStatus string

const (
	CIPending CIStatus = "pending"
	CIPassing CIStatus = "passing"
	CIFailing CIStatus = "failing"
	CINone    CIStatus = "none"
)

type GitHubClient interface {
	GetIssue(ctx context.Context, key task.Key) (Issue, error)
	InboxItems(ctx context.Context) ([]InboxItem, error)
	// GetItem re-fetches a tracked item by key; it can drop out of search results once unassigned or closed.
	GetItem(ctx context.Context, key task.Key) (InboxItem, error)
	GetPullRequest(ctx context.Context, key task.Key) (PullRequest, error)
	CIStatus(ctx context.Context, pr task.Key) (CIStatus, error)
}

type Worktree struct {
	Path   string
	Branch string
}

type WorktreeManager interface {
	// Ensure is idempotent: it returns the existing worktree when path and branch already match.
	Ensure(ctx context.Context, repoPath, branch, baseRef string) (Worktree, error)
}

type Turn struct {
	Session task.Session
	Outcome agent.Outcome
}

type ReviewInput struct {
	Worktree string
	BaseRef  string
	Prompt   string
}

type Finding struct {
	Severity string // blocking, should_fix
	File     string
	Line     int
	Summary  string
	Detail   string
}

type Review struct {
	Clean    bool
	Findings []Finding
}

// AgentAdapter runs one headless turn to completion per call.
// The runner process owns lifetime; adapters just build argv and parse output.
type AgentAdapter interface {
	Name() task.Agent
	// Start returns the session because Codex assigns its thread id at runtime (thread.started).
	Start(ctx context.Context, worktree, prompt string) (Turn, error)
	Resume(ctx context.Context, s task.Session, worktree, message string) (Turn, error)
	Review(ctx context.Context, in ReviewInput) (Review, error)
}
