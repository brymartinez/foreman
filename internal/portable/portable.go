package portable

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/localissues"
	"github.com/brymartinez/foreman/internal/store"
	"github.com/brymartinez/foreman/internal/task"
)

// Service reads and writes portable files against one database.
type Service struct {
	DB  *sql.DB
	Now func() time.Time
	// Guidance refreshes a repository's rendered knowledge.md after an import
	// changed its learnings. Optional.
	Guidance func(repo string)
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC().Truncate(time.Second)
	}
	return time.Now().UTC().Truncate(time.Second)
}

func (s Service) tasks() store.SQLite { return store.SQLite{DB: s.DB, Now: s.Now} }

// ExportOptions selects what an export carries.
type ExportOptions struct {
	// IssueID limits the export to one issue. Empty exports all local work.
	IssueID string
	// Learnings adds each repository's curated learnings. Only a full export can.
	Learnings bool
}

func triageKey(id string) string { return "local/" + strings.ToLower(id) }

// Export reads one consistent snapshot of the local work.
func (s Service) Export(ctx context.Context, opts ExportOptions) (File, error) {
	if opts.IssueID != "" && opts.Learnings {
		return File{}, errors.New("learnings are exported with all local work, not with one issue")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return File{}, err
	}
	defer tx.Rollback()
	f := File{Format: Format, ExportedAt: s.now(), Issues: []Issue{}}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM local_issues ORDER BY created_at, id`)
	if err != nil {
		return File{}, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return File{}, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if opts.IssueID != "" {
		if !slices.Contains(ids, opts.IssueID) {
			return File{}, fmt.Errorf("%w: %s", localissues.ErrNotFound, opts.IssueID)
		}
		ids = []string{opts.IssueID}
	}
	recs, err := s.tasks().List(ctx)
	if err != nil {
		return File{}, err
	}
	for _, id := range ids {
		issue, err := exportIssue(tx, recs, id)
		if err != nil {
			return File{}, err
		}
		f.Issues = append(f.Issues, issue)
	}
	if opts.Learnings {
		repos, err := knowledge.Repositories(tx)
		if err != nil {
			return File{}, err
		}
		for _, repo := range repos {
			snap, err := exportLearnings(tx, repo)
			if err != nil {
				return File{}, err
			}
			if len(snap.Learnings) > 0 {
				snap.ExportedAt = f.ExportedAt
				f.Learnings = append(f.Learnings, snap)
			}
		}
	}
	return f, nil
}

type reader interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
	Exec(query string, args ...any) (sql.Result, error)
}

func exportIssue(q reader, recs []*task.Record, id string) (Issue, error) {
	li, err := localissues.GetIn(q, id)
	if err != nil {
		return Issue{}, err
	}
	out := Issue{ID: li.ID, Title: li.Title, Description: li.Description, SourceURL: li.SourceURL, Status: string(li.Status), Repo: li.Repo,
		Notes: li.Notes, Tags: li.Tags, CreatedAt: li.CreatedAt.UTC(), UpdatedAt: li.UpdatedAt.UTC()}
	var triage string
	switch err := q.QueryRow(`SELECT decisions FROM triage WHERE id = ?`, triageKey(id)).Scan(&triage); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return Issue{}, err
	default:
		out.Triage = strings.TrimSpace(triage)
	}
	for _, rec := range recs {
		if rec.Source == task.Local && rec.ExternalID == id {
			out.Work = workOf(rec)
			break
		}
	}
	return out, nil
}

// exportLearnings keeps what a repository's guidance rests on: active and retired
// learnings with their evidence and revision history. Candidates are pending
// proposals, and evaluations are raw judgments, so both stay out.
func exportLearnings(q reader, repo string) (knowledge.Snapshot, error) {
	next, ls, err := knowledge.ReadLearnings(q, repo)
	if err != nil {
		return knowledge.Snapshot{}, err
	}
	snap := knowledge.Snapshot{Format: knowledge.FormatVersion, Repo: repo, NextID: next, Learnings: []knowledge.Learning{}, Evaluations: []knowledge.Evaluation{}}
	for _, l := range ls {
		if l.Status != knowledge.Candidate {
			snap.Learnings = append(snap.Learnings, l)
		}
	}
	return snap, nil
}

func workOf(rec *task.Record) *Work {
	w := &Work{Repo: string(rec.Repo), State: "open", Notes: rec.Body}
	if rec.State == task.Done {
		w.State = "done"
	}
	var commits []string
	addCommit := func(c string) {
		if c != "" && !slices.Contains(commits, c) {
			commits = append(commits, c)
		}
	}
	var cp Checkpoint
	switch d, imp := rec.Delegation, rec.Imported; {
	case d != nil:
		if d.LocalPlan != nil {
			w.Requirements, w.Plan = d.LocalPlan.Documents[task.PRD], d.LocalPlan.Documents[task.ImplementationPlan]
			w.Fingerprint = d.LocalPlan.Fingerprint
		}
		cp = Checkpoint{Head: d.CheckpointHead, Next: d.CheckpointNext, Count: d.Checkpoints}
		w.Branch = d.Branch
		addCommit(d.CheckpointHead)
		addCommit(d.LocalHead)
		addCommit(d.PRHead)
		if d.PR > 0 {
			w.PR = &PR{Number: d.PR, URL: d.PRURL}
		}
	case imp != nil:
		w.Requirements, w.Plan = imp.Documents[task.PRD], imp.Documents[task.ImplementationPlan]
		w.Fingerprint = imp.Fingerprint
		cp = Checkpoint{Head: imp.CheckpointHead, Next: imp.CheckpointNext, Count: imp.Checkpoints}
		w.Branch = imp.Branch
		for _, c := range imp.Commits {
			addCommit(c)
		}
		if imp.PR > 0 {
			w.PR = &PR{Number: imp.PR, URL: imp.PRURL}
		}
	case rec.Reset != nil && rec.Reset.PR > 0:
		w.Branch = rec.Reset.Branch
		w.PR = &PR{Number: rec.Reset.PR, URL: rec.Reset.PRURL}
	}
	if cp != (Checkpoint{}) {
		w.Checkpoint = &cp
	}
	w.Commits = commits
	for _, d := range rec.Decisions {
		w.Decisions = append(w.Decisions, Decision{AskedAt: d.AskedAt.UTC(), Question: d.Question, Answer: d.Answer, AnsweredAt: d.AnsweredAt.UTC()})
	}
	for _, r := range rec.Reviews {
		if r.Skipped {
			continue
		}
		verdict, kind := "changes_requested", "adversarial"
		if r.Clean {
			verdict = "clean"
		}
		if r.Kind == task.SelfReview {
			kind = "self"
		}
		w.Reviews = append(w.Reviews, Review{Kind: kind, Round: r.Round, Reviewer: string(r.Reviewer), Verdict: verdict, Summary: r.Summary, Findings: r.Findings, Head: r.Head, At: r.At.UTC()})
	}
	return w
}

// Marshal writes the file as YAML.
func Marshal(f File) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}

var (
	shaPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
	tagLimit   = 64
)

// Parse reads and validates a file. It rejects another format, unknown fields,
// more than one document, and any record the stores could not have written.
func Parse(data []byte) (File, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f File
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return File{}, errors.New("not a foreman work export: the file is empty")
		}
		return File{}, fmt.Errorf("not a foreman work export: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return File{}, errors.New("not a foreman work export: more than one YAML document")
	}
	if f.Format != Format {
		return File{}, fmt.Errorf("unsupported format %q (want %s)", f.Format, Format)
	}
	seen := map[string]bool{}
	for i := range f.Issues {
		if err := f.Issues[i].validate(); err != nil {
			return File{}, fmt.Errorf("issue %d (%s): %w", i+1, f.Issues[i].ID, err)
		}
		if seen[f.Issues[i].ID] {
			return File{}, fmt.Errorf("issue %d: duplicate id %s", i+1, f.Issues[i].ID)
		}
		seen[f.Issues[i].ID] = true
		f.Issues[i].normalize()
	}
	repos := map[string]bool{}
	for i, snap := range f.Learnings {
		if err := snap.Validate(); err != nil {
			return File{}, fmt.Errorf("learnings %d: %w", i+1, err)
		}
		if len(snap.Evaluations) > 0 {
			return File{}, fmt.Errorf("learnings %d (%s): evaluations are not portable", i+1, snap.Repo)
		}
		for _, l := range snap.Learnings {
			if l.Status == knowledge.Candidate {
				return File{}, fmt.Errorf("learnings %d (%s): pending proposal %s is not portable", i+1, snap.Repo, l.ID)
			}
		}
		if repos[snap.Repo] {
			return File{}, fmt.Errorf("learnings %d: duplicate repository %s", i+1, snap.Repo)
		}
		repos[snap.Repo] = true
	}
	return f, nil
}

func (i *Issue) normalize() {
	i.CreatedAt, i.UpdatedAt = i.CreatedAt.UTC(), i.UpdatedAt.UTC()
	i.Triage = strings.TrimSpace(i.Triage)
	if w := i.Work; w != nil {
		for k := range w.Decisions {
			w.Decisions[k].AskedAt, w.Decisions[k].AnsweredAt = w.Decisions[k].AskedAt.UTC(), w.Decisions[k].AnsweredAt.UTC()
		}
		for k := range w.Reviews {
			w.Reviews[k].At = w.Reviews[k].At.UTC()
		}
	}
}

func (i Issue) validate() error {
	if !localissues.IDPattern.MatchString(i.ID) {
		return fmt.Errorf("id must look like L-0A1B2C3D")
	}
	in := localissues.Input{Title: i.Title, SourceURL: i.SourceURL, Repo: i.Repo}
	if err := in.Validate(); err != nil {
		return err
	}
	if i.Status != string(localissues.Open) && i.Status != string(localissues.Done) {
		return fmt.Errorf("status %q is not open or done", i.Status)
	}
	if i.CreatedAt.IsZero() || i.UpdatedAt.IsZero() {
		return errors.New("created_at and updated_at are required")
	}
	if len(i.Tags) > tagLimit {
		return fmt.Errorf("more than %d tags", tagLimit)
	}
	if w := i.Work; w != nil {
		if err := w.validate(); err != nil {
			return fmt.Errorf("work: %w", err)
		}
	}
	return nil
}

func (w Work) validate() error {
	if _, err := task.ParseRepo(w.Repo); err != nil {
		return err
	}
	if w.State != "open" && w.State != "done" {
		return fmt.Errorf("state %q is not open or done", w.State)
	}
	if w.Branch != "" && !task.ValidBranchName(w.Branch) {
		return fmt.Errorf("invalid branch %q", w.Branch)
	}
	for _, c := range w.Commits {
		if !shaPattern.MatchString(c) {
			return fmt.Errorf("invalid commit %q", c)
		}
	}
	if cp := w.Checkpoint; cp != nil {
		if cp.Head != "" && !shaPattern.MatchString(cp.Head) || cp.Count < 0 {
			return errors.New("invalid checkpoint")
		}
	}
	if pr := w.PR; pr != nil && (pr.Number <= 0 || pr.URL != "" && !strings.HasPrefix(pr.URL, "https://")) {
		return errors.New("invalid pull request reference")
	}
	for i, d := range w.Decisions {
		if err := d.Question.Validate(); err != nil {
			return fmt.Errorf("decision %d: %w", i+1, err)
		}
	}
	for i, r := range w.Reviews {
		if r.Kind != "self" && r.Kind != "adversarial" || r.Verdict != "clean" && r.Verdict != "changes_requested" {
			return fmt.Errorf("review %d has an unknown kind or verdict", i+1)
		}
		if r.Reviewer != "" {
			if _, err := task.ParseAgent(r.Reviewer); err != nil {
				return fmt.Errorf("review %d: %w", i+1, err)
			}
		}
		if r.Head != "" && !shaPattern.MatchString(r.Head) {
			return fmt.Errorf("review %d: invalid head %q", i+1, r.Head)
		}
	}
	return nil
}

// recordFor is the task record an import restores. It has no delegation, session,
// worktree, or runner, so nothing runs. An unfinished run's PR becomes a reset mark,
// which the next start reads to continue that PR when it is still reachable.
func (s Service) recordFor(i Issue) *task.Record {
	w := i.Work
	rec := &task.Record{
		Key:        localissues.Key(i.ID, task.Repo(w.Repo)),
		Identifier: i.ID,
		Kind:       task.Issue,
		Title:      i.Title,
		URL:        i.SourceURL,
		State:      task.Discovered,
		Body:       w.Notes,
	}
	if w.State == "done" {
		rec.State = task.Done
	}
	for _, d := range w.Decisions {
		rec.Decisions = append(rec.Decisions, task.Decision{AskedAt: d.AskedAt, Question: d.Question, Answer: d.Answer, AnsweredAt: d.AnsweredAt})
	}
	for _, r := range w.Reviews {
		agent, _ := task.ParseAgent(r.Reviewer)
		kind := task.CompetingReview
		if r.Kind == "self" {
			kind = task.SelfReview
		}
		rec.Reviews = append(rec.Reviews, task.Review{Kind: kind, Round: r.Round, Reviewer: agent, Clean: r.Verdict == "clean", Summary: r.Summary, Findings: r.Findings, Head: r.Head, At: r.At})
	}
	imp := &task.ImportedWork{Fingerprint: w.Fingerprint, Branch: w.Branch, Commits: w.Commits, At: s.now()}
	if w.Requirements != "" || w.Plan != "" {
		imp.Documents = map[task.PlanDoc]string{}
		if w.Requirements != "" {
			imp.Documents[task.PRD] = w.Requirements
		}
		if w.Plan != "" {
			imp.Documents[task.ImplementationPlan] = w.Plan
		}
	}
	if cp := w.Checkpoint; cp != nil {
		imp.CheckpointHead, imp.CheckpointNext, imp.Checkpoints = cp.Head, cp.Next, cp.Count
	}
	if pr := w.PR; pr != nil {
		imp.PR, imp.PRURL = pr.Number, pr.URL
		if rec.State != task.Done {
			rec.Reset = &task.ResetMark{At: s.now(), PR: pr.Number, PRURL: pr.URL, Branch: w.Branch}
		}
	}
	rec.Imported = imp
	return rec
}

// ConflictPolicy says what an import does with an issue that already exists with
// different content.
type ConflictPolicy string

const (
	// Fail changes nothing when any record conflicts.
	Fail ConflictPolicy = "fail"
	// Skip keeps this database's version of each conflicting record.
	Skip ConflictPolicy = "skip"
	// Replace overwrites each conflicting record with the file's.
	Replace ConflictPolicy = "replace"
)

// Conflict is a record the file and the database both hold with different content.
type Conflict struct {
	Kind   string // issue or learnings
	ID     string // issue id, or repository
	Detail string
}

// Plan is what an import would do.
type Plan struct {
	Create    []string
	Unchanged []string
	Conflicts []Conflict
	Applied   bool
}

// ErrConflicts means an import found conflicts and the policy was to fail.
var ErrConflicts = errors.New("the file conflicts with existing records; nothing was imported")

// Preview reports what Apply would do without changing anything.
func (s Service) Preview(ctx context.Context, f File) (Plan, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Plan{}, err
	}
	defer tx.Rollback()
	p, _, err := s.plan(ctx, tx, f)
	return p, err
}

type change struct {
	issue    *Issue
	snapshot *knowledge.Snapshot
	replace  bool
}

func (s Service) plan(ctx context.Context, q reader, f File) (Plan, []change, error) {
	var p Plan
	var changes []change
	recs, err := s.tasks().List(ctx)
	if err != nil {
		return p, nil, err
	}
	for k := range f.Issues {
		in := &f.Issues[k]
		have, err := exportIssue(q, recs, in.ID)
		switch {
		case errors.Is(err, localissues.ErrNotFound):
			p.Create = append(p.Create, in.ID)
			changes = append(changes, change{issue: in})
			continue
		case err != nil:
			return p, nil, err
		}
		if same(have, *in) {
			p.Unchanged = append(p.Unchanged, in.ID)
			continue
		}
		p.Conflicts = append(p.Conflicts, Conflict{Kind: "issue", ID: in.ID, Detail: describe(have, *in)})
		changes = append(changes, change{issue: in, replace: true})
	}
	for k := range f.Learnings {
		snap := &f.Learnings[k]
		have, err := exportLearnings(q, snap.Repo)
		if err != nil {
			return p, nil, err
		}
		switch {
		case len(have.Learnings) == 0 && have.NextID == 0:
			p.Create = append(p.Create, "learnings:"+snap.Repo)
			changes = append(changes, change{snapshot: snap})
		case knowledge.SameSnapshot(have, withTime(*snap, have)):
			p.Unchanged = append(p.Unchanged, "learnings:"+snap.Repo)
		default:
			p.Conflicts = append(p.Conflicts, Conflict{Kind: "learnings", ID: snap.Repo, Detail: "this database already holds different learnings for the repository"})
			changes = append(changes, change{snapshot: snap, replace: true})
		}
	}
	return p, changes, nil
}

func withTime(a, b knowledge.Snapshot) knowledge.Snapshot { a.ExportedAt = b.ExportedAt; return a }

func same(a, b Issue) bool {
	enc := func(i Issue) string {
		i.normalize()
		raw, _ := yaml.Marshal(i)
		return string(raw)
	}
	return enc(a) == enc(b)
}

func describe(have, in Issue) string {
	var parts []string
	check := func(name string, a, b any) {
		if fmt.Sprint(a) != fmt.Sprint(b) {
			parts = append(parts, name)
		}
	}
	check("title", have.Title, in.Title)
	check("description", have.Description, in.Description)
	check("source_url", have.SourceURL, in.SourceURL)
	check("status", have.Status, in.Status)
	check("repo", have.Repo, in.Repo)
	check("notes", have.Notes, in.Notes)
	check("tags", have.Tags, in.Tags)
	check("triage", have.Triage, in.Triage)
	if !same(Issue{Work: have.Work}, Issue{Work: in.Work}) {
		parts = append(parts, "work")
	}
	if len(parts) == 0 {
		parts = append(parts, "timestamps")
	}
	return "differs in " + strings.Join(parts, ", ")
}

// Apply imports the file in one transaction: either every create and replace the
// policy allows is saved, or none is. Importing the same file again changes
// nothing. It starts no agent and touches no tracker.
func (s Service) Apply(ctx context.Context, f File, policy ConflictPolicy) (Plan, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Plan{}, err
	}
	defer tx.Rollback()
	p, changes, err := s.plan(ctx, tx, f)
	if err != nil {
		return p, err
	}
	if len(p.Conflicts) > 0 && policy != Skip && policy != Replace {
		return p, ErrConflicts
	}
	for _, c := range changes {
		if c.replace && policy != Replace {
			continue
		}
		switch {
		case c.issue != nil:
			if err := s.writeIssue(ctx, tx, *c.issue, c.replace); err != nil {
				return p, fmt.Errorf("%s: %w", c.issue.ID, err)
			}
		case c.snapshot != nil:
			if err := knowledge.WriteLearnings(tx, c.snapshot.Repo, c.snapshot.NextID, c.snapshot.Learnings); err != nil {
				return p, fmt.Errorf("learnings of %s: %w", c.snapshot.Repo, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return p, err
	}
	p.Applied = true
	if s.Guidance != nil {
		for _, c := range changes {
			if c.snapshot != nil && (!c.replace || policy == Replace) {
				s.Guidance(c.snapshot.Repo)
			}
		}
	}
	return p, nil
}

func (s Service) writeIssue(ctx context.Context, tx *sql.Tx, in Issue, replace bool) error {
	if replace {
		// Replacing work that has a live agent would orphan its runner.
		recs, err := s.tasks().List(ctx)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			if rec.Source == task.Local && rec.ExternalID == in.ID && rec.Delegation != nil && rec.Delegation.RunnerPID > 0 && pidAlive(rec.Delegation.RunnerPID) {
				return fmt.Errorf("its agent is still running (pid %d); stop it before replacing the work", rec.Delegation.RunnerPID)
			}
		}
	}
	if err := localissues.PutIn(tx, localissues.Issue{ID: in.ID, Title: in.Title, Description: in.Description, SourceURL: in.SourceURL,
		Status: localissues.Status(in.Status), Repo: in.Repo, Notes: in.Notes, Tags: in.Tags, CreatedAt: in.CreatedAt, UpdatedAt: in.UpdatedAt}); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM triage WHERE id = ?`, triageKey(in.ID)); err != nil {
		return err
	}
	if in.Triage != "" {
		if _, err := tx.Exec(`INSERT INTO triage (id, decisions, confirmed_at) VALUES (?, ?, ?)`, triageKey(in.ID), in.Triage, in.UpdatedAt.Format(time.RFC3339)); err != nil {
			return err
		}
	}
	if replace {
		// The file's version of the issue is the whole truth: drop whatever work the
		// database held for it, in any repository, so its old branch, PR, and
		// checkpoints cannot resurface in an export or a resume.
		if _, err := tx.Exec(`DELETE FROM tasks WHERE source = ? AND external_id = ?`, string(task.Local), in.ID); err != nil {
			return err
		}
	}
	if in.Work != nil {
		if err := s.tasks().SaveIn(tx, s.recordFor(in)); err != nil {
			return err
		}
	}
	return nil
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
