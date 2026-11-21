package store

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/brymartinez/foreman/internal/knowledge"
)

// Legacy names the directories earlier versions kept state in.
type Legacy struct {
	TasksDir     string
	TriageDir    string
	KnowledgeDir string
}

// MigrationReport says what an upgrade did, for the one-line notice at startup.
type MigrationReport struct {
	Tasks, Triage, Repositories int
	// LinearRemoved counts the Linear task and triage records deleted without import.
	LinearRemoved int
	// Ran is false when the database was already migrated.
	Ran bool
}

const (
	metaImport  = "legacy_import"
	metaCleanup = "legacy_cleanup"
	// backupDir holds the files an import copied, beside the tasks they came from.
	// It starts with a dot, so a vault ignores it.
	backupDir = ".pre-sqlite"
)

// ErrRunnersActive means an upgrade found runners that could still write the
// files it is about to retire.
type ErrRunnersActive struct{ Tasks []string }

func (e ErrRunnersActive) Error() string {
	return fmt.Sprintf("upgrading to the SQLite store needs every runner stopped, but %s still running; wait for them to finish or cancel them from the dashboard of the previous version, then start foreman again", strings.Join(e.Tasks, ", "))
}

// MigrateLegacy copies the Markdown task files, confirmed triage records, and
// knowledge directories into db, then retires the originals. It is safe to run
// again after any interruption and does nothing once an upgrade finished.
//
// The copy is one transaction. It is validated by reading every row back and
// comparing it to its source before it commits, and the commit is the cutover:
// before it, reads and writes still belong to the files; after it, to the
// database. Runners that could still write the files block the upgrade, so an old
// runner never recreates a record that moved or writes to the retired store.
// Linear task and triage records are never imported; they are deleted after the
// commit, and a crash before that finishes is completed by the next run.
func MigrateLegacy(db *sql.DB, l Legacy) (MigrationReport, error) {
	var rep MigrationReport
	done, err := metaGet(db, metaImport)
	if err != nil {
		return rep, err
	}
	if done == "" {
		if err := importLegacy(db, l, &rep); err != nil {
			return rep, err
		}
		rep.Ran = true
	}
	if cleaned, err := metaGet(db, metaCleanup); err != nil {
		return rep, err
	} else if cleaned == "" {
		n, err := cleanupLegacy(l)
		if err != nil {
			return rep, err
		}
		rep.LinearRemoved += n
		if err := metaSet(db, metaCleanup, time.Now().UTC().Format(time.RFC3339)); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func metaGet(db rowQuerier, key string) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func metaSet(db *sql.DB, key, value string) error {
	_, err := db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// legacyHead is the part of a task file's frontmatter that decides what happens to it.
type legacyHead struct {
	Repo       string `yaml:"repo"`
	Source     string `yaml:"issue_source"`
	RunnerPID  int    `yaml:"runner_pid"`
	Identifier string `yaml:"issue_identifier"`
}

type legacyTask struct {
	name   string
	path   string
	raw    []byte
	head   legacyHead
	linear bool
}

func readLegacyTasks(dir string) ([]legacyTask, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []legacyTask
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		fm, _, ok := split(raw)
		if !ok {
			continue
		}
		var head legacyHead
		if err := yaml.Unmarshal(fm, &head); err != nil {
			return nil, fmt.Errorf("%s: frontmatter: %w", path, err)
		}
		if head.Repo == "" {
			continue // a note in the vault, not a task
		}
		out = append(out, legacyTask{name: e.Name(), path: path, raw: raw, head: head, linear: head.Source == "linear"})
	}
	return out, nil
}

type legacyTriage struct {
	id     string
	path   string
	linear bool
	text   string
}

// readLegacyTriage walks triage_dir for github/<owner>/<repo>/<n>.md and linear/<id>.md.
func readLegacyTriage(dir string) ([]legacyTriage, error) {
	var out []legacyTriage
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".md" || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		id := strings.TrimSuffix(filepath.ToSlash(rel), ".md")
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		t := legacyTriage{id: id, path: path, linear: strings.HasPrefix(id, "linear/")}
		if !t.linear {
			if !strings.HasPrefix(id, "github/") {
				return nil
			}
			text, ok := strings.CutPrefix(strings.ReplaceAll(string(raw), "\r\n", "\n"), triageHeader(id))
			if !ok || strings.TrimSpace(text) == "" {
				return fmt.Errorf("invalid confirmed triage record %s", path)
			}
			t.text = strings.TrimSpace(text)
		}
		out = append(out, t)
		return nil
	})
	return out, err
}

