// Package task holds the durable task model persisted as Markdown frontmatter.
package task

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Agent string

const (
	Claude Agent = "claude"
	Codex  Agent = "codex"
)

func ParseAgent(s string) (Agent, error) {
	switch a := Agent(s); a {
	case Claude, Codex:
		return a, nil
	}
	return "", fmt.Errorf("unknown agent %q (want claude or codex)", s)
}

// Reviewer is the competing agent: Claude implements, Codex reviews, and vice versa.
func (a Agent) Reviewer() Agent {
	if a == Claude {
		return Codex
	}
	return Claude
}

type State string

const (
	Discovered    State = "DISCOVERED"
	Working       State = "WORKING"
	NeedsDecision State = "NEEDS_DECISION"
	Reviewing     State = "REVIEWING"
	ReadyForHuman State = "READY_FOR_HUMAN"
	// Reviewed means the developer commented on the task's pull request and the agent
	// has not answered yet. Resuming the task hands it those comments.
	Reviewed State = "REVIEWED"
	Done     State = "DONE"
	Failed   State = "FAILED"
	// PRClosed means the task's PR was closed without merging. The agent did not
	// fail, so the task keeps its file and branch. Cleanup may remove its worktree.
	PRClosed State = "PR_CLOSED"
)

// Repo is "owner/name", as gh reports nameWithOwner.
type Repo string

var repoPattern = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)

func ParseRepo(s string) (Repo, error) {
	if !repoPattern.MatchString(s) {
		return "", fmt.Errorf("invalid repo %q (want owner/name)", s)
	}
	return Repo(s), nil
}

type Key struct {
	Repo       Repo
	Number     int
	Source     Source
	ExternalID string
}

type Source string

const (
	GitHub Source = "github"
	Linear Source = "linear"
)

type Kind string

const (
	Issue       Kind = "issue"
	PullRequest Kind = "pr"
)

type InboxReason string

const (
	Assigned        InboxReason = "assigned"
	Mentioned       InboxReason = "mentioned"
	ReviewRequested InboxReason = "review_requested"
	Tracked         InboxReason = "tracked"
)

// Snapshot is what GitHub looked like the last time the user ran inbox.
// The inbox diff compares current GitHub state against it.
type Snapshot struct {
	UpdatedAt time.Time
	State     string // open, closed, merged
	Reasons   []InboxReason
}

type Session struct {
	Agent Agent
	ID    string
}

type Usage struct {
	ClaudeCostUSD        float64
	CodexInputTokens     int64
	CodexCachedTokens    int64
	CodexOutputTokens    int64
	CodexReasoningTokens int64
	ClaudeTurns          int
	CodexTurns           int
	// ToolCalls counts tool calls by name: Claude tool names, or Codex item types.
	ToolCalls map[string]int
}

// Profile is the agent, models, and effort routing chose for a task. It is stored
// resolved, so editing routing.yaml or config.yaml never changes a running task.
// Empty fields leave the adapter's own setting in place.
type Profile struct {
	Name                string `yaml:"name"`
	Agent               Agent  `yaml:"agent"`
	PlanModel           string `yaml:"plan_model,omitempty"`
	Model               string `yaml:"model,omitempty"`
	PlanReasoningEffort string `yaml:"plan_reasoning_effort,omitempty"`
	ReasoningEffort     string `yaml:"reasoning_effort,omitempty"`
}

// Delegation exists once the user has run start; tracked-only items have none.
type Delegation struct {
	Implementer Session
	// Profile is set when routing chose the implementer; RouteReason says why.
	// RouteLogged is set once the runner has written the route line to the log.
	Profile     *Profile
	RouteReason string
	RouteLogged bool
	StartedAt   time.Time
	Reviewer    Agent
	// Account is the GitHub login active in gh when the task first launched. Every
	// commit, push, and PR action of the task runs as it. AccountEmail is its noreply
	// address. Tasks started before pinning have neither until their next launch.
	Account      string
	AccountEmail string
	Branch       string
	BaseRef      string
	Worktree     string
	// WorktreeRemovedAt is set once cleanup removed the worktree; the branch may remain.
	WorktreeRemovedAt time.Time
	ContextFile       string
	RunnerPID         int
	RunnerLog         string
	PR                int
	PRURL             string
	// PriorPR is the PR of a run the developer reset. Each fresh session reads it
	// again as context. It equals PR when this run continues that PR.
	PriorPR int
	// PRHead is the PR's head commit when foreman last read it. A review covers one head.
	PRHead string
	// CheckpointHead is the last accepted implementation commit, or the head when
	// the worktree was prepared. The commit and CheckpointNext cross to a fresh session.
	CheckpointHead string
	CheckpointNext string
	Checkpoints    int
	// ConflictAttempts lists every PR head and base head an automatic merge conflict
	// repair ran for, oldest first. A repair runs once per pair. ConflictPending is set
	// until the runner has run the last one.
	ConflictAttempts []ConflictAttempt
	ConflictPending  bool
}

