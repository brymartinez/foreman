package config

import (
	"os"
	"path/filepath"
	"strings"
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
	if cfg.WorktreesDir != filepath.Join(home, "wt") || cfg.RepositoriesDir != filepath.Join(home, ".local/share/foreman/repositories") || cfg.LogDir != filepath.Join(home, ".local/share/foreman/logs") {
		t.Errorf("dirs: %+v", cfg)
	}
	if cfg.Claude.Model != "haiku" || cfg.Claude.Command != "claude" {
		t.Errorf("claude: %+v", cfg.Claude)
	}
	if cfg.ReviewRounds != 3 || cfg.FixRounds != 3 {
		t.Errorf("rounds must load as their defaults so a materialized file shows them: %d %d", cfg.ReviewRounds, cfg.FixRounds)
	}
	if cfg.Codex.Command != "/opt/codex" || cfg.Codex.Model != "gpt-6-sol" {
		t.Errorf("codex: %+v", cfg.Codex)
	}
	if cfg.StagesDir != filepath.Join(home, ".config/foreman/stages") {
		t.Errorf("stages dir = %s", cfg.StagesDir)
	}
	if !cfg.Cleanup.OnDone || cfg.Cleanup.DeleteMergedBranch {
		t.Errorf("cleanup: an explicit false must override its default, and unset keys keep theirs: %+v", cfg.Cleanup)
	}
	if !cfg.Mentions || cfg.Refresh != DefaultRefresh {
		t.Errorf("unset mentions and refresh must keep today's behavior: %t %d", cfg.Mentions, cfg.Refresh)
	}
}

func TestLoadMentionsAndRefresh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FOREMAN_CONFIG", "")
	t.Setenv("FOREMAN_TASKS_DIR", "")
	os.MkdirAll(filepath.Join(home, ".config/foreman"), 0o755)
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(home, ".config/foreman/config.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("mentions: false\nrefresh: 30\n")
	cfg, err := Load()
	if err != nil || cfg.Mentions || cfg.Refresh != 30 {
		t.Fatalf("mentions %t refresh %d err %v", cfg.Mentions, cfg.Refresh, err)
	}

	write("refresh: 2\n")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "the minimum is 5") {
		t.Fatalf("a refresh below the floor was accepted: %v", err)
	}
}

func TestLoadValidatesIgnoredRepositories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FOREMAN_CONFIG", "")
	t.Setenv("FOREMAN_TASKS_DIR", "")
	os.MkdirAll(filepath.Join(home, ".config/foreman"), 0o755)
	os.WriteFile(filepath.Join(home, ".config/foreman/config.yaml"), []byte("ignore_repositories: [midsoln/foreman, acme/widgets]\n"), 0o644)
	cfg, err := Load()
	if err != nil || len(cfg.IgnoredRepos) != 2 || cfg.IgnoredRepos[1] != "acme/widgets" {
		t.Fatalf("ignored repos = %+v, err = %v", cfg.IgnoredRepos, err)
	}

	os.WriteFile(filepath.Join(home, ".config/foreman/config.yaml"), []byte("ignore_repositories: [not-a-repo]\n"), 0o644)
	if _, err := Load(); err == nil {
		t.Fatal("invalid ignored repository accepted")
	}
}

func TestMaterializeWritesDefaultsOnlyForEmptyFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := Config{TasksDir: "/tasks", Budgets: Budgets{ImplementerUSD: 3, ReviewerUSD: 1}, Notifications: Notifications{Enabled: true}}
	if err := Materialize(path, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 || !strings.Contains(string(raw), "implementer_usd: 3") {
		t.Fatalf("materialized config = %q, err = %v", raw, err)
	}
	if err := os.WriteFile(path, []byte("tasks_dir: /custom\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Materialize(path, Config{TasksDir: "/other"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "tasks_dir: /custom\n" {
		t.Fatalf("existing config replaced: %q", got)
	}
}
