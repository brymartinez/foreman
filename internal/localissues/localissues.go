// Package localissues is Foreman's own issue store: work taken from any tracker
// (Linear, Jira, a chat message) and managed entirely in Foreman. An issue keeps a
// stable ID, its source URL as context, and private notes and tags. Foreman never
// reads from or writes to the tracker the URL points at.
package localissues

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

// Status is where an issue stands. Confirmed triage is stored separately, so an
// issue is "awaiting triage" exactly while the triage store has no record for it.
type Status string

const (
	Open Status = "open"
	Done Status = "done"
)

// IDPattern is a local issue's stable ID: L- and eight uppercase hex digits.
var IDPattern = regexp.MustCompile(`^L-[0-9A-F]{8}$`)

// Issue is one local issue.
type Issue struct {
	ID          string
	Title       string
	Description string
	SourceURL   string
	Status      Status
	// Repo is an optional hint, "owner/name", for the repository the work belongs
	// in. Delegation still confirms the repository.
	Repo      string
	Notes     string
	Tags      []string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Input is what intake saves: the confirmed scope of a new issue.
type Input struct {
	Title       string
	Description string
	SourceURL   string
	Repo        string
	Notes       string
	Tags        []string
}

// Store is the local issue table plus the triage store that holds each issue's
// confirmed decisions.
type Store struct {
	DB  *sql.DB
	Now func() time.Time
}

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC().Truncate(time.Second)
	}
	return time.Now().UTC().Truncate(time.Second)
}

// Validate checks an input before it is saved.
func (in Input) Validate() error {
	if strings.TrimSpace(in.Title) == "" {
		return errors.New("a local issue needs a title")
	}
	if in.SourceURL != "" {
		u, err := url.Parse(in.SourceURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return fmt.Errorf("source URL %q must be an http or https link without credentials", in.SourceURL)
		}
	}
	if in.Repo != "" {
		if _, err := task.ParseRepo(in.Repo); err != nil {
			return err
		}
	}
	return nil
}

// NewID makes an unused stable ID.
func NewID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "L-" + strings.ToUpper(hex.EncodeToString(b)), nil
}

