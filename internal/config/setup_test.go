package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureTasksDirCreatesAfterConsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault", "tasks")
	ready, err := EnsureTasksDir(bufio.NewReader(strings.NewReader("y\n")), &strings.Builder{}, path)
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
	ready, err := EnsureTasksDir(bufio.NewReader(strings.NewReader("n\n")), &out, path)
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
	if _, err := EnsureTasksDir(bufio.NewReader(strings.NewReader("y\n")), &strings.Builder{}, path); err == nil {
		t.Fatal("file path accepted as tasks directory")
	}
}

func TestEnsureTasksDirAcceptsExistingDirectoryWithoutInput(t *testing.T) {
	path := t.TempDir()
	ready, err := EnsureTasksDir(bufio.NewReader(strings.NewReader("")), &strings.Builder{}, path)
	if err != nil || !ready {
		t.Fatal(err)
	}
}

func TestEnsureTasksDirLeavesLaterAnswersForTheNextPrompt(t *testing.T) {
	in := bufio.NewReader(strings.NewReader("y\n1\nclaude\n"))
	if _, err := EnsureTasksDir(in, &strings.Builder{}, filepath.Join(t.TempDir(), "tasks")); err != nil {
		t.Fatal(err)
	}
	if next, _ := in.ReadString('\n'); next != "1\n" {
		t.Fatalf("next prompt reads %q", next)
	}
}
