package knowledge

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// FormatVersion names the export payload's schema. Parse rejects any other.
const FormatVersion = "foreman-knowledge/1"

// payloadMarker sits on its own line just before the payload's fenced block, so
// Parse finds the data without reading the guidance above it.
const payloadMarker = "<!-- foreman-knowledge-payload -->"

// ErrNoKnowledge means the repository has no learnings and no evaluations.
var ErrNoKnowledge = errors.New("knowledge: the repository has no knowledge")

// Snapshot is a repository's whole learning state: what an export carries and what
// an ingest stores. It leaves out attempts and the lock, which belong to one machine.
type Snapshot struct {
	Format      string       `yaml:"format"`
	Repo        string       `yaml:"repo"`
	ExportedAt  time.Time    `yaml:"exported_at"`
	NextID      int          `yaml:"next_id"`
	Learnings   []Learning   `yaml:"learnings"`
	Evaluations []Evaluation `yaml:"evaluations"`
}

// Empty reports whether the snapshot holds no knowledge at all.
func (x Snapshot) Empty() bool { return len(x.Learnings) == 0 && len(x.Evaluations) == 0 }

// Guidance is the knowledge.md the snapshot's active learnings render to.
func (x Snapshot) Guidance() string { return render(x.Learnings) }

// Snapshot reads repo's learnings and every evaluation, oldest task first.
func (s Store) Snapshot(repo string) (Snapshot, error) {
	f, err := s.load(repo)
	if err != nil {
		return Snapshot{}, err
	}
	evs, err := s.Evaluations(repo, 0)
	if err != nil {
		return Snapshot{}, err
	}
	sortEvaluations(evs)
	return Snapshot{Format: FormatVersion, Repo: repo, NextID: f.NextID, Learnings: f.Learnings, Evaluations: evs}, nil
}

func sortEvaluations(evs []Evaluation) {
	slices.SortStableFunc(evs, func(a, b Evaluation) int {
		if c := a.TaskAt.Compare(b.TaskAt); c != 0 {
			return c
		}
		return strings.Compare(a.Task, b.Task)
	})
}