func cleanTags(tags []string) []string {
	var out []string
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t != "" && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// Create saves a new open issue and returns it with its stable ID.
func (s Store) Create(ctx context.Context, in Input) (Issue, error) {
	if err := in.Validate(); err != nil {
		return Issue{}, err
	}
	now := s.now()
	for attempt := 0; attempt < 8; attempt++ {
		id, err := NewID()
		if err != nil {
			return Issue{}, err
		}
		issue := Issue{ID: id, Title: strings.TrimSpace(in.Title), Description: strings.TrimSpace(in.Description), SourceURL: in.SourceURL,
			Status: Open, Repo: in.Repo, Notes: in.Notes, Tags: cleanTags(in.Tags), CreatedAt: now, UpdatedAt: now}
		err = s.insert(ctx, issue)
		if err == nil {
			return issue, nil
		}
		if !strings.Contains(err.Error(), "UNIQUE") {
			return Issue{}, err
		}
	}
	return Issue{}, errors.New("could not allocate a local issue ID")
}

func (s Store) insert(ctx context.Context, i Issue) error {
	tags, _ := json.Marshal(nonNil(i.Tags))
	_, err := s.DB.ExecContext(ctx, `INSERT INTO local_issues (id, title, description, source_url, status, repo, notes, tags, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, i.ID, i.Title, i.Description, i.SourceURL, string(i.Status), i.Repo, i.Notes, string(tags),
		i.CreatedAt.Format(time.RFC3339), i.UpdatedAt.Format(time.RFC3339))
	return err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

const columns = `id, title, description, source_url, status, repo, notes, tags, created_at, updated_at`

type scanner interface{ Scan(...any) error }

func scan(r scanner) (Issue, error) {
	var i Issue
	var status, tags, created, updated string
	if err := r.Scan(&i.ID, &i.Title, &i.Description, &i.SourceURL, &status, &i.Repo, &i.Notes, &tags, &created, &updated); err != nil {
		return Issue{}, err
	}
	i.Status = Status(status)
	if err := json.Unmarshal([]byte(tags), &i.Tags); err != nil {
		return Issue{}, fmt.Errorf("local issue %s: tags: %w", i.ID, err)
	}
	var err error
	if i.CreatedAt, err = time.Parse(time.RFC3339, created); err != nil {
		return Issue{}, fmt.Errorf("local issue %s: %w", i.ID, err)
	}
	if i.UpdatedAt, err = time.Parse(time.RFC3339, updated); err != nil {
		return Issue{}, fmt.Errorf("local issue %s: %w", i.ID, err)
	}
	return i, nil
}

// ErrNotFound means no local issue has the ID.
var ErrNotFound = errors.New("local issue not found")

// Get reads one issue.
func (s Store) Get(ctx context.Context, id string) (Issue, error) {
	i, err := scan(s.DB.QueryRowContext(ctx, `SELECT `+columns+` FROM local_issues WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return i, err
}

// List reads every issue, oldest first.
func (s Store) List(ctx context.Context) ([]Issue, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+columns+` FROM local_issues ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Issue
	for rows.Next() {
		i, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// SetStatus opens or completes an issue.
func (s Store) SetStatus(ctx context.Context, id string, status Status) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE local_issues SET status = ?, updated_at = ? WHERE id = ?`, string(status), s.now().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return nil
}

// Key is the task key an issue has once a repository is chosen for it.
func Key(id string, repo task.Repo) task.Key {
	return task.Key{Repo: repo, Source: task.Local, ExternalID: id}
}

// OpenIssues implements app.LocalIssues. Issues carry no repository in their key
// until delegation binds one, like any work waiting for a repository choice.
func (s Store) OpenIssues(ctx context.Context) ([]app.InboxItem, error) {
	issues, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []app.InboxItem
	for _, i := range issues {
		if i.Status == Open {
			out = append(out, item(i))
		}
	}
	return out, nil
}

func item(i Issue) app.InboxItem {
	return app.InboxItem{Key: Key(i.ID, ""), Identifier: i.ID, Kind: task.Issue, Title: i.Title, URL: i.SourceURL, State: state(i),
		CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt, Reasons: []task.InboxReason{task.Assigned}, Labels: labels(i)}
}

// labels are the tags shown with the issue. The "triaged" label belongs to
// GitHub's flow, so a tag of that name cannot stand in for confirmed triage.
func labels(i Issue) []string {
	var out []string
	for _, t := range i.Tags {
		if !strings.EqualFold(t, "triaged") {
			out = append(out, t)
		}
	}
	return out
}

func state(i Issue) string {
	if i.Status == Done {
		return "closed"
	}
	return "open"
}

// GetIssue implements app.LocalIssues. The repository hint becomes a GitHub link in
// the body, which repository selection already reads as evidence.
func (s Store) GetIssue(ctx context.Context, key task.Key) (app.Issue, error) {
	i, err := s.Get(ctx, key.ExternalID)
	if errors.Is(err, ErrNotFound) {
		return app.Issue{}, app.ErrGone
	}
	if err != nil {
		return app.Issue{}, err
	}
	body := i.Description
	if i.Repo != "" {
		body += "\n\nRepository: https://github.com/" + i.Repo
	}
	return app.Issue{Key: key, Identifier: i.ID, Title: i.Title, Body: strings.TrimSpace(body), Labels: labels(i), URL: i.SourceURL, State: state(i), UpdatedAt: i.UpdatedAt}, nil
}

// Complete implements app.LocalIssues.
func (s Store) Complete(ctx context.Context, key task.Key) error {
	return s.SetStatus(ctx, key.ExternalID, Done)
}

// Queryer is what a transaction or the database offers a read or write.
type Queryer interface {
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
}

// GetIn reads an issue through q, or returns ErrNotFound.
func GetIn(q Queryer, id string) (Issue, error) {
	i, err := scan(q.QueryRow(`SELECT `+columns+` FROM local_issues WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return i, err
}

// PutIn inserts or replaces an issue through q, keeping its ID and times.
func PutIn(q Queryer, i Issue) error {
	tags, _ := json.Marshal(nonNil(i.Tags))
	_, err := q.Exec(`INSERT INTO local_issues (id, title, description, source_url, status, repo, notes, tags, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET title = excluded.title, description = excluded.description, source_url = excluded.source_url, status = excluded.status,
repo = excluded.repo, notes = excluded.notes, tags = excluded.tags, created_at = excluded.created_at, updated_at = excluded.updated_at`,
		i.ID, i.Title, i.Description, i.SourceURL, string(i.Status), i.Repo, i.Notes, string(tags),
		i.CreatedAt.UTC().Format(time.RFC3339), i.UpdatedAt.UTC().Format(time.RFC3339))
	return err
}

// CreateIn is Create inside the caller's transaction.
func (s Store) CreateIn(ctx context.Context, q Queryer, in Input) (Issue, error) {
	if err := in.Validate(); err != nil {
		return Issue{}, err
	}
	now := s.now()
	for attempt := 0; attempt < 8; attempt++ {
		id, err := NewID()
		if err != nil {
			return Issue{}, err
		}
		if _, err := GetIn(q, id); err == nil {
			continue
		} else if !errors.Is(err, ErrNotFound) {
			return Issue{}, err
		}
		issue := Issue{ID: id, Title: strings.TrimSpace(in.Title), Description: strings.TrimSpace(in.Description), SourceURL: in.SourceURL,
			Status: Open, Repo: in.Repo, Notes: in.Notes, Tags: cleanTags(in.Tags), CreatedAt: now, UpdatedAt: now}
		return issue, PutIn(q, issue)
	}
	return Issue{}, errors.New("could not allocate a local issue ID")
}
