package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FOREMAN_CONFIG", "")
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

	alt := filepath.Join(home, "alt.yaml")
	os.WriteFile(alt, []byte("tasks_dir: /alt/tasks\n"), 0o644)
	t.Setenv("FOREMAN_CONFIG", alt)
	if cfg, _ = Load(); cfg.TasksDir != "/alt/tasks" || cfg.StagesDir != filepath.Join(home, "stages") {
		t.Fatalf("FOREMAN_CONFIG: %+v", cfg)
	}

	t.Setenv("FOREMAN_TASKS_DIR", "/tmp/tasks")
	if cfg, _ = Load(); cfg.TasksDir != "/tmp/tasks" {
		t.Fatalf("env override: %+v", cfg)
	}
}

func TestAgentSettingsOverlayDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FOREMAN_CONFIG", "")
	t.Setenv("FOREMAN_TASKS_DIR", "")
	os.MkdirAll(filepath.Join(home, ".config/foreman"), 0o755)
	os.WriteFile(filepath.Join(home, ".config/foreman/config.yaml"), []byte("worktrees_dir: ~/wt\nclaude:\n  model: haiku\ncodex:\n  command: /opt/codex\ncleanup:\n  delete_merged_branch: false\n"), 0o644)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorktreesDir != filepath.Join(home, "wt") || cfg.LogDir != filepath.Join(home, ".local/share/foreman/logs") {
		t.Errorf("dirs: %+v", cfg)
	}
	if cfg.Claude.Model != "haiku" || cfg.Claude.Command != "claude" || len(cfg.Claude.AllowedTools) != 3 {
		t.Errorf("claude: %+v", cfg.Claude)
	}
	if cfg.Codex.Command != "/opt/codex" {
		t.Errorf("codex: %+v", cfg.Codex)
	}
	if cfg.StagesDir != filepath.Join(home, ".config/foreman/stages") {
		t.Errorf("stages dir = %s", cfg.StagesDir)
	}
	if !cfg.Cleanup.OnDone || cfg.Cleanup.DeleteMergedBranch {
		t.Errorf("cleanup: an explicit false must override its default, and unset keys keep theirs: %+v", cfg.Cleanup)
	}
}
