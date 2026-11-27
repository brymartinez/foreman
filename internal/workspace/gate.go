// Package workspace holds the workspace store's shared records, the sync between
// the host database and the store, and the move from SQLite.
package workspace

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"syscall"
	"time"

	"github.com/brymartinez/foreman/internal/config"
)

// MoveLock names the row in the locks table that `foreman storage move` holds. The
// gate refuses every other command while a live process holds it.
const MoveLock = "workspace-move"

// Marker is the cutover marker: the workspace now lives in Backend at Target.
type Marker struct {
	Backend      config.Backend
	Target       string
	MovedAt      string
	Tasks        int
	Issues       int
	Repositories int
}

// Target names where a backend keeps the workspace, for comparing against the marker.
func Target(s config.Storage) string {
	switch s.Backend {
	case config.BackendNotion:
		return s.Notion.ParentPageID
	case config.BackendObsidian:
		return filepath.Join(filepath.Clean(s.Obsidian.Vault), s.Obsidian.Folder)
	}
	return ""
}

// Queryer is the read side of a database or transaction.
type Queryer interface {
	QueryRow(string, ...any) *sql.Row
}

// ReadMarker returns the cutover marker, or nil before the cutover.
func ReadMarker(q Queryer) (*Marker, error) {
	var m Marker
	err := q.QueryRow(`SELECT backend, target, moved_at, tasks, issues, repositories FROM workspace WHERE id = 1`).
		Scan(&m.Backend, &m.Target, &m.MovedAt, &m.Tasks, &m.Issues, &m.Repositories)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// Execer is the write side of a database or transaction.
type Execer interface {
	Exec(string, ...any) (sql.Result, error)
}

// WriteMarker records the cutover. The move calls it inside the transaction that
// ends verification.
func WriteMarker(x Execer, m Marker) error {
	if m.MovedAt == "" {
		m.MovedAt = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := x.Exec(`INSERT INTO workspace (id, backend, target, moved_at, tasks, issues, repositories) VALUES (1, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET backend = excluded.backend, target = excluded.target, moved_at = excluded.moved_at,
	tasks = excluded.tasks, issues = excluded.issues, repositories = excluded.repositories`,
		string(m.Backend), m.Target, m.MovedAt, m.Tasks, m.Issues, m.Repositories)
	return err
}

// HasWork reports whether the database holds tasks, local issues, triage, or
// learnings: what a move has to copy.
func HasWork(q Queryer) (bool, error) {
	var n int
	err := q.QueryRow(`SELECT (SELECT COUNT(*) FROM tasks) + (SELECT COUNT(*) FROM local_issues) +
		(SELECT COUNT(*) FROM triage) + (SELECT COUNT(*) FROM learnings) + (SELECT COUNT(*) FROM evaluations)`).Scan(&n)
	return n > 0, err
}

// MoveRunning reports whether a live process holds the move lock.
func MoveRunning(q Queryer) (bool, error) {
	var pid int
	err := q.QueryRow(`SELECT pid FROM locks WHERE name = ?`, MoveLock).Scan(&pid)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return pidAlive(pid), nil
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Gate decides whether this process may use the database under the configured
// storage. An unset backend and sqlite pass until a cutover marker exists. A
// database with no work gets its marker on the first successful setup.
func Gate(d *sql.DB, s config.Storage) error { return GateWith(d, s, nil) }

// GateWith is Gate with a check of the destination. An empty database is marked
// only after verify succeeds, so a mistyped or unshared Notion page does not bind
// the workspace to a destination nobody can reach. A nil verify skips the check.
func GateWith(d *sql.DB, s config.Storage, verify func() error) error {
	if running, err := MoveRunning(d); err != nil {
		return err
	} else if running {
		return errors.New("foreman storage move is running; wait for it to finish")
	}
	marker, err := ReadMarker(d)
	if err != nil {
		return err
	}
	if marker != nil {
		if s.Backend == marker.Backend && Target(s) == marker.Target {
			return nil
		}
		return fmt.Errorf("this workspace already moved to %s (%s) on %s; set storage.backend and its target back to match, because moving back to sqlite or to another store is not supported",
			marker.Backend, marker.Target, marker.MovedAt)
	}
	if s.Backend != config.BackendNotion && s.Backend != config.BackendObsidian {
		return nil
	}
	if err := s.Validate(); err != nil {
		return err
	}
	work, err := HasWork(d)
	if err != nil {
		return err
	}
	if work {
		return fmt.Errorf("storage.backend is %s but the local database still holds tasks, issues, triage, or learnings; run foreman storage move to copy them, or set storage.backend: sqlite", s.Backend)
	}
	if verify != nil {
		if err := verify(); err != nil {
			return fmt.Errorf("the %s store is not usable yet, so the workspace was not bound to it: %w; run foreman storage check, or set storage.backend: sqlite", s.Backend, err)
		}
	}
	return markEmpty(d, s)
}

// markEmpty writes the first-run marker in one transaction that rechecks what the
// destination check could not see: another process may have finished a move, or
// saved work, while the check ran.
func markEmpty(d *sql.DB, s config.Storage) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	marker, err := ReadMarker(tx)
	if err != nil {
		return err
	}
	if marker != nil {
		if marker.Backend == s.Backend && marker.Target == Target(s) {
			return nil
		}
		return fmt.Errorf("this workspace moved to %s (%s) while it was being set up; set storage.backend and its target to match", marker.Backend, marker.Target)
	}
	work, err := HasWork(tx)
	if err != nil {
		return err
	}
	if work {
		return fmt.Errorf("storage.backend is %s but the local database holds work now; run foreman storage move to copy it, or set storage.backend: sqlite", s.Backend)
	}
	if err := WriteMarker(tx, Marker{Backend: s.Backend, Target: Target(s)}); err != nil {
		return err
	}
	return tx.Commit()
}