// ResetMark is all a reset keeps of the discarded run: the PR it opened, if any, so
// the next start can continue that PR or read it as context. PRState is the state
// GitHub reported at the reset; the next start reads it again.
type ResetMark struct {
	At      time.Time
	PR      int
	PRURL   string
	Branch  string
	PRState string
	// Cleanup is the part of the run a reset has not removed yet, or nil.
	Cleanup *ResetCleanup
}

// ResetCleanup names the local run files a reset still has to remove. Since is set
// while Owner, the reset or one start in process PID, removes them and makes the new
// worktree; any other start waits while that process runs, because it would work at
// the same path. Since is zero once the owner finished or gave up, and the next start
// removes what is left.
type ResetCleanup struct {
	Worktree  string    `yaml:"worktree,omitempty"`
	Branch    string    `yaml:"branch,omitempty"`
	RepoDir   string    `yaml:"repo_dir,omitempty"`
	RunnerLog string    `yaml:"runner_log,omitempty"`
	Since     time.Time `yaml:"since,omitempty"`
	Owner     string    `yaml:"owner,omitempty"`
	PID       int       `yaml:"pid,omitempty"`
	// Head is the worktree's commit when the reset ran. A retry removes the path only
	// while it still has that commit, so it never removes a newer worktree there.
	Head string `yaml:"head,omitempty"`
}

// ConflictAttempt is one automatic merge conflict repair: the PR head that conflicted
// and the base head it conflicted with.
type ConflictAttempt struct {
	Head string `yaml:"head"`
	Base string `yaml:"base"`
}

// ConflictAttempted reports whether an automatic repair already ran, or is running,
// for this PR head and base head.
func (d *Delegation) ConflictAttempted(head, base string) bool {
	if head == "" || base == "" {
		return false
	}
	for _, c := range d.ConflictAttempts {
		if c.Head == head && c.Base == base {
			return true
		}
	}
	return false
}

// LastConflict is the most recent repair attempt, or the zero value.
func (d *Delegation) LastConflict() ConflictAttempt {
	if len(d.ConflictAttempts) == 0 {
		return ConflictAttempt{}
	}
	return d.ConflictAttempts[len(d.ConflictAttempts)-1]
}

// Check is one validation command the implementation agent ran, as it reported it.
type Check struct {
	Command string `json:"command" yaml:"command"`
	Passed  bool   `json:"passed" yaml:"passed"`
}

// Outcome is the last thing an agent turn (or the runner) reported, kept for show.
type Outcome struct {
	Status  string
	Summary string
	At      time.Time
}

// Question is the BLOCKED: Need decision payload. Agents return it as
// structured output; foreman never scrapes it from terminal text.
type Question struct {
	Issue          string   `json:"issue" yaml:"issue"`
	Context        string   `json:"context" yaml:"context"`
	Options        []Option `json:"options" yaml:"options"`
	Recommendation string   `json:"recommendation" yaml:"recommendation"`
	ImpactIfWrong  string   `json:"impactIfWrong" yaml:"impact_if_wrong"`
}

func (q Question) Validate() error {
	if strings.TrimSpace(q.Issue) == "" {
		return errors.New("question has no issue")
	}
	if len(q.Options) < 2 {
		return fmt.Errorf("question needs at least two options, got %d", len(q.Options))
	}
	seen := map[string]bool{}
	for _, o := range q.Options {
		label := strings.ToUpper(strings.TrimSpace(o.Label))
		if label == "" || seen[label] {
			return fmt.Errorf("option labels must be unique and non-empty, got %q", o.Label)
		}
		seen[label] = true
	}
	return nil
}

