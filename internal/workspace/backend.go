package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/brymartinez/foreman/internal/knowledge"
)

// ErrNoPayload is a knowledge entry's ParseErr when the entry exists but holds no
// payload at all, as after a publish that created the page and then failed. Unlike
// a payload that does not parse, it is not someone's edit, so publishing resumes.
var ErrNoPayload = errors.New("the entry has no knowledge payload")

// TaskEntry is one task as the store holds it: the user's fields, the
// Foreman-owned fields it last published, and the parsed payload.
type TaskEntry struct {
	// Ref locates the entry in the store: a Notion page ID or a note path.
	Ref string
	// ID is the Foreman ID, empty for an entry a user just created.
	ID        string
	CreatedAt time.Time
	// EditedAt is when the store last saw any edit, Foreman's included.
	EditedAt time.Time

	// User-owned fields.
	Name            string
	Repository      string
	SourceURL       string
	Tags            []string
	Description     string
	ConfirmedTriage string
	ScopeConfirmed  bool
	Queue           bool
	Answer          string
	ReplyCode       string // the code published with the visible question

	// Record is the parsed payload, nil when the entry has none yet. ParseErr says
	// why a present payload was not accepted.
	Record   *Record
	ParseErr error
}

// TaskPublish is what Foreman writes for a task. Create seeds the user-owned
// fields only when the entry does not exist yet; an existing entry's user fields
// are never touched.
type TaskPublish struct {
	Record    Record
	ReplyCode string
	// Reason is why a queued task could not start, shown in the Foreman section.
	Reason string
	// Create holds the user fields to seed a new entry with.
	Create TaskSeed
	// Resume says an earlier publish created the entry and failed before it finished
	// writing it, so a description that is only the start of Create.Description is
	// completed.
	Resume bool
	// Reseed makes an existing entry take Create's confirmed triage and scope flag.
	// Only a move sets it: until the cutover SQLite owns the workspace, so a retry
	// must carry scope that changed there since the first attempt.
	Reseed bool
}

// TaskSeed is the user-owned content of a new entry.
type TaskSeed struct {
	Name, Repository, SourceURL, Description, ConfirmedTriage string
	Tags                                                      []string
	ScopeConfirmed                                            bool
}

// KnowledgeEntry is one repository's knowledge as the store holds it.
type KnowledgeEntry struct {
	Repo     string
	Ref      string
	EditedAt time.Time
	// Snapshot is the parsed payload. ParseErr is set instead when it does not parse.
	Snapshot *knowledge.Snapshot
	ParseErr error
	Digest   string
}

// KnowledgePublish is what Foreman writes for one repository.
type KnowledgePublish struct {
	Repo     string
	Snapshot knowledge.Snapshot
	// Guidance is the rendered active guidance shown above the payload.
	Guidance string
	Active   int
}

// Duplicate is a Foreman ID that more than one entry holds. Refs are oldest first.
type Duplicate struct {
	ID   string
	Refs []string
}

// Step is one reported step of a connectivity check.
type Step struct {
	Name string
	Err  error
}

// Backend is the store-neutral workspace store. Notion and Obsidian implement it.
type Backend interface {
	// Ensure creates whatever the store needs (databases, folders) and adds
	// missing fields. It never removes anything. It is safe to repeat.
	Ensure(ctx context.Context) error
	// Check runs the connectivity check, reports each step through report, and
	// stops at the first failure. It changes no real task.
	Check(ctx context.Context, report func(Step)) error

	ListTasks(ctx context.Context) ([]TaskEntry, error)
	// TaskFields looks up the current user fields by Foreman ID without requiring the task payload.
	TaskFields(ctx context.Context, id string) (*TaskEntry, error)
	// CodedAnswers reports whether submitted answers carry the visible question's reply code.
	CodedAnswers() bool
	// UpsertTask finds the entry by Foreman ID and creates or updates it.
	UpsertTask(ctx context.Context, p TaskPublish) (ref string, err error)
	// AssignID writes the Foreman ID of an entry a user created.
	AssignID(ctx context.Context, ref, id string) error
	ClearQueue(ctx context.Context, ref string) error
	ClearAnswer(ctx context.Context, ref string) error
	// Duplicates lists Foreman IDs held by more than one entry.
	Duplicates(ctx context.Context) ([]Duplicate, error)
	// Trash moves an entry out of use. It is how duplicates and the check's test
	// entry go.
	Trash(ctx context.Context, ref string) error

	ListKnowledge(ctx context.Context) ([]KnowledgeEntry, error)
	WriteKnowledge(ctx context.Context, p KnowledgePublish) error
}

// SnapshotDigest identifies a snapshot's content, ignoring when it was exported.
func SnapshotDigest(s knowledge.Snapshot) string {
	s.ExportedAt = time.Time{}
	s.Format = knowledge.FormatVersion
	raw, err := yaml.Marshal(s)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