// Format writes x as the export file: the active guidance for people to read, then
// the payload in a fenced YAML block for Parse.
func Format(x Snapshot) ([]byte, error) {
	x.Format = FormatVersion
	payload, err := yaml.Marshal(x)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Foreman knowledge export\n\n")
	fmt.Fprintf(&b, "Repository: %s\n", x.Repo)
	if !x.ExportedAt.IsZero() {
		fmt.Fprintf(&b, "Exported: %s\n", x.ExportedAt.UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "Learnings: %d active, %d candidate, %d retired. Evaluations: %d.\n\n", count(x.Learnings, Active), count(x.Learnings, Candidate), count(x.Learnings, Retired), len(x.Evaluations))
	b.WriteString("Ingest this file on another machine with `foreman knowledge ingest FILE`. ")
	b.WriteString("The guidance below is for reading. The YAML block at the end is the data, and editing it can make the file invalid.\n\n")
	// The rendered guidance's headings move down one level under this file's title.
	for _, line := range strings.SplitAfter(render(x.Learnings), "\n") {
		if strings.HasPrefix(line, "#") {
			line = "#" + line
		}
		b.WriteString(line)
	}
	fmt.Fprintf(&b, "\n## Data\n\n%s\n```yaml\n%s```\n", payloadMarker, payload)
	return b.Bytes(), nil
}

func count(ls []Learning, status Status) int {
	n := 0
	for _, l := range ls {
		if l.Status == status {
			n++
		}
	}
	return n
}

// Parse reads an export written by Format and validates all of it, so an ingest
// never stores a record the learner could not have written.
func Parse(data []byte) (Snapshot, error) {
	raw, err := payload(data)
	if err != nil {
		return Snapshot{}, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var x Snapshot
	if err := dec.Decode(&x); err != nil {
		return Snapshot{}, fmt.Errorf("knowledge: invalid export: %w", err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Snapshot{}, errors.New("knowledge: invalid export: more than one YAML document")
	}
	if err := x.validate(); err != nil {
		return Snapshot{}, fmt.Errorf("knowledge: invalid export: %w", err)
	}
	return x, nil
}

func payload(data []byte) ([]byte, error) {
	lines := strings.SplitAfter(string(data), "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimRight(line, "\r\n") == payloadMarker {
			start = i
		}
	}
	if start < 0 || start+1 >= len(lines) || strings.TrimRight(lines[start+1], "\r\n") != "```yaml" {
		return nil, errors.New("knowledge: not a foreman knowledge export: no payload block")
	}
	var b strings.Builder
	for _, line := range lines[start+2:] {
		if strings.TrimRight(line, "\r\n") == "```" {
			return []byte(b.String()), nil
		}
		b.WriteString(line)
	}
	return nil, errors.New("knowledge: not a foreman knowledge export: the payload block is not closed")
}

func (x Snapshot) validate() error {
	if x.Format != FormatVersion {
		return fmt.Errorf("unsupported format %q (want %s)", x.Format, FormatVersion)
	}
	if !ValidRepo(x.Repo) {
		return fmt.Errorf("invalid repository %q", x.Repo)
	}
	if x.NextID < 0 {
		return errors.New("negative next_id")
	}
	ids := map[string]bool{}
	for i, l := range x.Learnings {
		at := fmt.Sprintf("learning %d", i+1)
		switch {
		case !validName(l.ID, taskID):
			return fmt.Errorf("%s: invalid id %q", at, l.ID)
		case ids[l.ID]:
			return fmt.Errorf("%s: duplicate id %q", at, l.ID)
		case !validKind(l.Kind):
			return fmt.Errorf("%s: unknown kind %q", at, l.Kind)
		case l.Status != Candidate && l.Status != Active && l.Status != Retired:
			return fmt.Errorf("%s: unknown status %q", at, l.Status)
		}
		ids[l.ID] = true
		if text, err := Sanitize(l.Text); err != nil || text != l.Text {
			return fmt.Errorf("%s: text is not fit for knowledge.md", at)
		}
		for j, e := range l.Evidence {
			if !validName(e.Task, taskID) || !validSource(e.Source) {
				return fmt.Errorf("%s: evidence %d has an invalid task or source", at, j+1)
			}
		}
		for j, c := range l.History {
			if !validName(c.Task, taskID) || c.Action == "" {
				return fmt.Errorf("%s: history %d has an invalid task or no action", at, j+1)
			}
		}
	}
	tasks := map[string]bool{}
	for i, ev := range x.Evaluations {
		at := fmt.Sprintf("evaluation %d", i+1)
		if !validName(ev.Task, taskID) {
			return fmt.Errorf("%s: invalid task id %q", at, ev.Task)
		}
		if tasks[ev.Task] {
			return fmt.Errorf("%s: duplicate task %q", at, ev.Task)
		}
		tasks[ev.Task] = true
		for j, p := range ev.Proposals {
			if !validKind(p.Kind) || (p.Scope != Reusable && p.Scope != OneOff) || !validSource(p.Source) {
				return fmt.Errorf("%s: proposal %d has an unknown kind, scope, or source", at, j+1)
			}
		}
		for j, r := range ev.Revisions {
			if (r.Action != Revise && r.Action != Retire) || !validSource(r.Source) {
				return fmt.Errorf("%s: revision %d has an unknown action or source", at, j+1)
			}
		}
	}
	return nil
}

func validSource(s Source) bool { return s == Maintainer || s == Observed }

// ValidRepo reports whether repo is an owner/repo name the store accepts.
func ValidRepo(repo string) bool {
	_, err := Store{}.repoDir(repo)
	return err == nil
}

// idNumber is the counter in a learning id such as L12, or 0 for any other id.
func idNumber(id string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "L"))
	if err != nil || !strings.HasPrefix(id, "L") {
		return 0
	}
	return n
}