// Option finds an option by label, case-insensitively.
func (q Question) Option(label string) (Option, bool) {
	for _, o := range q.Options {
		if strings.EqualFold(strings.TrimSpace(o.Label), strings.TrimSpace(label)) {
			return o, true
		}
	}
	return Option{}, false
}

// Recommended resolves the recommendation to an option. Agents are told to give the
// bare label, but also write "A", "A.", "A: because…" or "Option A – …".
func (q Question) Recommended() (Option, bool) {
	rec := strings.TrimSpace(q.Recommendation)
	rec = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(rec, "Option "), "option "))
	for _, o := range q.Options {
		label := strings.TrimSpace(o.Label)
		if label == "" || len(rec) < len(label) || !strings.EqualFold(rec[:len(label)], label) {
			continue
		}
		if rest := rec[len(label):]; rest == "" || !isWordChar(rest[0]) {
			return o, true
		}
	}
	return Option{}, false
}

func isWordChar(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

type Option struct {
	Label       string `json:"label" yaml:"label"`
	Description string `json:"description" yaml:"description"`
}

type Decision struct {
	AskedAt    time.Time
	Question   Question
	Answer     string
	AnsweredAt time.Time
	// DeliveredAt is set once a runner has handed the answer to the agent.
	DeliveredAt time.Time
}

// Pending is the oldest unanswered decision, if any. A blocked turn can ask several
// questions; they are answered in the order they were asked.
func (r *Record) Pending() *Decision {
	for i := range r.Decisions {
		if r.Decisions[i].Answer == "" {
			return &r.Decisions[i]
		}
	}
	return nil
}

// PendingCount is how many decisions are waiting for an answer.
func (r *Record) PendingCount() int {
	n := 0
	for _, d := range r.Decisions {
		if d.Answer == "" {
			n++
		}
	}
	return n
}

// Undelivered lists the answered decisions the agent has not received yet.
func (r *Record) Undelivered() []Decision {
	var out []Decision
	for _, d := range r.Decisions {
		if d.Answer != "" && d.DeliveredAt.IsZero() {
			out = append(out, d)
		}
	}
	return out
}

type Record struct {
	Key
	Identifier     string
	Kind           Kind
	Title          string
	URL            string
	State          State
	Delegation     *Delegation
	GitHub         *Snapshot
	LastGitHubSync time.Time
	Decisions      []Decision
	LastOutcome    *Outcome
	Usage          Usage
	// Validation is what the implementation agent reported with its PR.
	Validation []Check
	// Reviews are the review rounds, oldest first.
	Reviews []Review
	// Plan holds the planning documents posted on the issue, in PlanDocs order.
	Plan []PlanComment
	// Feedback is every comment on the task's PR foreman has read, and whether the
	// agent has answered it.
	Feedback []Feedback
	// CI is the readiness gate's last look at the PR's checks on GitHub. Any implementer
	// turn clears it.
	CI *CIStatus
	// Attempts are the runs of work on the issue, one per PR, oldest first.
	Attempts []Attempt
	// Reset is set when the developer discarded a failed run. The record then has no
	// delegation, and auto mode leaves the issue for a manual start.
	Reset *ResetMark
	// Body is the Markdown below the frontmatter, round-tripped untouched so human edits survive.
	Body string
}

func (r *Record) IssueSource() Source {
	if r.Source == Linear {
		return Linear
	}
	return GitHub
}

// PlanDoc is one planning document the implementer writes before any code.
type PlanDoc string

const (
	Analysis           PlanDoc = "analysis"
	PRD                PlanDoc = "prd"
	Design             PlanDoc = "design"
	ImplementationPlan PlanDoc = "plan"
)

// PlanDocs lists the documents in the order they are written and posted.
var PlanDocs = []PlanDoc{Analysis, PRD, Design, ImplementationPlan}

// Title is the document's heading in its issue comment.
func (d PlanDoc) Title() string {
	switch d {
	case Analysis:
		return "Analysis"
	case PRD:
		return "PRD"
	case Design:
		return "Design"
	case ImplementationPlan:
		return "Implementation plan"
	}
	return string(d)
}

// PlanComment is a document foreman posted on the issue as its own comment.
type PlanComment struct {
	Doc               PlanDoc `yaml:"doc"`
	CommentID         int64   `yaml:"comment_id,omitempty"`
	ExternalCommentID string  `yaml:"external_comment_id,omitempty"`
	URL               string  `yaml:"url"`
}

// PlanComment returns the posted comment for doc, or nil.
func (r *Record) PlanComment(doc PlanDoc) *PlanComment {
	for i := range r.Plan {
		if r.Plan[i].Doc == doc {
			return &r.Plan[i]
		}
	}
	return nil
}

// PlanPosted reports whether every planning document is on the issue.
func (r *Record) PlanPosted() bool {
	for _, d := range PlanDocs {
		if c := r.PlanComment(d); c == nil || c.URL == "" {
			return false
		}
	}
	return true
}

// SetPlanComment records a posted comment, replacing an earlier one for the same doc.
func (r *Record) SetPlanComment(c PlanComment) {
	if cur := r.PlanComment(c.Doc); cur != nil {
		*cur = c
		return
	}
	r.Plan = append(r.Plan, c)
}

type Finding struct {
	Severity string `json:"severity" yaml:"severity"` // blocking, should_fix
	File     string `json:"file" yaml:"file,omitempty"`
	Line     int    `json:"line" yaml:"line,omitempty"`
	Summary  string `json:"summary" yaml:"summary"`
	Detail   string `json:"detail" yaml:"detail,omitempty"`
}

type Review struct {
	Round    int
	Reviewer Agent
	PR       int
	Clean    bool
	Summary  string
	Findings []Finding
	At       time.Time
	Head     string // the PR head commit reviewed; empty on records from before heads were recorded
}

// LatestReview is the most recent review round, or nil.
func (r *Record) LatestReview() *Review {
	if len(r.Reviews) == 0 {
		return nil
	}
	return &r.Reviews[len(r.Reviews)-1]
}

// ChangeRequestsSinceDecision counts review rounds that requested changes since the
// developer last answered a question. The round cap limits fix rounds, so
// clean re-reviews (after a CI fix) don't use it up; answering resets it.
func (r *Record) ChangeRequestsSinceDecision() int {
	var last time.Time
	for _, d := range r.Decisions {
		if d.AnsweredAt.After(last) {
			last = d.AnsweredAt
		}
	}
	n := 0
	for _, rv := range r.Reviews {
		if !rv.Clean && rv.At.After(last) {
			n++
		}
	}
	return n
}

type CIState string

const (
	CIPending CIState = "pending"
	CIPassing CIState = "passing"
	CIFailing CIState = "failing"
	// CINone means the PR has no checks at all. The gate treats it as passing.
	CINone CIState = "none"
)

// CheckRun is one failing or pending CI check, kept so the implementer and the
// developer can see which check to open.
type CheckRun struct {
	Name string `json:"name" yaml:"name"`
	Link string `json:"link" yaml:"link,omitempty"`
}

type CIStatus struct {
	State CIState
	// Failing lists the checks that failed or were cancelled.
	Failing   []CheckRun
	CheckedAt time.Time
}

// Green reports whether CI lets the task through the readiness gate.
func (c *CIStatus) Green() bool {
	return c != nil && (c.State == CIPassing || c.State == CINone)
}

// CommitsSinceReview reports whether the PR head moved after its latest review.
func (r *Record) CommitsSinceReview() bool {
	latest := r.LatestReview()
	return r.Delegation != nil && latest != nil && latest.Head != "" && r.Delegation.PRHead != "" && latest.Head != r.Delegation.PRHead
}

// ReviewPending reports whether the verified PR head has not been reviewed, so a run
// continues with the review instead of implementing again: after a crash during a
// review, or after new commits on a PR that had a clean review.
func (r *Record) ReviewPending() bool {
	d := r.Delegation
	if d == nil || d.PR == 0 || d.PRHead == "" || len(r.Undelivered()) > 0 {
		return false
	}
	latest := r.LatestReview()
	return latest == nil || latest.PR != d.PR || latest.Head != d.PRHead
}
