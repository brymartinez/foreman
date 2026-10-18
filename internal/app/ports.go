// Package app orchestrates the task lifecycle. It depends only on these interfaces;
// gh, git, the Markdown store, the agent CLIs, and process spawning live behind them.
// Ports grow with the stage that first needs them.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/knowledge"
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
	Key        task.Key
	Identifier string // source display ID, e.g. ACME-42
	Kind       task.Kind
	Title      string
	URL        string
	State      string // open, closed, merged
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Reasons    []task.InboxReason
	Labels     []string
}

type Issue struct {
	Key        task.Key
	Identifier string
	BranchName string // tracker-provided branch name, when available
	Title      string
	Body       string
	Labels     []string
	URL        string
	State      string
	UpdatedAt  time.Time
}

type PullRequest struct {
	Key               task.Key
	URL               string
	State             string // open, closed, merged
	HeadRefName       string
	BaseRefName       string
	HeadSHA           string // the commit at the PR's head
	IsCrossRepository bool
	// Mergeable is mergeable, conflicting, or unknown. GitHub computes it lazily, so
	// unknown is common right after a push to either branch.
	Mergeable string
	// Body is the PR description. The reviewer checks the change against it alone.
	Body  string
	Title string
	// ClosedAt and MergedAt are zero while the PR is open. A merged PR has both.
	ClosedAt time.Time
	MergedAt time.Time
}

const (
	Mergeable   = "mergeable"
	Conflicting = "conflicting"
)

// PRComment is one comment on a pull request. Path, Line, and InReplyTo are set for
// an inline review comment, and are what tell the agent where to reply.
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

// ErrGone means a tracker reports an item deleted or out of reach.
var ErrGone = errors.New("item not found or no longer accessible")

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
	// PullRequestsForBranch returns every PR in repo, in any state, whose head is
	// branch in repo itself.
	PullRequestsForBranch(ctx context.Context, repo task.Repo, branch string) ([]PullRequest, error)
	// CIStatus summarizes the checks on the PR's current head. CheckedAt is left zero.
	CIStatus(ctx context.Context, pr task.Key) (task.CIStatus, error)
	// PostIssueComment adds a comment to the issue, or edits comment id when id > 0.
	// Editing a comment that no longer exists returns ErrGone. It is foreman's only
	// write to GitHub.
	PostIssueComment(ctx context.Context, issue task.Key, id int64, body string) (IssueComment, error)
	// GetIssueComment reads one comment. A missing comment returns ErrGone.
	GetIssueComment(ctx context.Context, repo task.Repo, id int64) (IssueComment, error)
	// PullRequestComments reads every comment on a pull request: the conversation
	// comments, the inline review comments and their replies, and the body of each
	// submitted review. A review with no body is left out.
	PullRequestComments(ctx context.Context, pr task.Key) ([]PRComment, error)
	// IssueComments reads an issue's conversation comments, oldest first.
	IssueComments(ctx context.Context, issue task.Key) ([]IssueComment, error)
	// BranchHead returns the commit a branch points at on GitHub.
	BranchHead(ctx context.Context, repo task.Repo, branch string) (string, error)
}

// Account is a GitHub user. ID is the numeric user id that GitHub's noreply
// commit address needs.
type Account struct {
	Login string
	ID    int64
}

// Email is the account's GitHub noreply address, which attributes commits to it
// without exposing a private email.
func (a Account) Email() string {
	return fmt.Sprintf("%d+%s@users.noreply.github.com", a.ID, a.Login)
}

// AccountProvider reads the GitHub account active in gh, which opens the task's PRs.
type AccountProvider interface {
	ActiveAccount(ctx context.Context) (Account, error)
}

// SSHAccount is the GitHub login an SSH host's key authenticates as. Host is the
// [user@]host from the remote URL, which ~/.ssh/config resolves.
type SSHAccount struct {
	Host  string
	Login string
}

// RemoteAuth reports who pushes to the origin of the checkout at dir: one account per
// SSH push URL. Push URLs that are not SSH are skipped, because no push account can
// be probed.
type RemoteAuth interface {
	PushAccounts(ctx context.Context, dir string) ([]SSHAccount, error)
}

type IssueComment struct {
	ID   int64
	URL  string
	Body string
}

// TrackerComment keeps external comment IDs intact across issue trackers.
type TrackerComment struct {
	ID   string
	URL  string
	Body string
}

// IssueTracker owns issue operations. Repository and PR operations stay on GitHubClient.
type IssueTracker interface {
	AssignedIssues(ctx context.Context, projects map[task.Repo]string) ([]InboxItem, error)
	GetIssue(ctx context.Context, key task.Key) (Issue, error)
	IssueComments(ctx context.Context, key task.Key) ([]TrackerComment, error)
	GetIssueComment(ctx context.Context, id string) (TrackerComment, error)
	PostIssueComment(ctx context.Context, key task.Key, id, body string) (TrackerComment, error)
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
	// BaseRef, e.g. origin/main, is fetched before a new branch is created from it.
	// Empty means the branch must already exist. The branch's own remote copy, e.g.
	// origin/<Branch>, also fast-forwards an existing local branch to it.
	BaseRef string
}

