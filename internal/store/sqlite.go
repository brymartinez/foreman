package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

// SQLite keeps one row per task in the shared database. The row's document is the
// same Markdown (frontmatter and notes body) the Markdown store writes, so user
// frontmatter keys, comments, and notes survive saves, and a Markdown export for
// Obsidian is a straight copy. Foreman-owned fields stay in the frontmatter until
// a field needs its own column; the columns here are only what queries filter on.
type SQLite struct {
	DB *sql.DB
	// Now stamps rows; time.Now when nil.
	Now func() time.Time
}

func (s SQLite) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// taskID is the row key: the repository plus the source-qualified issue identity.
func taskID(key task.Key) string {
	switch key.Source {
	case task.Local:
		return fmt.Sprintf("%s#local:%s", key.Repo, key.ExternalID)
	default:
		return fmt.Sprintf("%s#%d", key.Repo, key.Number)
	}
}

type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
}

func (s SQLite) read(q queryer, key task.Key) (*task.Record, []byte, error) {
	var doc string
	err := q.QueryRow(`SELECT document FROM tasks WHERE id = ?`, taskID(key)).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	rec, err := decode([]byte(doc))
	if err != nil {
		return nil, nil, fmt.Errorf("task %s: %w", taskID(key), err)
	}
	if rec == nil || rec.Key != key {
		return nil, nil, fmt.Errorf("task %s: row holds a different task", taskID(key))
	}
	return rec, []byte(doc), nil
}

func (s SQLite) write(q queryer, rec *task.Record, existing []byte) error {
	if rec.Source == task.Local && (!localIssueID.MatchString(rec.ExternalID) || rec.Identifier == "") {
		return errors.New("local issue needs a stable ID and identifier")
	}
	out, err := encode(rec, existing)
	if err != nil {
		return fmt.Errorf("task %s: %w", taskID(rec.Key), err)
	}
	source := string(rec.Source)
	if source == "" {
		source = string(task.GitHub)
	}
	_, err = q.Exec(`INSERT INTO tasks (id, source, repo, number, external_id, state, title, document, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET state = excluded.state, title = excluded.title, document = excluded.document, updated_at = excluded.updated_at`,
		taskID(rec.Key), source, string(rec.Repo), rec.Number, rec.ExternalID, string(rec.State), rec.Title, string(out), s.now().UTC().Format(time.RFC3339))
	return err
}

func (s SQLite) Get(ctx context.Context, key task.Key) (*task.Record, error) {
	rec, _, err := s.read(s.DB, key)
	return rec, err
}

func (s SQLite) Save(ctx context.Context, rec *task.Record) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, existing, err := s.read(tx, rec.Key)
	if err != nil {
		return err
	}
	if err := s.write(tx, rec, existing); err != nil {
		return err
	}
	return tx.Commit()
}

// Update runs fn in one write transaction, so the dashboard and the runners, in
// separate processes, never overwrite each other. A nil result from fn saves nothing.
func (s SQLite) Update(ctx context.Context, key task.Key, fn func(cur *task.Record) (*task.Record, error)) (*task.Record, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	cur, existing, err := s.read(tx, key)
	if err != nil {
		return nil, err
	}
	next, err := fn(cur)
	if err != nil || next == nil {
		return next, err
	}
	if err := s.write(tx, next, existing); err != nil {
		return nil, err
	}
	return next, tx.Commit()
}

// List returns every task, sorted by repository, source, then issue.
func (s SQLite) List(ctx context.Context) ([]*task.Record, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, document FROM tasks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var recs []*task.Record
	for rows.Next() {
		var id, doc string
		if err := rows.Scan(&id, &doc); err != nil {
			return nil, err
		}
		rec, err := decode([]byte(doc))
		if err != nil {
			return nil, fmt.Errorf("task %s: %w", id, err)
		}
		if rec != nil {
			recs = append(recs, rec)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].Repo != recs[j].Repo {
			return recs[i].Repo < recs[j].Repo
		}
		if recs[i].Source != recs[j].Source {
			return recs[i].Source < recs[j].Source
		}
		if recs[i].Source == task.Local {
			return recs[i].ExternalID < recs[j].ExternalID
		}
		return recs[i].Number < recs[j].Number
	})
	return recs, nil
}

// Document is the task's stored Markdown, for export.
func (s SQLite) Document(key task.Key) (string, error) {
	_, doc, err := s.read(s.DB, key)
	return string(doc), err
}

