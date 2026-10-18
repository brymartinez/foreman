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
	"regexp"
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
	if key.Source == task.Linear {
		return filepath.Join(m.Dir, fmt.Sprintf("%s-linear-%s.md", strings.ReplaceAll(string(key.Repo), "/", "-"), key.ExternalID))
	}
	return filepath.Join(m.Dir, fmt.Sprintf("%s-%d.md", strings.ReplaceAll(string(key.Repo), "/", "-"), key.Number))
}

// Path is the file that holds the task's record.
func (m Markdown) Path(key task.Key) string { return m.path(key) }

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
	if rec.Source == task.Linear && (!linearIssueID.MatchString(rec.ExternalID) || rec.Identifier == "") {
		return fmt.Errorf("linear issue needs a stable ID and identifier")
	}
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
		if recs[i].Source != recs[j].Source {
			return recs[i].Source < recs[j].Source
		}
		if recs[i].Source == task.Linear {
			return recs[i].ExternalID < recs[j].ExternalID
		}
		return recs[i].Number < recs[j].Number
	})
	return recs, nil
}

type frontmatter struct {
	Repo                string                 `yaml:"repo"`
	Number              int                    `yaml:"number"`
	Source              task.Source            `yaml:"issue_source,omitempty"`
	ExternalID          string                 `yaml:"external_issue_id,omitempty"`
	Identifier          string                 `yaml:"issue_identifier,omitempty"`
	Kind                task.Kind              `yaml:"kind"`
	Title               string                 `yaml:"title"`
	URL                 string                 `yaml:"url"`
	State               task.State             `yaml:"state"`
	ImplementationAgent task.Agent             `yaml:"implementation_agent,omitempty"`
	Profile             *task.Profile          `yaml:"profile,omitempty"`
	RouteReason         string                 `yaml:"route_reason,omitempty"`
	RouteLogged         bool                   `yaml:"route_logged,omitempty"`
	StartedAt           *time.Time             `yaml:"started_at,omitempty"`
	SessionID           string                 `yaml:"session_id,omitempty"`
	ReviewAgent         task.Agent             `yaml:"review_agent,omitempty"`
	Account             string                 `yaml:"account,omitempty"`
	AccountEmail        string                 `yaml:"account_email,omitempty"`
	Branch              string                 `yaml:"branch,omitempty"`
	BaseRef             string                 `yaml:"base_ref,omitempty"`
	RepoDir             string                 `yaml:"repo_dir,omitempty"`
	Worktree            string                 `yaml:"worktree,omitempty"`
	WorktreeRemovedAt   *time.Time             `yaml:"worktree_removed_at,omitempty"`
	ContextFile         string                 `yaml:"context_file,omitempty"`
	Launch              *task.Launch           `yaml:"launch,omitempty"`
	RunnerPID           int                    `yaml:"runner_pid,omitempty"`
	RunnerLog           string                 `yaml:"runner_log,omitempty"`
	PR                  int                    `yaml:"pr,omitempty"`
	PRURL               string                 `yaml:"pr_url,omitempty"`
	PriorPR             int                    `yaml:"prior_pr,omitempty"`
	PRHead              string                 `yaml:"pr_head,omitempty"`
	PRDigest            string                 `yaml:"pr_digest,omitempty"`
	CheckpointHead      string                 `yaml:"checkpoint_head,omitempty"`
	CheckpointNext      string                 `yaml:"checkpoint_next,omitempty"`
	Checkpoints         int                    `yaml:"checkpoints,omitempty"`
	ConflictAttempts    []task.ConflictAttempt `yaml:"conflict_attempts,omitempty"`
	ConflictPending     bool                   `yaml:"conflict_pending,omitempty"`
	ReviewBeforePR      bool                   `yaml:"review_before_pr,omitempty"`
	LocalHead           string                 `yaml:"local_head,omitempty"`
	ProposedTitle       string                 `yaml:"proposed_title,omitempty"`
	ProposedBody        string                 `yaml:"proposed_body,omitempty"`
	GitHub              *snapshot              `yaml:"github,omitempty"`
	LastGitHubSync      *time.Time             `yaml:"last_github_sync,omitempty"`
	Decisions           []decisionFM           `yaml:"decisions,omitempty"`
	LastOutcome         *outcomeFM             `yaml:"last_outcome,omitempty"`
	Usage               usageFM                `yaml:"usage,omitempty"`
	Validation          []task.Check           `yaml:"validation,omitempty"`
	Reviews             []reviewFM             `yaml:"reviews,omitempty"`
	Plan                []task.PlanComment     `yaml:"plan,omitempty"`
	Feedback            []feedbackFM           `yaml:"feedback,omitempty"`
	CI                  *ciFM                  `yaml:"ci,omitempty"`
	Attempts            []task.Attempt         `yaml:"attempts,omitempty"`
	Reset               *resetFM               `yaml:"reset,omitempty"`
}

