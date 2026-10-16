// Package knowledge keeps what foreman learned about each repository: per-task
// evaluations, the learnings they support, and the knowledge.md rendered from the
// active ones. Everything lives under Dir/<owner>/<repo>/, outside any repository.
package knowledge

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
)

type Kind string

const (
	Convention Kind = "convention"
	Fact       Kind = "fact"
	Practice   Kind = "practice"
)

// Scope separates guidance worth reusing from a detail of one task.
type Scope string

const (
	Reusable Scope = "reusable"
	OneOff   Scope = "one_off"
)

// Source says who backs a learning: a maintainer's recorded decision or comment, or
// only the agent's observation of the work.
type Source string

const (
	Maintainer Source = "maintainer"
	Observed   Source = "observed"
)

type Status string

const (
	Candidate Status = "candidate"
	Active    Status = "active"
	Retired   Status = "retired"
)

const (
	Revise = "revise"
	Retire = "retire"
)

// Proposal is one learning an evaluation suggests. Supports names existing learnings
// it reinforces.
type Proposal struct {
	Kind     Kind     `yaml:"kind" json:"kind"`
	Text     string   `yaml:"text" json:"text"`
	Scope    Scope    `yaml:"scope" json:"scope"`
	Why      string   `yaml:"why,omitempty" json:"why"`
	Source   Source   `yaml:"source" json:"source"`
	Supports []string `yaml:"supports,omitempty" json:"supports"`
	// Cites names the decisions (D1) and PR comments (C1) behind a maintainer source.
	Cites []string `yaml:"cites,omitempty" json:"cites"`
}

// Revision asks to rewrite or retire an existing learning.
type Revision struct {
	ID     string   `yaml:"id" json:"id"`
	Action string   `yaml:"action" json:"action"`
	Text   string   `yaml:"text,omitempty" json:"text"`
	Reason string   `yaml:"reason" json:"reason"`
	Source Source   `yaml:"source" json:"source"`
	Cites  []string `yaml:"cites,omitempty" json:"cites"`
}

// Evaluation is one judgment of one task. TaskAt is when the task's work started;
// it orders observations from different tasks.
type Evaluation struct {
	Task        string     `yaml:"task"`
	TaskAt      time.Time  `yaml:"task_at"`
	At          time.Time  `yaml:"at"`
	Agent       string     `yaml:"agent,omitempty"`
	Fingerprint string     `yaml:"fingerprint"`
	Assessment  string     `yaml:"assessment"`
	Signals     []string   `yaml:"signals,omitempty"`
	Proposals   []Proposal `yaml:"proposals,omitempty"`
	Revisions   []Revision `yaml:"revisions,omitempty"`
	// MaintainerRefs are the ids of the task's answered decisions (D1...) and human PR
	// comments (C1...). A maintainer source counts only when it cites some of them
	// and nothing else.
	MaintainerRefs []string `yaml:"maintainer_refs,omitempty"`
	// Dropped lists rejected proposals and revisions by position and reason, never
	// their text.
	Dropped []string `yaml:"dropped,omitempty"`
}

type Evidence struct {
	Task   string    `yaml:"task"`
	At     time.Time `yaml:"at"`
	Source Source    `yaml:"source"`
	Note   string    `yaml:"note,omitempty"`
	Cites  []string  `yaml:"cites,omitempty"`
}

// Change is one entry in a learning's history: a promotion, demotion, revision, or
// retirement, with the task and reason behind it.
type Change struct {
	At     time.Time `yaml:"at"`
	Task   string    `yaml:"task"`
	Action string    `yaml:"action"`
	From   string    `yaml:"from,omitempty"`
	// To is a rewrite's new text, so the text can be rebuilt when one is withdrawn.
	To     string `yaml:"to,omitempty"`
	Reason string `yaml:"reason,omitempty"`
}

type Learning struct {
	ID       string     `yaml:"id"`
	Kind     Kind       `yaml:"kind"`
	Text     string     `yaml:"text"`
	Status   Status     `yaml:"status"`
	Evidence []Evidence `yaml:"evidence,omitempty"`
	History  []Change   `yaml:"history,omitempty"`
}

// Tasks counts the distinct tasks behind a learning.
func (l Learning) Tasks() int {
	seen := map[string]bool{}
	for _, e := range l.Evidence {
		seen[e.Task] = true
	}
	return len(seen)
}

// Store reads and writes one directory per repository under Dir.
type Store struct {
	Dir string
	// Log receives why a proposal was dropped. It never receives the proposal's text.
	Log io.Writer
}

// ErrLocked means another process is evaluating the same repository.
var ErrLocked = errors.New("knowledge: repository is locked by another evaluation")

var (
	repoPart = regexp.MustCompile(`^[\w.-]+$`)
	taskID   = regexp.MustCompile(`^[\w.-]+$`)
)

func validName(s string, pattern *regexp.Regexp) bool {
	return pattern.MatchString(s) && s != "." && s != ".."
}

func validKind(k Kind) bool { return k == Convention || k == Fact || k == Practice }

func errTask(id string) error { return fmt.Errorf("knowledge: invalid task id %q", id) }