// SQLiteTriage keeps confirmed triage decisions, one row per issue.
type SQLiteTriage struct {
	DB  *sql.DB
	Now func() time.Time
}

// triageID is the row key, the same identity the Markdown records used as a path.
func triageID(key task.Key) (string, error) {
	switch key.Source {
	case task.Local:
		if !localIssueID.MatchString(key.ExternalID) {
			return "", errors.New("invalid local issue ID")
		}
		return "local/" + strings.ToLower(key.ExternalID), nil
	case "", task.GitHub:
		repo, err := task.ParseRepo(string(key.Repo))
		if err != nil || key.Number <= 0 {
			return "", errors.New("invalid GitHub issue identity")
		}
		for _, part := range strings.Split(string(repo), "/") {
			if part == "." || part == ".." {
				return "", errors.New("invalid GitHub issue repository")
			}
		}
		return "github/" + strings.ToLower(string(repo)) + "/" + strconv.Itoa(key.Number), nil
	}
	return "", fmt.Errorf("invalid issue source %q", key.Source)
}

// Read returns empty text for an issue that has yet to be triaged.
func (s SQLiteTriage) Read(key task.Key) (string, error) {
	id, err := triageID(key)
	if err != nil {
		return "", err
	}
	var text string
	err = s.DB.QueryRow(`SELECT decisions FROM triage WHERE id = ?`, id).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("invalid confirmed triage record %s", id)
	}
	return strings.TrimSpace(text), nil
}

// Save stores the first confirmed record. A second interview must read the record
// that won before continuing.
func (s SQLiteTriage) Save(key task.Key, decisions string) error {
	id, err := triageID(key)
	if err != nil {
		return err
	}
	decisions = strings.TrimSpace(decisions)
	if decisions == "" {
		return errors.New("confirmed triage is empty")
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	res, err := s.DB.Exec(`INSERT INTO triage (id, decisions, confirmed_at) VALUES (?, ?, ?) ON CONFLICT(id) DO NOTHING`, id, decisions, now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("confirmed triage for %s already exists", id)
	}
	return nil
}

// LockStart serializes starts of one issue across processes and across the
// repositories a local issue could start in. The lock names the holder's pid, so
// one left by a process that died is taken over.
func (s SQLiteTriage) LockStart(key task.Key) (func(), error) {
	id, err := triageID(key)
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var pid int
	err = tx.QueryRow(`SELECT pid FROM start_locks WHERE id = ?`, id).Scan(&pid)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, err
	case pidAlive(pid):
		return nil, errors.New("issue is already starting")
	}
	if _, err := tx.Exec(`INSERT INTO start_locks (id, pid, since) VALUES (?, ?, ?)
ON CONFLICT(id) DO UPDATE SET pid = excluded.pid, since = excluded.since`, id, pidSelf(), time.Now().UTC().Format(time.RFC3339)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return func() {
		_, _ = s.DB.Exec(`DELETE FROM start_locks WHERE id = ? AND pid = ?`, id, pidSelf())
	}, nil
}

// pidAlive is true while the process exists. A process held by someone else still
// counts as alive.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Queryer is what a transaction or the database offers a read or write.
type Queryer interface {
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
}

// GetIn reads a task through q, for callers that run their own transaction.
func (s SQLite) GetIn(q Queryer, key task.Key) (*task.Record, error) {
	rec, _, err := s.read(q, key)
	return rec, err
}

// SaveIn writes a task through q, keeping the user's frontmatter and notes in any
// stored copy.
func (s SQLite) SaveIn(q Queryer, rec *task.Record) error {
	_, existing, err := s.read(q, rec.Key)
	if err != nil {
		return err
	}
	return s.write(q, rec, existing)
}

// SaveIn is Save inside the caller's transaction.
func (s SQLiteTriage) SaveIn(q Queryer, key task.Key, decisions string) error {
	id, err := triageID(key)
	if err != nil {
		return err
	}
	decisions = strings.TrimSpace(decisions)
	if decisions == "" {
		return errors.New("confirmed triage is empty")
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	res, err := q.Exec(`INSERT INTO triage (id, decisions, confirmed_at) VALUES (?, ?, ?) ON CONFLICT(id) DO NOTHING`, id, decisions, now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("confirmed triage for %s already exists", id)
	}
	return nil
}
