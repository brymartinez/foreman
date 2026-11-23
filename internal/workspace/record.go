package workspace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/brymartinez/foreman/internal/localissues"
	"github.com/brymartinez/foreman/internal/portable"
	"github.com/brymartinez/foreman/internal/task"
)

// Format names the shared record's schema.
const Format = "foreman-task/1"

// StatusInbox is the status of a local issue that has no task yet.
const StatusInbox = "INBOX"

var (
	githubID = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+#[1-9][0-9]*$`)
	statusRe = regexp.MustCompile(`^[A-Z_]+$`)
)

// Record is the shared record of one task: what people on any device need. It is
// built from named fields only. Provider sessions, launch claims, runner PIDs,
// worktree and checkout paths, the account pin, usage, and logs have no field here,
// so they cannot reach the store.
type Record struct {
	Format string      `yaml:"format"`
	ID     string      `yaml:"id"` // Foreman ID: L-xxxxxxxx or owner/repo#N
	Source task.Source `yaml:"source"`
	Repo   string      `yaml:"repo,omitempty"`
	Title  string      `yaml:"title"`
	URL    string      `yaml:"url,omitempty"`
	// Status is the exact task state, or INBOX for an issue with no task.
	Status     string    `yaml:"status"`
	StageSince time.Time `yaml:"stage_since,omitempty"`
	Outcome    *Outcome  `yaml:"outcome,omitempty"`
	// Triage is the confirmed scope.
	Triage string `yaml:"triage,omitempty"`
	// Issue holds the local issue fields; GitHub tasks have none.
	Issue *Issue `yaml:"issue,omitempty"`
	// Work holds the plan, decisions (an empty answer is a waiting question),
	// reviews, checkpoint, branch, commits, and PR.
	Work *portable.Work `yaml:"work,omitempty"`
}

// Outcome is the last turn's result.
type Outcome struct {
	Status  string    `yaml:"status"`
	Summary string    `yaml:"summary,omitempty"`
	At      time.Time `yaml:"at,omitempty"`
}

// Issue is a local issue's own fields.
type Issue struct {
	Description string    `yaml:"description,omitempty"`
	Status      string    `yaml:"status"`
	Notes       string    `yaml:"notes,omitempty"`
	Tags        []string  `yaml:"tags,omitempty"`
	CreatedAt   time.Time `yaml:"created_at"`
	UpdatedAt   time.Time `yaml:"updated_at"`
}

// Input is what the host holds for one entry.
type Input struct {
	Task    *task.Record
	Issue   *localissues.Issue
	Triage  string
	StageAt time.Time
}

func sec(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC().Truncate(time.Second)
}

// TaskID is the Foreman ID of a task record.
func TaskID(rec *task.Record) string {
	if rec.Source == task.Local {
		return rec.ExternalID
	}
	return fmt.Sprintf("%s#%d", rec.Repo, rec.Number)
}

// Build makes the shared record from saved state. It reads no network and calls no
// model.
func Build(in Input) (Record, error) {
	r := Record{Format: Format, Status: StatusInbox, Triage: strings.TrimSpace(in.Triage)}
	if rec := in.Task; rec != nil {
		r.ID, r.Source, r.Repo = TaskID(rec), rec.Source, string(rec.Repo)
		r.Title, r.URL = rec.Title, rec.URL
		r.Status = string(rec.State)
		r.StageSince = sec(in.StageAt)
		if o := rec.LastOutcome; o != nil {
			r.Outcome = &Outcome{Status: o.Status, Summary: o.Summary, At: sec(o.At)}
		}
		r.Work = portable.WorkOf(rec)
	}
	if li := in.Issue; li != nil {
		if r.ID == "" {
			r.ID, r.Source, r.Title, r.URL, r.Repo = li.ID, task.Local, li.Title, li.SourceURL, li.Repo
		}
		r.Issue = &Issue{Description: li.Description, Status: string(li.Status), Notes: li.Notes, Tags: li.Tags,
			CreatedAt: li.CreatedAt.UTC(), UpdatedAt: li.UpdatedAt.UTC()}
	}
	if r.ID == "" {
		return Record{}, errors.New("a shared record needs a task or a local issue")
	}
	return r, r.validate()
}

func (r Record) validate() error {
	if r.Format != Format {
		return fmt.Errorf("unsupported format %q (want %s)", r.Format, Format)
	}
	switch r.Source {
	case task.Local:
		if !localissues.IDPattern.MatchString(r.ID) {
			return fmt.Errorf("id %q must look like L-0A1B2C3D", r.ID)
		}
	case task.GitHub:
		if !githubID.MatchString(r.ID) {
			return fmt.Errorf("id %q must look like owner/repo#N", r.ID)
		}
	default:
		return fmt.Errorf("unknown source %q", r.Source)
	}
	if strings.TrimSpace(r.Title) == "" {
		return errors.New("title is empty")
	}
	if r.Status != StatusInbox && !statusRe.MatchString(r.Status) {
		return fmt.Errorf("invalid status %q", r.Status)
	}
	if r.Repo != "" {
		if _, err := task.ParseRepo(r.Repo); err != nil {
			return err
		}
	}
	if r.Issue != nil && r.Issue.Status != string(localissues.Open) && r.Issue.Status != string(localissues.Done) {
		return fmt.Errorf("issue status %q is not open or done", r.Issue.Status)
	}
	if r.Issue != nil && r.Source != task.Local {
		return errors.New("only a local task has issue fields")
	}
	if r.Work != nil {
		if err := r.Work.Validate(); err != nil {
			return fmt.Errorf("work: %w", err)
		}
	}
	return nil
}

