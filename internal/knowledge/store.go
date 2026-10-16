package knowledge

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"go.yaml.in/yaml/v3"
)

// learningsFile is the source of truth; knowledge.md is rendered from it.
type learningsFile struct {
	NextID    int        `yaml:"next_id"`
	Learnings []Learning `yaml:"learnings"`
}

// repoDir returns Dir/<owner>/<repo>, rejecting anything that could leave it.
func (s Store) repoDir(repo string) (string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || !validName(owner, repoPart) || !validName(name, repoPart) {
		return "", fmt.Errorf("knowledge: invalid repository %q", repo)
	}
	return filepath.Join(s.Dir, owner, name), nil
}

// Learnings returns every learning for repo, including candidates and retired ones.
// A repository with no store has none.
func (s Store) Learnings(repo string) ([]Learning, error) {
	f, err := s.load(repo)
	return f.Learnings, err
}

func (s Store) load(repo string) (learningsFile, error) {
	dir, err := s.repoDir(repo)
	if err != nil {
		return learningsFile{}, err
	}
	var f learningsFile
	err = readYAML(filepath.Join(dir, "learnings.yaml"), &f)
	if errors.Is(err, fs.ErrNotExist) {
		return learningsFile{}, nil
	}
	return f, err
}

// Evaluation returns the stored evaluation of one task, or nil.
func (s Store) Evaluation(repo, task string) (*Evaluation, error) {
	dir, err := s.repoDir(repo)
	if err != nil {
		return nil, err
	}
	if !validName(task, taskID) {
		return nil, errTask(task)
	}
	var ev Evaluation
	err = readYAML(filepath.Join(dir, "evaluations", task+".yaml"), &ev)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ev, nil
}

// Evaluations returns up to limit stored evaluations, newest task first.
func (s Store) Evaluations(repo string, limit int) ([]Evaluation, error) {
	dir, err := s.repoDir(repo)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(dir, "evaluations"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var evs []Evaluation
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		var ev Evaluation
		if err := readYAML(filepath.Join(dir, "evaluations", e.Name()), &ev); err != nil {
			return nil, err
		}
		evs = append(evs, ev)
	}
	slices.SortFunc(evs, func(a, b Evaluation) int { return b.TaskAt.Compare(a.TaskAt) })
	if limit > 0 && len(evs) > limit {
		evs = evs[:limit]
	}
	return evs, nil
}

// Apply records ev as the task's evaluation, replacing any earlier one, folds it
// into the repository's learnings, and rewrites knowledge.md.
func (s Store) Apply(repo string, ev Evaluation) error {
	dir, err := s.repoDir(repo)
	if err != nil {
		return err
	}
	if !validName(ev.Task, taskID) {
		return errTask(ev.Task)
	}
	f, err := s.load(repo)
	if err != nil {
		return err
	}
	ev = s.apply(&f, ev)
	if err := os.MkdirAll(filepath.Join(dir, "evaluations"), 0o700); err != nil {
		return err
	}
	if err := writeYAML(filepath.Join(dir, "learnings.yaml"), f); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, "knowledge.md"), []byte(render(f.Learnings))); err != nil {
		return err
	}
	// The evaluation goes last: its fingerprint tells the refresh the task is done,
	// so a failed write above leaves the task due. Re-applying is safe, because apply
	// first withdraws the task's earlier evidence.
	return writeYAML(filepath.Join(dir, "evaluations", ev.Task+".yaml"), ev)
}

// Lock takes the repository's evaluation lock without waiting. It returns ErrLocked
// while another process holds it.
func (s Store) Lock(repo string) (func(), error) {
	dir, err := s.repoDir(repo)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "learning.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	unlock := func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		lock.Close()
	}
	// An ingest interrupted after it committed finishes before anything else reads
	// or writes the repository under the lock.
	if err := finish(dir); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

// Busy reports whether an evaluation of repo holds the lock now.
func (s Store) Busy(repo string) bool {
	unlock, err := s.Lock(repo)
	if err != nil {
		return errors.Is(err, ErrLocked)
	}
	unlock()
	return false
}

func readYAML(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func writeYAML(path string, v any) error {
	raw, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	return writeFile(path, raw)
}

// writeFile replaces path atomically, so a reader never sees half a file.
func writeFile(path string, raw []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return rename(tmp.Name(), path)
}

var kindTitles = []struct {
	kind  Kind
	title string
}{
	{Convention, "Conventions"},
	{Fact, "Repository facts"},
	{Practice, "Working practices"},
}

// render writes the active learnings only, without ids or evidence, so the file
// holds nothing but generic guidance.
func render(ls []Learning) string {
	var b strings.Builder
	b.WriteString("# Repository knowledge\n\n")
	b.WriteString("Foreman learned these from earlier tasks in this repository. They may be wrong. ")
	b.WriteString("Repository instructions, the issue, recorded decisions, and verified facts take precedence.\n")
	for _, kt := range kindTitles {
		var lines []string
		for _, l := range ls {
			if l.Status == Active && l.Kind == kt.kind {
				lines = append(lines, "- "+l.Text)
			}
		}
		if len(lines) > 0 {
			fmt.Fprintf(&b, "\n## %s\n\n%s\n", kt.title, strings.Join(lines, "\n"))
		}
	}
	return b.String()
}

func normalize(text string) string {
	return strings.TrimRight(strings.ToLower(strings.Join(strings.Fields(text), " ")), ".")
}

func nextID(f *learningsFile) string {
	f.NextID = cmp.Or(f.NextID, len(f.Learnings)) + 1
	return fmt.Sprintf("L%d", f.NextID)
}

// Attempt is when an evaluation of a task last started and the evidence it started
// from. A refresh reads it to wait before retrying an evaluation that keeps failing.
type Attempt struct {
	Fingerprint string    `yaml:"fingerprint"`
	At          time.Time `yaml:"at"`
}

// Attempt returns the task's last evaluation attempt, or nil.
func (s Store) Attempt(repo, task string) (*Attempt, error) {
	path, err := s.attemptPath(repo, task)
	if err != nil {
		return nil, err
	}
	var at Attempt
	err = readYAML(path, &at)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &at, nil
}

// RecordAttempt stores the task's latest evaluation attempt, replacing the last one.
func (s Store) RecordAttempt(repo, task string, at Attempt) error {
	path, err := s.attemptPath(repo, task)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeYAML(path, at)
}

func (s Store) attemptPath(repo, task string) (string, error) {
	dir, err := s.repoDir(repo)
	if err != nil {
		return "", err
	}
	if !validName(task, taskID) {
		return "", errTask(task)
	}
	return filepath.Join(dir, "attempts", task+".yaml"), nil
}