type resetFM struct {
	At      time.Time          `yaml:"at"`
	PR      int                `yaml:"pr,omitempty"`
	PRURL   string             `yaml:"pr_url,omitempty"`
	Branch  string             `yaml:"branch,omitempty"`
	PRState string             `yaml:"pr_state,omitempty"`
	Cleanup *task.ResetCleanup `yaml:"cleanup,omitempty"`
}

var linearIssueID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type feedbackFM struct {
	Kind      task.CommentKind `yaml:"kind"`
	ID        int64            `yaml:"id"`
	Author    string           `yaml:"author,omitempty"`
	URL       string           `yaml:"url,omitempty"`
	At        time.Time        `yaml:"at"`
	Digest    string           `yaml:"digest"`
	HandledAt *time.Time       `yaml:"handled_at,omitempty"`
}

type ciFM struct {
	State     task.CIState    `yaml:"state"`
	Failing   []task.CheckRun `yaml:"failing,omitempty"`
	CheckedAt time.Time       `yaml:"checked_at"`
}

type reviewFM struct {
	Round    int             `yaml:"round"`
	Kind     task.ReviewKind `yaml:"kind,omitempty"`
	Reviewer task.Agent      `yaml:"reviewer"`
	PR       int             `yaml:"pr"`
	Verdict  string          `yaml:"verdict"`
	Summary  string          `yaml:"summary"`
	Findings []task.Finding  `yaml:"findings,omitempty"`
	At       time.Time       `yaml:"at"`
	Head     string          `yaml:"head,omitempty"`
	Proposal string          `yaml:"proposal,omitempty"`
}

type outcomeFM struct {
	Status  string    `yaml:"status"`
	Summary string    `yaml:"summary"`
	At      time.Time `yaml:"at"`
}

