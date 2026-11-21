package knowledge

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// LegacyRepos lists the repositories that have a knowledge directory under dir,
// laid out as <owner>/<repo>/.
func LegacyRepos(dir string) ([]string, error) {
	owners, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var repos []string
	for _, o := range owners {
		if !o.IsDir() || strings.HasPrefix(o.Name(), ".") {
			continue
		}
		names, err := os.ReadDir(filepath.Join(dir, o.Name()))
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			if n.IsDir() && !strings.HasPrefix(n.Name(), ".") && ValidRepo(o.Name()+"/"+n.Name()) {
				repos = append(repos, o.Name()+"/"+n.Name())
			}
		}
	}
	return repos, nil
}

// ReadLegacy reads one repository's file-based knowledge: its snapshot and the
// last evaluation attempt of each task. A save an interrupted ingest committed is
// finished first, so the files are whole.
func ReadLegacy(dir, repo string) (Snapshot, map[string]Attempt, error) {
	s := Store{Dir: dir}
	unlock, err := s.Lock(repo)
	if err != nil {
		return Snapshot{}, nil, err
	}
	defer unlock()
	snap, err := s.Snapshot(repo)
	if err != nil {
		return Snapshot{}, nil, err
	}
	attempts := map[string]Attempt{}
	rd, err := s.repoDir(repo)
	if err != nil {
		return Snapshot{}, nil, err
	}
	entries, err := os.ReadDir(filepath.Join(rd, "attempts"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Snapshot{}, nil, err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		task := strings.TrimSuffix(e.Name(), ".yaml")
		at, err := s.Attempt(repo, task)
		if err != nil {
			return Snapshot{}, nil, err
		}
		if at != nil {
			attempts[task] = *at
		}
	}
	return snap, attempts, nil
}

// WriteSnapshot stores a whole repository's knowledge and attempts in q, replacing
// what the database held for it. Pass a transaction to make it atomic.
func WriteSnapshot(q execer, snap Snapshot, attempts map[string]Attempt) error {
	if err := checkRepo(snap.Repo); err != nil {
		return err
	}
	if err := saveLearnings(q, snap.Repo, learningsFile{NextID: snap.NextID, Learnings: snap.Learnings}); err != nil {
		return err
	}
	if _, err := q.Exec(`DELETE FROM evaluations WHERE repo = ?`, snap.Repo); err != nil {
		return err
	}
	for _, ev := range snap.Evaluations {
		if !validName(ev.Task, taskID) {
			return errTask(ev.Task)
		}
		if err := saveEvaluation(q, snap.Repo, ev); err != nil {
			return err
		}
	}
	if _, err := q.Exec(`DELETE FROM attempts WHERE repo = ?`, snap.Repo); err != nil {
		return err
	}
	for task, at := range attempts {
		raw, err := yaml.Marshal(at)
		if err != nil {
			return err
		}
		if _, err := q.Exec(`INSERT INTO attempts (repo, task, data) VALUES (?, ?, ?)`, snap.Repo, task, string(raw)); err != nil {
			return err
		}
	}
	return nil
}

// ReadSnapshot reads repo's knowledge from q.
func ReadSnapshot(q queryer, repo string) (Snapshot, error) {
	s := SQLStore{}
	f, err := s.load(q, repo)
	if err != nil {
		return Snapshot{}, err
	}
	evs, err := s.evaluations(q, repo)
	if err != nil {
		return Snapshot{}, err
	}
	sortEvaluations(evs)
	return Snapshot{Format: FormatVersion, Repo: repo, NextID: f.NextID, Learnings: f.Learnings, Evaluations: evs}, nil
}

// SameSnapshot compares two snapshots by content, ignoring the export time.
func SameSnapshot(a, b Snapshot) bool {
	enc := func(x Snapshot) string {
		x.ExportedAt = time.Time{}
		raw, _ := yaml.Marshal(x)
		return string(raw)
	}
	return enc(a) == enc(b)
}
