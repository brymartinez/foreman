package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/brymartinez/foreman/internal/task"
)

// Triage stores one confirmed Markdown document per issue.
type Triage struct{ Dir string }

func (s Triage) path(key task.Key) (string, string, error) {
	switch key.Source {
	case task.Local:
		if !localIssueID.MatchString(key.ExternalID) {
			return "", "", errors.New("invalid local issue ID")
		}
		id := "local/" + strings.ToLower(key.ExternalID)
		return filepath.Join(s.Dir, id+".md"), id, nil
	case "", task.GitHub:
		repo, err := task.ParseRepo(string(key.Repo))
		if err != nil || key.Number <= 0 {
			return "", "", errors.New("invalid GitHub issue identity")
		}
		for _, part := range strings.Split(string(repo), "/") {
			if part == "." || part == ".." {
				return "", "", errors.New("invalid GitHub issue repository")
			}
		}
		id := "github/" + strings.ToLower(string(repo)) + "/" + strconv.Itoa(key.Number)
		return filepath.Join(s.Dir, id+".md"), id, nil
	default:
		return "", "", fmt.Errorf("invalid issue source %q", key.Source)
	}
}

func triageHeader(id string) string {
	return "<!-- foreman:confirmed-triage " + id + " -->\n# Triage decisions\n\n"
}

// Read returns empty text for an issue that has yet to be triaged.
func (s Triage) Read(key task.Key) (string, error) {
	path, id, err := s.path(key)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	text, ok := strings.CutPrefix(strings.ReplaceAll(string(raw), "\r\n", "\n"), triageHeader(id))
	if !ok || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("invalid confirmed triage record %s", path)
	}
	return strings.TrimSpace(text), nil
}

// Save installs the first confirmed record atomically. A second interview must
// read the record that won before continuing.
func (s Triage) Save(key task.Key, decisions string) error {
	path, id, err := s.path(key)
	if err != nil {
		return err
	}
	decisions = strings.TrimSpace(decisions)
	if decisions == "" {
		return errors.New("confirmed triage is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".triage-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.WriteString(triageHeader(id) + decisions + "\n")
	if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
		return err
	}
	return os.Link(f.Name(), path)
}

// LockStart serializes starts by issue even when two local starts pick
// different repositories and therefore different task files.
func (s Triage) LockStart(key task.Key) (func(), error) {
	path, _, err := s.path(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("issue is already starting")
		}
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}