type usageFM struct {
	ClaudeCostUSD        float64        `yaml:"claude_cost_usd,omitempty"`
	CodexInputTokens     int64          `yaml:"codex_input_tokens,omitempty"`
	CodexCachedTokens    int64          `yaml:"codex_cached_tokens,omitempty"`
	CodexOutputTokens    int64          `yaml:"codex_output_tokens,omitempty"`
	CodexReasoningTokens int64          `yaml:"codex_reasoning_tokens,omitempty"`
	ClaudeTurns          int            `yaml:"claude_turns,omitempty"`
	CodexTurns           int            `yaml:"codex_turns,omitempty"`
	ToolCalls            map[string]int `yaml:"tool_calls,omitempty"`
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
	Delivered  *time.Time    `yaml:"delivered_at,omitempty"`
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
	if fm.Source == task.Linear {
		if !linearIssueID.MatchString(fm.ExternalID) || fm.Identifier == "" {
			return nil, fmt.Errorf("linear issue needs a stable ID and identifier")
		}
	} else if fm.Source != "" && fm.Source != task.GitHub {
		return nil, fmt.Errorf("unknown issue source %q", fm.Source)
	} else if fm.Number <= 0 {
		return nil, fmt.Errorf("number must be positive, got %d", fm.Number)
	}
	if fm.Source == task.GitHub {
		fm.Source = ""
	}
	if fm.Kind != task.Issue && fm.Kind != task.PullRequest {
		return nil, fmt.Errorf("kind must be issue or pr, got %q", fm.Kind)
	}
	if !validState(fm.State) {
		return nil, fmt.Errorf("unknown state %q", fm.State)
	}
	rec := &task.Record{
		Key:        task.Key{Repo: repo, Number: fm.Number, Source: fm.Source, ExternalID: fm.ExternalID},
		Identifier: fm.Identifier,
		Kind:       fm.Kind,
		Title:      fm.Title,
		URL:        fm.URL,
		State:      fm.State,
		Body:       body,
	}
	if fm.ImplementationAgent != "" {
		impl, err := task.ParseAgent(string(fm.ImplementationAgent))
		if err != nil {
			return nil, fmt.Errorf("implementation_agent: %w", err)
		}
		rec.Delegation = &task.Delegation{
			Implementer:      task.Session{Agent: impl, ID: fm.SessionID},
			Profile:          fm.Profile,
			RouteReason:      fm.RouteReason,
			RouteLogged:      fm.RouteLogged,
			Reviewer:         impl.Reviewer(),
			Account:          fm.Account,
			AccountEmail:     fm.AccountEmail,
			Branch:           fm.Branch,
			BaseRef:          fm.BaseRef,
			RepoDir:          fm.RepoDir,
			Worktree:         fm.Worktree,
			ContextFile:      fm.ContextFile,
			Launch:           fm.Launch,
			RunnerPID:        fm.RunnerPID,
			RunnerLog:        fm.RunnerLog,
			PR:               fm.PR,
			PRURL:            fm.PRURL,
			PriorPR:          fm.PriorPR,
			PRHead:           fm.PRHead,
			PRDigest:         fm.PRDigest,
			CheckpointHead:   fm.CheckpointHead,
			CheckpointNext:   fm.CheckpointNext,
			Checkpoints:      fm.Checkpoints,
			ConflictAttempts: fm.ConflictAttempts,
			ConflictPending:  fm.ConflictPending,
			ReviewBeforePR:   fm.ReviewBeforePR,
			LocalHead:        fm.LocalHead,
			ProposedTitle:    fm.ProposedTitle,
			ProposedBody:     fm.ProposedBody,
		}
		if fm.StartedAt != nil {
			rec.Delegation.StartedAt = *fm.StartedAt
		}
		if fm.WorktreeRemovedAt != nil {
			rec.Delegation.WorktreeRemovedAt = *fm.WorktreeRemovedAt
		}
		if fm.Profile != nil && fm.Profile.Agent != impl {
			return nil, fmt.Errorf("profile %q is for %q, but implementation_agent is %q", fm.Profile.Name, fm.Profile.Agent, impl)
		}
		if fm.ReviewAgent != "" && fm.ReviewAgent != impl.Reviewer() && fm.ReviewAgent != impl {
			return nil, fmt.Errorf("review_agent must be %q or %q, got %q", impl.Reviewer(), impl, fm.ReviewAgent)
		}
		if fm.ReviewAgent != "" {
			rec.Delegation.Reviewer = fm.ReviewAgent
		}
	}
	if fm.GitHub != nil {
		rec.GitHub = &task.Snapshot{UpdatedAt: fm.GitHub.UpdatedAt, State: fm.GitHub.State, Reasons: fm.GitHub.Reasons}
	}
	if fm.LastGitHubSync != nil {
		rec.LastGitHubSync = *fm.LastGitHubSync
	}
	rec.Validation = fm.Validation
	rec.Plan = fm.Plan
	for _, f := range fm.Feedback {
		switch f.Kind {
		case task.IssueComment, task.ReviewComment, task.ReviewSummary:
		default:
			return nil, fmt.Errorf("feedback %d has unknown kind %q", f.ID, f.Kind)
		}
		item := task.Feedback{Kind: f.Kind, ID: f.ID, Author: f.Author, URL: f.URL, At: f.At, Digest: f.Digest}
		if f.HandledAt != nil {
			item.HandledAt = *f.HandledAt
		}
		rec.Feedback = append(rec.Feedback, item)
	}
	for _, r := range fm.Reviews {
		if r.Verdict != "clean" && r.Verdict != "changes_requested" {
			return nil, fmt.Errorf("review round %d has unknown verdict %q", r.Round, r.Verdict)
		}
		// An unknown kind is refused rather than read as competing, which could pass the gate.
		if r.Kind != task.CompetingReview && r.Kind != task.SelfReview {
			return nil, fmt.Errorf("review round %d has unknown kind %q", r.Round, r.Kind)
		}
		rec.Reviews = append(rec.Reviews, task.Review{Round: r.Round, Kind: r.Kind, Reviewer: r.Reviewer, PR: r.PR, Clean: r.Verdict == "clean", Summary: r.Summary, Findings: r.Findings, At: r.At, Head: r.Head, Proposal: r.Proposal})
	}
	if c := fm.CI; c != nil {
		switch c.State {
		case task.CIPending, task.CIPassing, task.CIFailing, task.CINone:
		default:
			return nil, fmt.Errorf("ci has unknown state %q", c.State)
		}
		rec.CI = &task.CIStatus{State: c.State, Failing: c.Failing, CheckedAt: c.CheckedAt}
	}
	for _, a := range fm.Attempts {
		for _, m := range a.Marks {
			if !task.ValidPhase(m.Phase) {
				return nil, fmt.Errorf("attempt mark at %s has unknown phase %q", m.At.Format(time.RFC3339), m.Phase)
			}
		}
	}
	rec.Attempts = fm.Attempts
	if r := fm.Reset; r != nil {
		rec.Reset = &task.ResetMark{At: r.At, PR: r.PR, PRURL: r.PRURL, Branch: r.Branch, PRState: r.PRState, Cleanup: r.Cleanup}
	}
	if o := fm.LastOutcome; o != nil {
		rec.LastOutcome = &task.Outcome{Status: o.Status, Summary: o.Summary, At: o.At}
	}
	rec.Usage = task.Usage{ClaudeCostUSD: fm.Usage.ClaudeCostUSD, CodexInputTokens: fm.Usage.CodexInputTokens, CodexCachedTokens: fm.Usage.CodexCachedTokens, CodexOutputTokens: fm.Usage.CodexOutputTokens, CodexReasoningTokens: fm.Usage.CodexReasoningTokens, ClaudeTurns: fm.Usage.ClaudeTurns, CodexTurns: fm.Usage.CodexTurns, ToolCalls: fm.Usage.ToolCalls}
	for _, d := range fm.Decisions {
		dec := task.Decision{AskedAt: d.AskedAt, Question: d.Question, Answer: d.Answer}
		if d.AnsweredAt != nil {
			dec.AnsweredAt = *d.AnsweredAt
		}
		if d.Delivered != nil {
			dec.DeliveredAt = *d.Delivered
		}
		rec.Decisions = append(rec.Decisions, dec)
	}
	return rec, nil
}

