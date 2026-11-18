package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func TestTriageRecordIsConfirmedAndBoundToIssue(t *testing.T) {
	s := Triage{Dir: t.TempDir()}
	key := task.Key{Source: task.Linear, ExternalID: "11111111-1111-4111-8111-111111111111"}
	if text, err := s.Read(key); text != "" || err != nil {
		t.Fatalf("missing triage = %q, %v", text, err)
	}
	if err := s.Save(key, "Keep the existing API."); err != nil {
		t.Fatal(err)
	}
	key.Repo = "acme/api"
	if text, err := s.Read(key); text != "Keep the existing API." || err != nil {
		t.Fatalf("saved triage = %q, %v", text, err)
	}
	if err := s.Save(key, "Replacement"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second save = %v", err)
	}
	path := filepath.Join(s.Dir, "linear", key.ExternalID+".md")
	if err := os.WriteFile(path, []byte("Unconfirmed draft"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(key); err == nil {
		t.Fatal("accepted an unconfirmed document")
	}
}

func TestTriageStartLockUsesLinearIssueIdentity(t *testing.T) {
	s := Triage{Dir: t.TempDir()}
	key := task.Key{Source: task.Linear, ExternalID: "11111111-1111-4111-8111-111111111111", Repo: "acme/web"}
	unlock, err := s.LockStart(key)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	key.Repo = "acme/api"
	if release, err := s.LockStart(key); err == nil {
		release()
		t.Fatal("another repository acquired the same issue lock")
	}
}
