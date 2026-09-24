package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FOREMAN_TASKS_DIR", "")

	cfg, err := Load()
	if err != nil || cfg.TasksDir != filepath.Join(home, ".local/share/foreman/tasks") {
		t.Fatalf("default: %+v %v", cfg, err)
	}

	os.MkdirAll(filepath.Join(home, ".config/foreman"), 0o755)
	os.WriteFile(filepath.Join(home, ".config/foreman/config.yaml"), []byte("tasks_dir: ~/Vault/Engineering/Tasks\n"), 0o644)
	cfg, err = Load()
	if err != nil || cfg.TasksDir != filepath.Join(home, "Vault/Engineering/Tasks") {
		t.Fatalf("config file: %+v %v", cfg, err)
	}

	t.Setenv("FOREMAN_TASKS_DIR", "/tmp/tasks")
	if cfg, _ = Load(); cfg.TasksDir != "/tmp/tasks" {
		t.Fatalf("env override: %+v", cfg)
	}
}
