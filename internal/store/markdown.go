// Package store persists task records as Markdown files with YAML frontmatter.
package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/brymartinez/foreman/internal/task"
)

// Markdown stores one file per task in Dir. Frontmatter keys foreman does not own
// (Obsidian tags, aliases, anything the user adds) survive saves with their comments.
type Markdown struct {
	Dir string
}

func (m Markdown) path(key task.Key) string {
	return filepath.Join(m.Dir, fmt.Sprintf("%s-%d.md", strings.ReplaceAll(string(key.Repo), "/", "-"), key.Number))
}

func (m Markdown) Get(_ context.Context, key task.Key) (*task.Record, error) {
	path := m.path(key)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rec, err := decode(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if rec == nil || rec.Key != key {
		return nil, fmt.Errorf("%s: holds a different task than %s#%d", path, key.Repo, key.Number)
	}
	return rec, nil
}

func (m Markdown) Save(_ context.Context, rec *task.Record) error {
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return err
	}
	path := m.path(rec.Key)
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	out, err := encode(rec, existing)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	tmp, err := os.CreateTemp(m.Dir, ".foreman-*.md")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Update serializes read-modify-write across processes with a lock file in Dir.
// The runner and inbox both write delegated tasks, so neither may save a stale copy.
func (m Markdown) Update(ctx context.Context, key task.Key, fn func(cur *task.Record) (*task.Record, error)) (*task.Record, error) {
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(m.Dir, ".foreman.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	cur, err := m.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	next, err := fn(cur)
	if err != nil || next == nil {
		return next, err
	}
	return next, m.Save(ctx, next)
}

// List returns every task file in Dir, sorted by repo then number.
// Markdown files without a task frontmatter are ignored, so Dir can live inside a vault.
func (m Markdown) List(_ context.Context) ([]*task.Record, error) {
	entries, err := os.ReadDir(m.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var recs []*task.Record
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(m.Dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		rec, err := decode(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if rec != nil {
			recs = append(recs, rec)
		}
	}
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].Repo != recs[j].Repo {
			return recs[i].Repo < recs[j].Repo
		}
		return recs[i].Number < recs[j].Number
	})
	return recs, nil
}

type frontmatter struct {
	Repo                string       `yaml:"repo"`
	Number              int          `yaml:"number"`
	Kind                task.Kind    `yaml:"kind"`
	Title               string       `yaml:"title"`
	URL                 string       `yaml:"url"`
	State               task.State   `yaml:"state"`
	ImplementationAgent task.Agent   `yaml:"implementation_agent,omitempty"`
	SessionID           string       `yaml:"session_id,omitempty"`
	ReviewAgent         task.Agent   `yaml:"review_agent,omitempty"`
	Branch              string       `yaml:"branch,omitempty"`
	BaseRef             string       `yaml:"base_ref,omitempty"`
	Worktree            string       `yaml:"worktree,omitempty"`
	ContextFile         string       `yaml:"context_file,omitempty"`
	RunnerPID           int          `yaml:"runner_pid,omitempty"`
	RunnerLog           string       `yaml:"runner_log,omitempty"`
	PR                  int          `yaml:"pr,omitempty"`
	GitHub              *snapshot    `yaml:"github,omitempty"`
	LastGitHubSync      *time.Time   `yaml:"last_github_sync,omitempty"`
	Decisions           []decisionFM `yaml:"decisions,omitempty"`
	LastOutcome         *outcomeFM   `yaml:"last_outcome,omitempty"`
}

type outcomeFM struct {
	Status  string    `yaml:"status"`
	Summary string    `yaml:"summary"`
	At      time.Time `yaml:"at"`
}

type snapshot struct {
	UpdatedAt time.Time          `yaml:"updated_at"`
	State     string             `yaml:"state"`
	Reasons   []task.InboxReason `yaml:"reasons,flow"`
}

type decisionFM struct {
	AskedAt    time.Time     `yaml:"asked_at"`
	Question   task.Question `yaml:"question"`
	Answer     string        `yaml:"answer,omitempty"`
	AnsweredAt *time.Time    `yaml:"answered_at,omitempty"`
}

// ownedKeys are the frontmatter keys foreman writes; every other key belongs to the user.
var ownedKeys = map[string]bool{}

func init() {
	for _, f := range yamlKeys(frontmatter{}) {
		ownedKeys[f] = true
	}
}

func split(raw []byte) (fm, body []byte, ok bool) {
	text := string(raw)
	if !strings.HasPrefix(text, "---\n") {
		return nil, raw, false
	}
	inner := text[4:]
	end := strings.Index(inner, "\n---\n")
	switch {
	case end >= 0:
		return []byte(inner[:end+1]), []byte(inner[end+5:]), true
	case strings.HasSuffix(inner, "\n---"):
		return []byte(strings.TrimSuffix(inner, "---")), nil, true
	}
	return nil, raw, false
}

// decode returns nil for Markdown that is not a task file.
func decode(raw []byte) (*task.Record, error) {
	fmRaw, body, ok := split(raw)
	if !ok {
		return nil, nil
	}
	var fm frontmatter
	if err := yaml.Unmarshal(fmRaw, &fm); err != nil {
		return nil, fmt.Errorf("frontmatter: %w", err)
	}
	if fm.Repo == "" {
		return nil, nil
	}
	return fm.toRecord(string(body))
}

func (fm frontmatter) toRecord(body string) (*task.Record, error) {
	repo, err := task.ParseRepo(fm.Repo)
	if err != nil {
		return nil, err
	}
	if fm.Number <= 0 {
		return nil, fmt.Errorf("number must be positive, got %d", fm.Number)
	}
	if fm.Kind != task.Issue && fm.Kind != task.PullRequest {
		return nil, fmt.Errorf("kind must be issue or pr, got %q", fm.Kind)
	}
	if !validState(fm.State) {
		return nil, fmt.Errorf("unknown state %q", fm.State)
	}
	rec := &task.Record{
		Key:   task.Key{Repo: repo, Number: fm.Number},
		Kind:  fm.Kind,
		Title: fm.Title,
		URL:   fm.URL,
		State: fm.State,
		Body:  body,
	}
	if fm.ImplementationAgent != "" {
		impl, err := task.ParseAgent(string(fm.ImplementationAgent))
		if err != nil {
			return nil, fmt.Errorf("implementation_agent: %w", err)
		}
		rec.Delegation = &task.Delegation{
			Implementer: task.Session{Agent: impl, ID: fm.SessionID},
			Reviewer:    impl.Reviewer(),
			Branch:      fm.Branch,
			BaseRef:     fm.BaseRef,
			Worktree:    fm.Worktree,
			ContextFile: fm.ContextFile,
			RunnerPID:   fm.RunnerPID,
			RunnerLog:   fm.RunnerLog,
			PR:          fm.PR,
		}
		if fm.ReviewAgent != "" && fm.ReviewAgent != impl.Reviewer() {
			return nil, fmt.Errorf("review_agent must be the competing agent %q, got %q", impl.Reviewer(), fm.ReviewAgent)
		}
	}
	if fm.GitHub != nil {
		rec.GitHub = &task.Snapshot{UpdatedAt: fm.GitHub.UpdatedAt, State: fm.GitHub.State, Reasons: fm.GitHub.Reasons}
	}
	if fm.LastGitHubSync != nil {
		rec.LastGitHubSync = *fm.LastGitHubSync
	}
	if o := fm.LastOutcome; o != nil {
		rec.LastOutcome = &task.Outcome{Status: o.Status, Summary: o.Summary, At: o.At}
	}
	for _, d := range fm.Decisions {
		dec := task.Decision{AskedAt: d.AskedAt, Question: d.Question, Answer: d.Answer}
		if d.AnsweredAt != nil {
			dec.AnsweredAt = *d.AnsweredAt
		}
		rec.Decisions = append(rec.Decisions, dec)
	}
	return rec, nil
}

func validState(s task.State) bool {
	switch s {
	case task.Discovered, task.Working, task.NeedsDecision, task.Reviewing, task.ReadyForHuman, task.Done, task.Failed:
		return true
	}
	return false
}

func fromRecord(rec *task.Record) frontmatter {
	fm := frontmatter{
		Repo:   string(rec.Repo),
		Number: rec.Number,
		Kind:   rec.Kind,
		Title:  rec.Title,
		URL:    rec.URL,
		State:  rec.State,
	}
	if d := rec.Delegation; d != nil {
		fm.ImplementationAgent = d.Implementer.Agent
		fm.SessionID = d.Implementer.ID
		fm.ReviewAgent = d.Reviewer
		fm.Branch, fm.BaseRef, fm.Worktree, fm.ContextFile = d.Branch, d.BaseRef, d.Worktree, d.ContextFile
		fm.RunnerPID, fm.RunnerLog, fm.PR = d.RunnerPID, d.RunnerLog, d.PR
	}
	if s := rec.GitHub; s != nil {
		fm.GitHub = &snapshot{UpdatedAt: s.UpdatedAt, State: s.State, Reasons: s.Reasons}
	}
	if !rec.LastGitHubSync.IsZero() {
		t := rec.LastGitHubSync
		fm.LastGitHubSync = &t
	}
	if o := rec.LastOutcome; o != nil {
		fm.LastOutcome = &outcomeFM{Status: o.Status, Summary: o.Summary, At: o.At}
	}
	for _, d := range rec.Decisions {
		dec := decisionFM{AskedAt: d.AskedAt, Question: d.Question, Answer: d.Answer}
		if !d.AnsweredAt.IsZero() {
			t := d.AnsweredAt
			dec.AnsweredAt = &t
		}
		fm.Decisions = append(fm.Decisions, dec)
	}
	return fm
}

// encode writes rec over the frontmatter in existing (if any). Owned keys are replaced
// in place or removed; user keys, their comments, and their order are kept.
func encode(rec *task.Record, existing []byte) ([]byte, error) {
	var ours yaml.Node
	if err := ours.Encode(fromRecord(rec)); err != nil {
		return nil, err
	}
	merged := &ours
	if fmRaw, _, ok := split(existing); ok {
		var doc yaml.Node
		if err := yaml.Unmarshal(fmRaw, &doc); err == nil && len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
			merged = mergeOwned(doc.Content[0], &ours)
		}
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(merged); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	buf.WriteString("---\n")
	buf.WriteString(rec.Body)
	return buf.Bytes(), nil
}

func mergeOwned(base, ours *yaml.Node) *yaml.Node {
	values := map[string]*yaml.Node{}
	for i := 0; i+1 < len(ours.Content); i += 2 {
		values[ours.Content[i].Value] = ours.Content[i+1]
	}
	var content []*yaml.Node
	seen := map[string]bool{}
	for i := 0; i+1 < len(base.Content); i += 2 {
		k, v := base.Content[i], base.Content[i+1]
		if !ownedKeys[k.Value] {
			content = append(content, k, v)
			continue
		}
		if nv, ok := values[k.Value]; ok {
			nv.HeadComment, nv.LineComment, nv.FootComment = v.HeadComment, v.LineComment, v.FootComment
			content = append(content, k, nv)
			seen[k.Value] = true
		}
	}
	for i := 0; i+1 < len(ours.Content); i += 2 {
		if !seen[ours.Content[i].Value] {
			content = append(content, ours.Content[i], ours.Content[i+1])
		}
	}
	base.Content = content
	return base
}

func yamlKeys(v any) []string {
	t := reflect.TypeOf(v)
	keys := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		keys = append(keys, name)
	}
	return keys
}