// ErrWorktreeDirty means a worktree has uncommitted or untracked changes and was kept.
var ErrWorktreeDirty = errors.New("it has uncommitted or untracked changes")

// ErrBranchNotDeleted means Remove removed the worktree but kept the branch.
var ErrBranchNotDeleted = errors.New("the branch was not deleted")

// ErrBranchStatusUnknown means the worktree is gone but its branch could not be checked.
var ErrBranchStatusUnknown = errors.New("branch status is unknown")

// WorktreeRemoval names a task's worktree and, when DeleteBranch is set, its local branch.
type WorktreeRemoval struct {
	Path         string
	Branch       string
	DeleteBranch bool
	// Force removes the worktree with its uncommitted and untracked changes. A reset
	// sets it, because the developer confirmed discarding that work.
	Force bool
	// RepoDir is a checkout of the repository. It finds the branch when the worktree
	// is already gone, so removal can still delete it.
	RepoDir string
}

type WorktreeManager interface {
	// Ensure is idempotent: it reuses the worktree when Path already has Branch checked out,
	// and reuses Branch when it exists without a worktree.
	Ensure(ctx context.Context, req WorktreeRequest) error
	// Head reads the current commit without changing the worktree.
	Head(ctx context.Context, dir string) (string, error)
	// Checkpoint accepts a clean worktree on branch whose HEAD advances from prior.
	Checkpoint(ctx context.Context, dir, branch, prior string) (string, error)
	// ReviewHead applies the same checks while allowing HEAD to equal the prior
	// accepted checkpoint.
	ReviewHead(ctx context.Context, dir, branch, prior string) (string, error)
	// Remove deletes the worktree, and the branch if asked. It refuses a worktree with
	// uncommitted or untracked changes unless Force is set. A path that no longer
	// exists is already removed. A branch it could not delete after removing the
	// worktree is ErrBranchNotDeleted. If it cannot check the branch after the
	// worktree is gone, it returns ErrBranchStatusUnknown.
	Remove(ctx context.Context, req WorktreeRemoval) error
	// Contains fetches origin into the worktree at dir, then reports whether ancestor
	// is an ancestor of descendant, or the same commit.
	Contains(ctx context.Context, dir, ancestor, descendant string) (bool, error)
	// MergeConflicts fetches Base and Branch from origin and lists the files that
	// conflict when origin/Base merges into origin/Branch. None means a clean merge. It
	// changes neither the branch nor the worktree. A Local check fetches only Base and
	// merges it into the worktree's HEAD, for a branch that is not pushed yet.
	MergeConflicts(ctx context.Context, check MergeCheck) ([]string, error)
	// RemoteBranchHead returns the commit branch points at on origin, or "" when
	// origin has no such branch. It changes neither the branch nor the worktree.
	RemoteBranchHead(ctx context.Context, dir, branch string) (string, error)
	// ConfigureIdentity sets the commit author and committer for the worktree at dir
	// only, leaving the shared repository config and the global config alone.
	ConfigureIdentity(ctx context.Context, dir, name, email string) error
	// CommonDir is the repository directory a worktree at dir belongs to, which holds
	// its branch.
	CommonDir(ctx context.Context, dir string) (string, error)
	// BranchExists reports whether the repository at dir has a local branch of that name.
	BranchExists(ctx context.Context, dir, branch string) (bool, error)
	// SyncDefaultBranch fetches origin's branch and fast-forwards the checkout at dir,
	// which must have branch checked out with no uncommitted changes. It never
	// overwrites local work; anything it cannot fast-forward safely is an error.
	SyncDefaultBranch(ctx context.Context, dir, branch string) error
}

// MergeCheck names the worktree to fetch in, the PR's head branch, and its base branch.
type MergeCheck struct {
	Dir    string
	Branch string
	Base   string
	Local  bool
}

type AgentAdapter interface {
	Name() task.Agent
	// Start runs one headless turn to completion. onSession fires as soon as the session
	// id is known, so it is recorded even if the turn later crashes.
	Start(ctx context.Context, in agent.StartInput, onSession func(task.Session)) (agent.Turn, error)
	// Resume continues s with a message. It returns agent.ErrNoSession when s no longer exists.
	Resume(ctx context.Context, s task.Session, in agent.StartInput) (agent.Turn, error)
	// Review runs a disposable, read-only session.
	Review(ctx context.Context, in agent.StartInput) (agent.ReviewOutcome, error)
}

// BranchProposer runs a disposable agent turn before a new task has a worktree.
type BranchProposer interface {
	Propose(ctx context.Context, in agent.BranchInput) (agent.Outcome, error)
}

