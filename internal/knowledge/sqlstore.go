package knowledge

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"go.yaml.in/yaml/v3"
)

// SQLStore keeps every repository's learnings, evaluations, and attempts in the
// shared database. learnings holds the same YAML document learnings.yaml held,
// one row per repository, so promotion and merge rules are unchanged; each
// evaluation and attempt is its own row. A save is one transaction, so a reader
// sees the whole old state or the whole new one, and a crash never leaves a
// half-applied ingest.
type SQLStore struct {
	DB *sql.DB
	// Dir, when set, is where each repository's knowledge.md is rendered, as
	// <Dir>/<owner>/<repo>/knowledge.md. The file is a read-only copy for tools that
	// read guidance without Foreman, such as the triage skill. The database is the
	// source of truth, and nothing reads the file back.
	Dir string
	// Log receives why a proposal was dropped. It never receives the proposal's text.
	Log io.Writer
}

func (s SQLStore) rules() Store { return Store{Log: s.Log} }

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
	Exec(query string, args ...any) (sql.Result, error)
}

func checkRepo(repo string) error {
	_, err := Store{}.repoDir(repo)
	return err
}

func (s SQLStore) load(q queryer, repo string) (learningsFile, error) {
	var data string
	err := q.QueryRow(`SELECT data FROM learnings WHERE repo = ?`, repo).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return learningsFile{}, nil
	}
	if err != nil {
		return learningsFile{}, err
	}
	var f learningsFile
	if err := yaml.Unmarshal([]byte(data), &f); err != nil {
		return learningsFile{}, fmt.Errorf("learnings of %s: %w", repo, err)
	}
	return f, nil
}

func saveLearnings(q execer, repo string, f learningsFile) error {
	raw, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	_, err = q.Exec(`INSERT INTO learnings (repo, data) VALUES (?, ?) ON CONFLICT(repo) DO UPDATE SET data = excluded.data`, repo, string(raw))
	return err
}

func saveEvaluation(q execer, repo string, ev Evaluation) error {
	raw, err := yaml.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = q.Exec(`INSERT INTO evaluations (repo, task, task_at, data) VALUES (?, ?, ?, ?)
ON CONFLICT(repo, task) DO UPDATE SET task_at = excluded.task_at, data = excluded.data`,
		repo, ev.Task, ev.TaskAt.UTC().Format(time.RFC3339Nano), string(raw))
	return err
}

func (s SQLStore) Learnings(repo string) ([]Learning, error) {
	if err := checkRepo(repo); err != nil {
		return nil, err
	}
	f, err := s.load(s.DB, repo)
	return f.Learnings, err
}

func (s SQLStore) Evaluation(repo, task string) (*Evaluation, error) {
	if err := checkRepo(repo); err != nil {
		return nil, err
	}
	if !validName(task, taskID) {
		return nil, errTask(task)
	}
	var data string
	err := s.DB.QueryRow(`SELECT data FROM evaluations WHERE repo = ? AND task = ?`, repo, task).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ev Evaluation
	if err := yaml.Unmarshal([]byte(data), &ev); err != nil {
		return nil, fmt.Errorf("evaluation %s of %s: %w", task, repo, err)
	}
	return &ev, nil
}

func (s SQLStore) evaluations(q queryer, repo string) ([]Evaluation, error) {
	rows, err := q.Query(`SELECT data FROM evaluations WHERE repo = ?`, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var evs []Evaluation
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var ev Evaluation
		if err := yaml.Unmarshal([]byte(data), &ev); err != nil {
			return nil, fmt.Errorf("evaluation of %s: %w", repo, err)
		}
		evs = append(evs, ev)
	}
	return evs, rows.Err()
}

// Evaluations returns up to limit stored evaluations, newest task first.
func (s SQLStore) Evaluations(repo string, limit int) ([]Evaluation, error) {
	if err := checkRepo(repo); err != nil {
		return nil, err
	}
	evs, err := s.evaluations(s.DB, repo)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(evs, func(a, b Evaluation) int { return b.TaskAt.Compare(a.TaskAt) })
	if limit > 0 && len(evs) > limit {
		evs = evs[:limit]
	}
	return evs, nil
}

// Apply records ev as the task's evaluation, replacing any earlier one, and folds
// it into the repository's learnings, in one transaction.
func (s SQLStore) Apply(repo string, ev Evaluation) error {
	if err := checkRepo(repo); err != nil {
		return err
	}
	if !validName(ev.Task, taskID) {
		return errTask(ev.Task)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	f, err := s.load(tx, repo)
	if err != nil {
		return err
	}
	ev = s.rules().apply(&f, ev)
	if err := saveLearnings(tx, repo, f); err != nil {
		return err
	}
	if err := saveEvaluation(tx, repo, ev); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.WriteGuidance(repo)
}

// WriteGuidance renders repo's active learnings to knowledge.md under Dir. It does
// nothing without a Dir. A failure leaves the database correct and the copy stale,
// so it is reported to the caller to log, never treated as a failed save.
func (s SQLStore) WriteGuidance(repo string) error {
	if s.Dir == "" {
		return nil
	}
	dir, err := Store{Dir: s.Dir}.repoDir(repo)
	if err != nil {
		return err
	}
	f, err := s.load(s.DB, repo)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, "knowledge.md"), []byte(render(f.Learnings)))
}

func (s SQLStore) Attempt(repo, task string) (*Attempt, error) {
	if err := checkRepo(repo); err != nil {
		return nil, err
	}
	if !validName(task, taskID) {
		return nil, errTask(task)
	}
	var data string
	err := s.DB.QueryRow(`SELECT data FROM attempts WHERE repo = ? AND task = ?`, repo, task).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var at Attempt
	if err := yaml.Unmarshal([]byte(data), &at); err != nil {
		return nil, err
	}
	return &at, nil
}

