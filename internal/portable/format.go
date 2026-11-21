// Package portable moves local work between Foreman databases as one versioned
// YAML file. An export carries local issues with their confirmed triage, notes,
// tags, requirements, plans, decisions, review findings, checkpoints, and branch,
// commit, and PR references, and optionally the curated learnings of repositories.
// It never carries credentials, provider session IDs, raw logs, live runner state,
// or machine-specific paths. An import restores records only: it starts no agent
// and writes to no tracker.
package portable

import (
	"time"

	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/task"
)

// Format names the file's schema. Import rejects any other.
const Format = "foreman-work/1"

// File is the whole export.
type File struct {
	Format     string               `yaml:"format"`
	ExportedAt time.Time            `yaml:"exported_at"`
	Issues     []Issue              `yaml:"issues"`
	Learnings  []knowledge.Snapshot `yaml:"learnings,omitempty"`
}

// Issue is one local issue and, when work has started on it, that work.
type Issue struct {
	ID          string    `yaml:"id"`
	Title       string    `yaml:"title"`
	Description string    `yaml:"description,omitempty"`
	SourceURL   string    `yaml:"source_url,omitempty"`
	Status      string    `yaml:"status"`
	Repo        string    `yaml:"repo,omitempty"`
	Notes       string    `yaml:"notes,omitempty"`
	Tags        []string  `yaml:"tags,omitempty"`
	CreatedAt   time.Time `yaml:"created_at"`
	UpdatedAt   time.Time `yaml:"updated_at"`
	// Triage is the confirmed decisions, empty while the issue awaits triage.
	Triage string `yaml:"triage,omitempty"`
	Work   *Work  `yaml:"work,omitempty"`
}

// Work is the durable record of a task on one repository. State is open or done.
type Work struct {
	Repo         string      `yaml:"repo"`
	State        string      `yaml:"state"`
	Notes        string      `yaml:"notes,omitempty"`
	Requirements string      `yaml:"requirements,omitempty"`
	Plan         string      `yaml:"plan,omitempty"`
	Fingerprint  string      `yaml:"description_fingerprint,omitempty"`
	Decisions    []Decision  `yaml:"decisions,omitempty"`
	Reviews      []Review    `yaml:"reviews,omitempty"`
	Checkpoint   *Checkpoint `yaml:"checkpoint,omitempty"`
	Branch       string      `yaml:"branch,omitempty"`
	Commits      []string    `yaml:"commits,omitempty"`
	PR           *PR         `yaml:"pr,omitempty"`
}

// Decision is a question the agent asked and the answer, if given.
type Decision struct {
	AskedAt    time.Time     `yaml:"asked_at"`
	Question   task.Question `yaml:"question"`
	Answer     string        `yaml:"answer,omitempty"`
	AnsweredAt time.Time     `yaml:"answered_at,omitempty"`
}

// Review is one review round and its findings.
type Review struct {
	Kind     string         `yaml:"kind"` // self or adversarial
	Round    int            `yaml:"round"`
	Reviewer string         `yaml:"reviewer,omitempty"`
	Verdict  string         `yaml:"verdict"` // clean or changes_requested
	Summary  string         `yaml:"summary,omitempty"`
	Findings []task.Finding `yaml:"findings,omitempty"`
	Head     string         `yaml:"head,omitempty"`
	At       time.Time      `yaml:"at"`
}

// Checkpoint is the last accepted implementation commit and what came next.
type Checkpoint struct {
	Head  string `yaml:"head,omitempty"`
	Next  string `yaml:"next,omitempty"`
	Count int    `yaml:"count,omitempty"`
}

// PR is a pull request reference.
type PR struct {
	Number int    `yaml:"number"`
	URL    string `yaml:"url,omitempty"`
}
