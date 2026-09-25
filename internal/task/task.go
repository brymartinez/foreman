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
	Done          State = "DONE"
	Failed        State = "FAILED"
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
	Repo   Repo
	Number int
}

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
}

// Delegation exists once the user has run start; tracked-only items have none.
type Delegation struct {
	Implementer Session
	Reviewer    Agent
	Branch      string
	BaseRef     string
	Worktree    string
	// WorktreeRemovedAt is set once `foreman clean` removed the worktree; the branch may remain.
	WorktreeRemovedAt time.Time
	ContextFile       string
	RunnerPID         int
	RunnerLog         string
	PR                int
	PRURL             string
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

// Pending is the unanswered decision, if any. Agents ask one question per turn.
func (r *Record) Pending() *Decision {
	for i := len(r.Decisions) - 1; i >= 0; i-- {
		if r.Decisions[i].Answer == "" {
			return &r.Decisions[i]
		}
	}
	return nil
}

// Undelivered is the answered decision the agent has not received yet.
func (r *Record) Undelivered() *Decision {
	for i := range r.Decisions {
		d := &r.Decisions[i]
		if d.Answer != "" && d.DeliveredAt.IsZero() {
			return d
		}
	}
	return nil
}

type Record struct {
	Key
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
	// Reviews are the competing agent's review rounds, oldest first.
	Reviews []Review
	// CI is the readiness gate's last look at the PR's checks on GitHub. Any implementer
	// turn clears it.
	CI *CIStatus
	// Body is the Markdown below the frontmatter, round-tripped untouched so human edits survive.
	Body string
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
}

// LatestReview is the most recent review round, or nil.
func (r *Record) LatestReview() *Review {
	if len(r.Reviews) == 0 {
		return nil
	}
	return &r.Reviews[len(r.Reviews)-1]
}

// ChangeRequestsSinceDecision counts review rounds that requested changes since the
// developer last answered a question. The round cap (D11) limits fix rounds, so
// clean re-reviews (after a CI fix) don't use it up (D40); answering resets it.
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
	// CINone means the PR has no checks at all. The gate treats it as passing (D37).
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
