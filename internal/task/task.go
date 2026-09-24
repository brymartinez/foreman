// Package task holds the durable task model persisted as Markdown frontmatter.
package task

import (
	"fmt"
	"regexp"
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

// Delegation exists once the user has run start; tracked-only items have none.
type Delegation struct {
	Implementer Session
	Reviewer    Agent
	Branch      string
	BaseRef     string
	Worktree    string
	ContextFile string
	RunnerPID   int
	RunnerLog   string
	PR          int
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

type Option struct {
	Label       string `json:"label" yaml:"label"`
	Description string `json:"description" yaml:"description"`
}

type Decision struct {
	AskedAt    time.Time
	Question   Question
	Answer     string
	AnsweredAt time.Time
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
	// Body is the Markdown below the frontmatter, round-tripped untouched so human edits survive.
	Body string
}
