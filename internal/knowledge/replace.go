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
)

// replaced are the files Replace swaps as one unit. attempts/ and learning.lock
// belong to this machine and stay.
var replaced = []string{"evaluations", "learnings.yaml", "knowledge.md"}

// rename is os.Rename, swapped in tests to fail one step of Replace.
var rename = os.Rename

// Replace stores x as repo's whole learning state: learnings.yaml, one file per
// evaluation, and knowledge.md rendered from the learnings. It stages the new files
// beside the old ones, moves the old ones aside, and moves the staged ones in. A
// failed step moves the old files back, so the repository keeps either its old
// state or the new one.
func (s Store) Replace(repo string, x Snapshot) error {
	dir, err := s.repoDir(repo)
	if err != nil {
		return err
	}
	for _, ev := range x.Evaluations {
		if !validName(ev.Task, taskID) {
			return errTask(ev.Task)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(dir, ".ingest-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := os.Mkdir(filepath.Join(stage, "evaluations"), 0o700); err != nil {
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
	backup, err := os.MkdirTemp(dir, ".previous-*")
	if err != nil {
		return err
	}
	var aside, in []string
	undo := func(cause error) error {
		for _, name := range slices.Backward(in) {
			_ = os.RemoveAll(filepath.Join(dir, name))
		}
		failed := false
		for _, name := range slices.Backward(aside) {
			if err := rename(filepath.Join(backup, name), filepath.Join(dir, name)); err != nil {
				failed = true
			}
		}
		if failed {
			// The old files stay in backup rather than being deleted with it.
			return fmt.Errorf("%w; the previous knowledge is kept in %s", cause, backup)
		}
		_ = os.RemoveAll(backup)
		return cause
	}
	for _, name := range replaced {
		err := rename(filepath.Join(dir, name), filepath.Join(backup, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return undo(err)
		}
		aside = append(aside, name)
	}
	for _, name := range replaced {
		if err := rename(filepath.Join(stage, name), filepath.Join(dir, name)); err != nil {
			return undo(err)
		}
		in = append(in, name)
	}
	return os.RemoveAll(backup)
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