func validState(s task.State) bool {
	switch s {
	case task.Discovered, task.Working, task.NeedsDecision, task.Reviewing, task.ReadyForHuman, task.Reviewed, task.Done, task.Failed, task.PRClosed:
		return true
	}
	return false
}

func fromRecord(rec *task.Record) frontmatter {
	fm := frontmatter{
		Repo:       string(rec.Repo),
		Number:     rec.Number,
		Source:     rec.Source,
		ExternalID: rec.ExternalID,
		Identifier: rec.Identifier,
		Kind:       rec.Kind,
		Title:      rec.Title,
		URL:        rec.URL,
		State:      rec.State,
	}
	if d := rec.Delegation; d != nil {
		fm.ImplementationAgent = d.Implementer.Agent
		fm.Profile, fm.RouteReason, fm.RouteLogged = d.Profile, d.RouteReason, d.RouteLogged
		if !d.StartedAt.IsZero() {
			t := d.StartedAt
			fm.StartedAt = &t
		}
		fm.SessionID = d.Implementer.ID
		fm.ReviewAgent = d.Reviewer
		fm.Account, fm.AccountEmail = d.Account, d.AccountEmail
		fm.Branch, fm.BaseRef, fm.Worktree, fm.ContextFile = d.Branch, d.BaseRef, d.Worktree, d.ContextFile
		fm.RepoDir = d.RepoDir
		if !d.WorktreeRemovedAt.IsZero() {
			t := d.WorktreeRemovedAt
			fm.WorktreeRemovedAt = &t
		}
		fm.Launch = d.Launch
		fm.RunnerPID, fm.RunnerLog, fm.PR, fm.PRURL, fm.PRHead = d.RunnerPID, d.RunnerLog, d.PR, d.PRURL, d.PRHead
		fm.PriorPR, fm.PRDigest = d.PriorPR, d.PRDigest
		fm.CheckpointHead, fm.CheckpointNext, fm.Checkpoints = d.CheckpointHead, d.CheckpointNext, d.Checkpoints
		fm.ConflictAttempts, fm.ConflictPending = d.ConflictAttempts, d.ConflictPending
		fm.ReviewBeforePR, fm.LocalHead, fm.ProposedTitle, fm.ProposedBody = d.ReviewBeforePR, d.LocalHead, d.ProposedTitle, d.ProposedBody
	}
	if s := rec.GitHub; s != nil {
		fm.GitHub = &snapshot{UpdatedAt: s.UpdatedAt, State: s.State, Reasons: s.Reasons}
	}
	if !rec.LastGitHubSync.IsZero() {
		t := rec.LastGitHubSync
		fm.LastGitHubSync = &t
	}
	fm.Validation = rec.Validation
	fm.Plan = rec.Plan
	fm.Attempts = rec.Attempts
	if r := rec.Reset; r != nil {
		fm.Reset = &resetFM{At: r.At, PR: r.PR, PRURL: r.PRURL, Branch: r.Branch, PRState: r.PRState, Cleanup: r.Cleanup}
	}
	for _, f := range rec.Feedback {
		item := feedbackFM{Kind: f.Kind, ID: f.ID, Author: f.Author, URL: f.URL, At: f.At, Digest: f.Digest}
		if !f.HandledAt.IsZero() {
			t := f.HandledAt
			item.HandledAt = &t
		}
		fm.Feedback = append(fm.Feedback, item)
	}
	for _, r := range rec.Reviews {
		verdict := "changes_requested"
		if r.Clean {
			verdict = "clean"
		}
		fm.Reviews = append(fm.Reviews, reviewFM{Round: r.Round, Kind: r.Kind, Reviewer: r.Reviewer, PR: r.PR, Verdict: verdict, Summary: r.Summary, Findings: r.Findings, At: r.At, Head: r.Head, Proposal: r.Proposal})
	}
	if c := rec.CI; c != nil {
		fm.CI = &ciFM{State: c.State, Failing: c.Failing, CheckedAt: c.CheckedAt}
	}
	if o := rec.LastOutcome; o != nil {
		fm.LastOutcome = &outcomeFM{Status: o.Status, Summary: o.Summary, At: o.At}
	}
	fm.Usage = usageFM{ClaudeCostUSD: rec.Usage.ClaudeCostUSD, CodexInputTokens: rec.Usage.CodexInputTokens, CodexCachedTokens: rec.Usage.CodexCachedTokens, CodexOutputTokens: rec.Usage.CodexOutputTokens, CodexReasoningTokens: rec.Usage.CodexReasoningTokens, ClaudeTurns: rec.Usage.ClaudeTurns, CodexTurns: rec.Usage.CodexTurns, ToolCalls: rec.Usage.ToolCalls}
	for _, d := range rec.Decisions {
		dec := decisionFM{AskedAt: d.AskedAt, Question: d.Question, Answer: d.Answer}
		if !d.AnsweredAt.IsZero() {
			t := d.AnsweredAt
			dec.AnsweredAt = &t
		}
		if !d.DeliveredAt.IsZero() {
			t := d.DeliveredAt
			dec.Delivered = &t
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
