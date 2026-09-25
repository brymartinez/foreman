package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureTasksDirCreatesAfterConsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault", "tasks")
	ready, err := EnsureTasksDir(strings.NewReader("y\n"), &strings.Builder{}, path)
	if err != nil || !ready {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("tasks dir = %v, %v", info, err)
	}
}

func TestEnsureTasksDirDeclineLeavesMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks")
	var out strings.Builder
	ready, err := EnsureTasksDir(strings.NewReader("n\n"), &out, path)
	if err != nil || ready {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("path exists after decline: %v", err)
	}
	if !strings.Contains(out.String(), path) {
		t.Errorf("setup prompt does not name path: %s", out.String())
	}
}

func TestEnsureTasksDirRejectsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureTasksDir(strings.NewReader("y\n"), &strings.Builder{}, path); err == nil {
		t.Fatal("file path accepted as tasks directory")
	}
}

func TestEnsureTasksDirAcceptsExistingDirectoryWithoutInput(t *testing.T) {
	path := t.TempDir()
	ready, err := EnsureTasksDir(strings.NewReader(""), &strings.Builder{}, path)
	if err != nil || !ready {
		t.Fatal(err)
	}
}
