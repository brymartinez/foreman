package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// A save stages the new state in stageDir, then writes journalFile listing the
// staged evaluations. Writing the journal commits the save: from then on finish
// moves the staged files in, under the lock, until it completes, even after a
// crash. attempts/ and learning.lock belong to this machine and stay.
const (
	stageDir    = ".ingest"
	journalFile = "ingest.journal"
)

type journal struct {
	Evaluations []string `yaml:"evaluations"`
}

// ErrUnfinished means a committed save could not move every file in yet. The next
// Lock of the repository finishes it.
var ErrUnfinished = errors.New("knowledge: the save is committed but not finished; the next access to the repository finishes it")

// rename is os.Rename, swapped in tests to interrupt a save.
var rename = os.Rename

// Replace stores x as repo's whole learning state: learnings.yaml, one file per
// evaluation, and knowledge.md rendered from the learnings. The caller holds the
// repository's lock. A failure before the journal is written leaves the old state;
// after it, the new state is completed now or by the next Lock. Every file is
// replaced by a rename, so a reader sees a whole old or new file, never a gap.
func (s Store) Replace(repo string, x Snapshot) error {
	dir, err := s.repoDir(repo)
	if err != nil {
		return err
	}
	j := journal{Evaluations: []string{}}
	for _, ev := range x.Evaluations {
		if !validName(ev.Task, taskID) {
			return errTask(ev.Task)
		}
		j.Evaluations = append(j.Evaluations, ev.Task+".yaml")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := finish(dir); err != nil {
		return err
	}
	stage := filepath.Join(dir, stageDir)
	if err := s.stage(stage, x); err != nil {
		os.RemoveAll(stage)
		return err
	}
	if err := writeYAML(filepath.Join(dir, journalFile), j); err != nil {
		os.RemoveAll(stage)
		return err
	}
	if err := finish(dir); err != nil {
		return fmt.Errorf("%w: %v", ErrUnfinished, err)
	}
	return nil
}

func (s Store) stage(stage string, x Snapshot) error {
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(stage, "evaluations"), 0o700); err != nil {
		return err
	}
	if err := writeYAML(filepath.Join(stage, "learnings.yaml"), learningsFile{NextID: x.NextID, Learnings: x.Learnings}); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(stage, "knowledge.md"), []byte(render(x.Learnings))); err != nil {
		return err
	}
	for _, ev := range x.Evaluations {
		if err := writeYAML(filepath.Join(stage, "evaluations", ev.Task+".yaml"), ev); err != nil {
			return err
		}
	}
	return nil
}

// finish completes a committed save in dir, or drops a stage no journal commits.
// Each step can run again after an interruption: a staged file already moved in
// is gone from the stage, and only evaluations the journal does not list are
// removed. learnings.yaml moves last, because prompts and the learner read it.
func finish(dir string) error {
	stage := filepath.Join(dir, stageDir)
	var j journal
	err := readYAML(filepath.Join(dir, journalFile), &j)
	if errors.Is(err, fs.ErrNotExist) {
		return os.RemoveAll(stage)
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "evaluations"), 0o700); err != nil {
		return err
	}
	for _, name := range j.Evaluations {
		if !validName(strings.TrimSuffix(name, ".yaml"), taskID) {
			return fmt.Errorf("knowledge: invalid evaluation %q in %s", name, journalFile)
		}
		if err := moveIn(filepath.Join(stage, "evaluations", name), filepath.Join(dir, "evaluations", name)); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "evaluations"))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".yaml" && !slices.Contains(j.Evaluations, e.Name()) {
			if err := os.Remove(filepath.Join(dir, "evaluations", e.Name())); err != nil {
				return err
			}
		}
	}
	for _, name := range []string{"knowledge.md", "learnings.yaml"} {
		if err := moveIn(filepath.Join(stage, name), filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	if err := os.Remove(filepath.Join(dir, journalFile)); err != nil {
		return err
	}
	return os.RemoveAll(stage)
}

// moveIn renames a staged file over the live one, or does nothing when an earlier
// finish already moved it.
func moveIn(from, to string) error {
	if _, err := os.Lstat(from); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return rename(from, to)
}

// Digest hashes learnings.yaml and every evaluation file, so an ingest can tell
// whether anything wrote repo's knowledge while it was merging.
func (s Store) Digest(repo string) (string, error) {
	dir, err := s.repoDir(repo)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	add := func(name string) error {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(h, "%s:missing\n", name)
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s:%d\n", name, len(raw))
		h.Write(raw)
		return nil
	}
	if err := add("learnings.yaml"); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(filepath.Join(dir, "evaluations"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := add(filepath.Join("evaluations", e.Name())); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