func (s SQLStore) RecordAttempt(repo, task string, at Attempt) error {
	if err := checkRepo(repo); err != nil {
		return err
	}
	if !validName(task, taskID) {
		return errTask(task)
	}
	raw, err := yaml.Marshal(at)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`INSERT INTO attempts (repo, task, data) VALUES (?, ?, ?) ON CONFLICT(repo, task) DO UPDATE SET data = excluded.data`, repo, task, string(raw))
	return err
}

func (s SQLStore) Select(repo, query string, limit, maxChars int) ([]Learning, error) {
	ls, err := s.Learnings(repo)
	if err != nil {
		return nil, err
	}
	return selectActive(ls, query, limit, maxChars), nil
}

// Snapshot reads repo's learnings and every evaluation, oldest task first, from one
// consistent read.
func (s SQLStore) Snapshot(repo string) (Snapshot, error) {
	if err := checkRepo(repo); err != nil {
		return Snapshot{}, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback()
	f, err := s.load(tx, repo)
	if err != nil {
		return Snapshot{}, err
	}
	evs, err := s.evaluations(tx, repo)
	if err != nil {
		return Snapshot{}, err
	}
	sortEvaluations(evs)
	return Snapshot{Format: FormatVersion, Repo: repo, NextID: f.NextID, Learnings: f.Learnings, Evaluations: evs}, nil
}

// Digest hashes the stored learnings and evaluations, so an ingest can tell whether
// anything wrote repo's knowledge while it was merging.
func (s SQLStore) Digest(repo string) (string, error) {
	if err := checkRepo(repo); err != nil {
		return "", err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	h := sha256.New()
	var learnings string
	err = tx.QueryRow(`SELECT data FROM learnings WHERE repo = ?`, repo).Scan(&learnings)
	if errors.Is(err, sql.ErrNoRows) {
		fmt.Fprintf(h, "learnings:missing\n")
	} else if err != nil {
		return "", err
	} else {
		fmt.Fprintf(h, "learnings:%d\n%s", len(learnings), learnings)
	}
	rows, err := tx.Query(`SELECT task, data FROM evaluations WHERE repo = ? ORDER BY task`, repo)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var task, data string
		if err := rows.Scan(&task, &data); err != nil {
			return "", err
		}
		fmt.Fprintf(h, "evaluation:%s:%d\n%s", task, len(data), data)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Replace stores x as repo's whole learning state, all or nothing: the database
// commits the learnings and every evaluation together, or none of them.
func (s SQLStore) Replace(repo string, x Snapshot) error {
	if err := checkRepo(repo); err != nil {
		return err
	}
	for _, ev := range x.Evaluations {
		if !validName(ev.Task, taskID) {
			return errTask(ev.Task)
		}
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := saveLearnings(tx, repo, learningsFile{NextID: x.NextID, Learnings: x.Learnings}); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM evaluations WHERE repo = ?`, repo); err != nil {
		return err
	}
	for _, ev := range x.Evaluations {
		if err := saveEvaluation(tx, repo, ev); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.WriteGuidance(repo)
}

// Lock takes the repository's evaluation lock without waiting. It returns ErrLocked
// while a live process holds it. The lock names its holder's pid, so one left by a
// process that died is taken over.
func (s SQLStore) Lock(repo string) (func(), error) {
	if err := checkRepo(repo); err != nil {
		return nil, err
	}
	tok := make([]byte, 8)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(tok)
	name := "learning:" + repo
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var pid int
	err = tx.QueryRow(`SELECT pid FROM locks WHERE name = ?`, name).Scan(&pid)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, err
	case pidAlive(pid):
		return nil, ErrLocked
	}
	if _, err := tx.Exec(`INSERT INTO locks (name, pid, token, since) VALUES (?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET pid = excluded.pid, token = excluded.token, since = excluded.since`,
		name, lockPID(), token, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return func() {
		_, _ = s.DB.Exec(`DELETE FROM locks WHERE name = ? AND token = ?`, name, token)
	}, nil
}

// Busy reports whether an evaluation of repo holds the lock now.
func (s SQLStore) Busy(repo string) bool {
	unlock, err := s.Lock(repo)
	if err != nil {
		return errors.Is(err, ErrLocked)
	}
	unlock()
	return false
}

// Repos lists every repository with stored knowledge.
func (s SQLStore) Repos() ([]string, error) {
	rows, err := s.DB.Query(`SELECT repo FROM learnings UNION SELECT repo FROM evaluations ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var repos []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		repos = append(repos, r)
	}
	return repos, rows.Err()
}

// lockPID is the process taking a lock, swapped in tests that need a second holder.
var lockPID = func() int { return osGetpid() }

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// ReadLearnings reads repo's learnings document through q.
func ReadLearnings(q queryer, repo string) (nextID int, learnings []Learning, err error) {
	f, err := SQLStore{}.load(q, repo)
	return f.NextID, f.Learnings, err
}

// WriteLearnings replaces repo's learnings document through q and leaves its
// evaluations and attempts alone.
func WriteLearnings(q execer, repo string, nextID int, learnings []Learning) error {
	if err := checkRepo(repo); err != nil {
		return err
	}
	return saveLearnings(q, repo, learningsFile{NextID: nextID, Learnings: learnings})
}

// Validate checks a snapshot the way an ingest does.
func (x Snapshot) Validate() error { return x.validate() }

// Repositories lists the repositories that have learnings, read through q.
func Repositories(q queryer) ([]string, error) {
	rows, err := q.Query(`SELECT repo FROM learnings ORDER BY repo`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