// StageInstructionResolver loads the developer's instructions for a workflow stage:
// the global layer, then repoRoot/.foreman. Missing files resolve to empty.
type StageInstructionResolver interface {
	Resolve(repoRoot string, stage task.Stage) (task.StageInstructions, error)
}

// Runners launches and observes the detached per-task runner process.
type Runners interface {
	// Spawn passes token to the runner, which must hold the task's launch claim.
	Spawn(key task.Key, token string) (pid int, logPath string, err error)
	Alive(pid int) bool
	// Stop asks the runner to stop and waits for it to exit; false means it is still
	// running when the wait ends.
	Stop(key task.Key, pid int) (bool, error)
	// SpawnLearn starts a detached evaluation of the task. It does not wait for it.
	SpawnLearn(key task.Key) error
}

// Alert is one announcement to the developer: a task moved into a state that needs
// them. URL is the PR when the task has one, else the issue, and may be empty.
type Alert struct {
	State     task.State
	Cancelled bool // a FAILED run that was stopped rather than failing on its own
	Key       task.Key
	URL       string
}

// Label is the alert's human text.
func (a Alert) Label() string {
	switch a.State {
	case task.NeedsDecision:
		return "Needs your decision"
	case task.Failed:
		if a.Cancelled {
			return "Run cancelled"
		}
		return "Run failed"
	case task.ReadyForHuman:
		return "Ready for human review"
	case task.Reviewed:
		return "PR feedback to answer"
	}
	return string(a.State)
}

// Notifier delivers alerts. Delivery is best effort: the notifier records its own
// failures, and the caller ignores the error.
type Notifier interface {
	Notify(Alert) error
}

// Selector reads an issue against the routing rules and picks one.
type Selector interface {
	Select(ctx context.Context, in agent.SelectInput) (agent.Selection, error)
}

// Evaluator judges one finished task from its evidence packet.
type Evaluator interface {
	Evaluate(ctx context.Context, in agent.EvaluateInput) (agent.Evaluation, error)
}

// Diagnoser traces a self-review loop stalled at the fix limit to its cause in one
// tool-less turn. A reported conflict survives only if its quotes are in sources.
type Diagnoser interface {
	Diagnose(ctx context.Context, in agent.EvaluateInput, sources []agent.DiagnosisSource) (agent.DiagnosisResult, error)
}

// Merger groups two machines' learnings about one repository in one tool-less turn.
type Merger interface {
	Merge(ctx context.Context, in agent.EvaluateInput, refs []string) (agent.MergeResult, error)
}

// KnowledgeStore keeps each repository's evaluations and learnings. repo is
// "owner/name"; the store rejects anything that could reach another repository.
type KnowledgeStore interface {
	// Lock takes the repository's evaluation lock without waiting, or returns
	// knowledge.ErrLocked.
	Lock(repo string) (func(), error)
	// Busy reports whether an evaluation of repo holds the lock now.
	Busy(repo string) bool
	Learnings(repo string) ([]knowledge.Learning, error)
	// Evaluation returns one task's stored evaluation, or nil.
	Evaluation(repo, task string) (*knowledge.Evaluation, error)
	// Evaluations returns up to limit evaluations, newest task first.
	Evaluations(repo string, limit int) ([]knowledge.Evaluation, error)
	// Attempt returns the task's last evaluation attempt, or nil.
	Attempt(repo, task string) (*knowledge.Attempt, error)
	RecordAttempt(repo, task string, at knowledge.Attempt) error
	// Apply stores ev, replacing the task's earlier evaluation and evidence.
	Apply(repo string, ev knowledge.Evaluation) error
	// Select returns at most limit active learnings, totalling at most maxChars of
	// text, ranked by relevance to query.
	Select(repo, query string, limit, maxChars int) ([]knowledge.Learning, error)
	// Snapshot returns repo's learnings and every evaluation.
	Snapshot(repo string) (knowledge.Snapshot, error)
	// Digest hashes the stored learnings and evaluations.
	Digest(repo string) (string, error)
	// Replace stores x as repo's learnings and evaluations and rewrites knowledge.md,
	// all or nothing.
	Replace(repo string, x knowledge.Snapshot) error
}

type QuotaStatus int

const (
	QuotaUnknown QuotaStatus = iota
	QuotaAvailable
	// QuotaExhausted means the agent is confirmed to have no quota left on any model.
	QuotaExhausted
	// QuotaNotSetUp means the provider explicitly reports that the agent is absent.
	QuotaNotSetUp
)

// Quota is one agent's availability. Percent is the share left when Status is known.
type Quota struct {
	Status  QuotaStatus
	Percent float64
}

// QuotaChecker reports each agent's availability. An agent missing from the map is
// unknown. An error means every agent is unknown, and says why.
type QuotaChecker interface {
	Quota(ctx context.Context) (map[task.Agent]Quota, error)
}