func importLegacy(db *sql.DB, l Legacy, rep *MigrationReport) error {
	unlock, err := lockLegacy(l.TasksDir)
	if err != nil {
		return err
	}
	defer unlock()

	tasks, err := readLegacyTasks(l.TasksDir)
	if err != nil {
		return err
	}
	var running []string
	for _, t := range tasks {
		if t.head.RunnerPID > 0 && pidAlive(t.head.RunnerPID) {
			running = append(running, fmt.Sprintf("%s (pid %d)", t.name, t.head.RunnerPID))
		}
	}
	if len(running) > 0 {
		return ErrRunnersActive{Tasks: running}
	}
	triage, err := readLegacyTriage(l.TriageDir)
	if err != nil {
		return err
	}
	repos, err := knowledge.LegacyRepos(l.KnowledgeDir)
	if err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Another process may have finished the same import while this one waited.
	if v, err := metaGet(tx, metaImport); err != nil || v != "" {
		return err
	}
	st := SQLite{DB: db}
	imported := map[string]string{}
	for _, t := range tasks {
		if t.linear {
			continue
		}
		rec, err := decode(t.raw)
		if err != nil {
			return fmt.Errorf("%s: %w", t.path, err)
		}
		if rec == nil {
			continue
		}
		if err := st.write(tx, rec, nil); err != nil {
			return fmt.Errorf("%s: %w", t.path, err)
		}
		// Keep the user's frontmatter keys, comments, and notes exactly as written.
		if _, err := tx.Exec(`UPDATE tasks SET document = ? WHERE id = ?`, string(t.raw), taskID(rec.Key)); err != nil {
			return err
		}
		imported[taskID(rec.Key)] = string(t.raw)
		rep.Tasks++
	}
	for _, t := range triage {
		if t.linear {
			continue
		}
		res, err := tx.Exec(`INSERT INTO triage (id, decisions, confirmed_at) VALUES (?, ?, ?) ON CONFLICT(id) DO NOTHING`, t.id, t.text, fileTime(t.path))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			rep.Triage++
		}
	}
	snaps := map[string]knowledge.Snapshot{}
	for _, repo := range repos {
		snap, attempts, err := knowledge.ReadLegacy(l.KnowledgeDir, repo)
		if err != nil {
			return err
		}
		if err := knowledge.WriteSnapshot(tx, snap, attempts); err != nil {
			return err
		}
		snaps[repo] = snap
		rep.Repositories++
	}

	// Validate the copy against its source before committing it.
	for id, want := range imported {
		var got string
		if err := tx.QueryRow(`SELECT document FROM tasks WHERE id = ?`, id).Scan(&got); err != nil || got != want {
			return fmt.Errorf("validate import: task %s did not copy exactly", id)
		}
	}
	for _, t := range triage {
		if t.linear {
			continue
		}
		var got string
		if err := tx.QueryRow(`SELECT decisions FROM triage WHERE id = ?`, t.id).Scan(&got); err != nil || got != t.text {
			return fmt.Errorf("validate import: triage %s did not copy exactly", t.id)
		}
	}
	for repo, want := range snaps {
		got, err := knowledge.ReadSnapshot(tx, repo)
		if err != nil {
			return err
		}
		if !knowledge.SameSnapshot(got, want) {
			return fmt.Errorf("validate import: knowledge of %s did not copy exactly", repo)
		}
	}
	if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, metaImport, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// The commit is the cutover. Retire the copied files; a failure here leaves them
	// to the next run, and nothing reads them.
	return retireTasks(l.TasksDir, tasks)
}

// lockLegacy takes the Markdown store's own lock, so an old process that still
// holds it finishes its write first and none starts one mid-copy.
func lockLegacy(dir string) (func(), error) {
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return func() {}, nil
	}
	f, err := os.OpenFile(filepath.Join(dir, ".foreman.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

func fileTime(path string) string {
	if info, err := os.Stat(path); err == nil {
		return info.ModTime().UTC().Format(time.RFC3339)
	}
	return time.Now().UTC().Format(time.RFC3339)
}

// retireTasks moves the copied task files into a hidden backup folder, so nothing
// still reads or writes them and the user keeps their originals. Linear files stay
// for cleanupLegacy.
func retireTasks(dir string, tasks []legacyTask) error {
	var firstErr error
	for _, t := range tasks {
		if t.linear {
			continue
		}
		if err := os.MkdirAll(filepath.Join(dir, backupDir), 0o700); err != nil {
			return err
		}
		if err := os.Rename(t.path, filepath.Join(dir, backupDir, t.name)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// cleanupLegacy deletes every saved Linear task and triage record, active and
// completed, without importing it. Branches, worktrees, PRs, runner logs, and the
// Linear tickets are left alone.
func cleanupLegacy(l Legacy) (int, error) {
	removed := 0
	tasks, err := readLegacyTasks(l.TasksDir)
	if err != nil {
		return removed, err
	}
	for _, t := range tasks {
		if !t.linear {
			continue
		}
		if err := os.Remove(t.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, err
		}
		removed++
	}
	triage, err := readLegacyTriage(l.TriageDir)
	if err != nil {
		return removed, err
	}
	for _, t := range triage {
		if !t.linear {
			continue
		}
		for _, p := range []string{t.path, t.path + ".lock"} {
			if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return removed, err
			}
		}
		removed++
	}
	return removed, nil
}
