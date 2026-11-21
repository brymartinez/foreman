// Package db opens Foreman's SQLite database and applies its versioned schema.
// Every store that persists state (tasks, triage, local issues, learnings) shares
// this one file.
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go driver, so releases stay static and cgo-free
)

// migration is one schema step. Steps run in order, each in its own transaction,
// and never change once released: later versions add a new step.
type migration struct {
	version int
	name    string
	sql     string
}

var migrations = []migration{
	{1, "initial schema", `
CREATE TABLE tasks (
	id          TEXT PRIMARY KEY,
	source      TEXT NOT NULL,
	repo        TEXT NOT NULL,
	number      INTEGER NOT NULL,
	external_id TEXT NOT NULL,
	state       TEXT NOT NULL,
	title       TEXT NOT NULL,
	document    TEXT NOT NULL,
	updated_at  TEXT NOT NULL
);
CREATE INDEX tasks_by_repo ON tasks(repo);

CREATE TABLE triage (
	id           TEXT PRIMARY KEY,
	decisions    TEXT NOT NULL,
	confirmed_at TEXT NOT NULL
);

CREATE TABLE start_locks (
	id    TEXT PRIMARY KEY,
	pid   INTEGER NOT NULL,
	since TEXT NOT NULL
);

CREATE TABLE local_issues (
	id          TEXT PRIMARY KEY,
	title       TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	source_url  TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL,
	repo        TEXT NOT NULL DEFAULT '',
	notes       TEXT NOT NULL DEFAULT '',
	tags        TEXT NOT NULL DEFAULT '[]',
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL
);

CREATE TABLE learnings (
	repo TEXT PRIMARY KEY,
	data TEXT NOT NULL
);

CREATE TABLE evaluations (
	repo    TEXT NOT NULL,
	task    TEXT NOT NULL,
	task_at TEXT NOT NULL,
	data    TEXT NOT NULL,
	PRIMARY KEY (repo, task)
);

CREATE TABLE attempts (
	repo TEXT NOT NULL,
	task TEXT NOT NULL,
	data TEXT NOT NULL,
	PRIMARY KEY (repo, task)
);

CREATE TABLE locks (
	name  TEXT PRIMARY KEY,
	pid   INTEGER NOT NULL,
	token TEXT NOT NULL,
	since TEXT NOT NULL
);

CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`},
}

// SchemaVersion is the newest schema this build knows.
func SchemaVersion() int { return migrations[len(migrations)-1].version }

// ErrNewerSchema means the database was written by a newer Foreman.
var ErrNewerSchema = errors.New("the database was written by a newer foreman; upgrade foreman before opening it")

// Open opens (creating if needed) the database at path and brings its schema up to
// date. Several foreman processes (the dashboard and each runner) share the file:
// WAL mode lets readers proceed beside a writer, a busy timeout makes writers wait
// for each other, and every transaction takes the write lock up front, so a
// read-modify-write never loses to another process's.
func Open(path string) (*sql.DB, error) {
	// An empty path would make SQLite open a private temporary database, and every
	// process would silently lose its work.
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("the database path is empty")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("_txlock", "immediate")
	for _, p := range []string{"busy_timeout(15000)", "journal_mode(WAL)", "foreign_keys(1)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	// Build the URI with url.URL so characters such as #, ?, and % in the path stay
	// part of the file name instead of starting a fragment or query.
	uri := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	d, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	if err := Migrate(d); err != nil {
		d.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}

// Migrate applies every migration the database has not seen.
func Migrate(d *sql.DB) error {
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	var current sql.NullInt64
	if err := d.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	if int(current.Int64) > SchemaVersion() {
		return ErrNewerSchema
	}
	for _, m := range migrations {
		if m.version <= int(current.Int64) {
			continue
		}
		if err := apply(d, m); err != nil {
			return fmt.Errorf("migration %d (%s): %w", m.version, m.name, err)
		}
	}
	return nil
}

func apply(d *sql.DB, m migration) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Another process may have applied the step while this one waited for the lock.
	var done int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.version).Scan(&done); err != nil {
		return err
	}
	if done > 0 {
		return nil
	}
	if _, err := tx.Exec(m.sql); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`, m.version, m.name, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}

// Meta reads a value from the meta table; missing reads as "".
func Meta(q interface {
	QueryRow(string, ...any) *sql.Row
}, key string) (string, error) {
	var v string
	err := q.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}