// Encode writes the machine-readable payload.
func (r Record) Encode() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}

// Digest identifies the record's content. Equal records have equal digests.
func (r Record) Digest() string {
	raw, err := r.Encode()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Parse reads and validates a payload. It rejects another format, unknown fields,
// and more than one document.
func Parse(data []byte) (Record, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var r Record
	if err := dec.Decode(&r); err != nil {
		if errors.Is(err, io.EOF) {
			return Record{}, errors.New("not a foreman task record: the payload is empty")
		}
		return Record{}, fmt.Errorf("not a foreman task record: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Record{}, errors.New("not a foreman task record: more than one YAML document")
	}
	if err := r.validate(); err != nil {
		return Record{}, err
	}
	if r.Issue != nil {
		r.Issue.CreatedAt, r.Issue.UpdatedAt = r.Issue.CreatedAt.UTC(), r.Issue.UpdatedAt.UTC()
	}
	return r, nil
}

// ParseSection finds the record in a body of text: the last fenced block that
// parses. An earlier section that a crash left behind therefore never wins.
func ParseSection(text string) (Record, error) {
	var last Record
	var found bool
	for _, block := range fencedBlocks(text) {
		if r, err := Parse([]byte(block)); err == nil {
			last, found = r, true
		}
	}
	if !found {
		return Record{}, errors.New("no foreman task record found")
	}
	return last, nil
}

func fencedBlocks(text string) []string {
	var blocks []string
	var cur []string
	in := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if in {
				blocks = append(blocks, strings.Join(cur, "\n")+"\n")
				cur = nil
			}
			in = !in
			continue
		}
		if in {
			cur = append(cur, line)
		}
	}
	return blocks
}

// StatusLabel is the status as people read it: NEEDS_DECISION is "Needs decision".
func (r Record) StatusLabel() string {
	if r.Status == StatusInbox {
		return "Inbox"
	}
	s := strings.ToLower(strings.ReplaceAll(r.Status, "_", " "))
	return strings.ToUpper(s[:1]) + s[1:]
}

// Waiting is the unanswered questions, oldest first.
func (r Record) Waiting() []portable.Decision {
	if r.Work == nil {
		return nil
	}
	var out []portable.Decision
	for _, d := range r.Work.Decisions {
		if d.Answer == "" {
			out = append(out, d)
		}
	}
	return out
}

// QuestionText is the oldest unanswered question, for the Question field.
func (r Record) QuestionText() string {
	w := r.Waiting()
	if len(w) == 0 {
		return ""
	}
	return w[0].Question.Issue
}

// Commit is the newest commit, for the Commit field.
func (r Record) Commit() string {
	if r.Work == nil || len(r.Work.Commits) == 0 {
		return ""
	}
	return r.Work.Commits[len(r.Work.Commits)-1]
}

// Hydrate rebuilds the idle task record by the rules of `foreman issue import`: an
// open PR becomes a reset mark, a finished run stays done, and the saved plan seeds
// the next start. It returns nil when the record holds no work.
func (r Record) Hydrate(at time.Time) (*task.Record, error) {
	if r.Work == nil {
		return nil, nil
	}
	title := r.Title
	rec := portable.RecordFor(portable.Issue{ID: r.ID, Title: title, SourceURL: r.URL, Work: r.Work}, at)
	if r.Source == task.GitHub {
		repo, num, err := splitGitHubID(r.ID)
		if err != nil {
			return nil, err
		}
		rec.Key = task.Key{Repo: repo, Number: num, Source: task.GitHub}
		rec.Identifier = r.ID
	}
	return rec, nil
}

func splitGitHubID(id string) (task.Repo, int, error) {
	i := strings.LastIndex(id, "#")
	var n int
	if i < 0 {
		return "", 0, fmt.Errorf("id %q has no issue number", id)
	}
	if _, err := fmt.Sscanf(id[i+1:], "%d", &n); err != nil {
		return "", 0, err
	}
	repo, err := task.ParseRepo(id[:i])
	return repo, n, err
}

// LocalIssue is the local issue the record describes, or nil for a GitHub task.
func (r Record) LocalIssue() *localissues.Issue {
	if r.Source != task.Local || r.Issue == nil {
		return nil
	}
	return &localissues.Issue{ID: r.ID, Title: r.Title, Description: r.Issue.Description, SourceURL: r.URL,
		Status: localissues.Status(r.Issue.Status), Repo: r.Repo, Notes: r.Issue.Notes, Tags: r.Issue.Tags,
		CreatedAt: r.Issue.CreatedAt, UpdatedAt: r.Issue.UpdatedAt}
}
