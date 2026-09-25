// Package config resolves foreman's directories and agent CLI settings.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	TasksDir     string  `yaml:"tasks_dir"`
	WorktreesDir string  `yaml:"worktrees_dir"`
	LogDir       string  `yaml:"log_dir"`
	ReviewRounds int     `yaml:"review_rounds"` // 0 means the default (3)
	Claude       Claude  `yaml:"claude"`
	Codex        Codex   `yaml:"codex"`
	Cleanup      Cleanup `yaml:"cleanup"`
	// StagesDir holds the global stage instructions: stages/ next to the config file,
	// so ~/.config/foreman/stages by default. It is a convention, not a setting (D48).
	StagesDir string `yaml:"-"`
}

// Cleanup controls what happens to a task's worktree once the task is DONE (D42).
type Cleanup struct {
	// OnDone removes DONE tasks' worktrees when inbox runs. Off means only `foreman clean`.
	OnDone bool `yaml:"on_done"`
	// DeleteMergedBranch deletes the local branch too, when its PR is merged.
	DeleteMergedBranch bool `yaml:"delete_merged_branch"`
}

type Claude struct {
	Command      string   `yaml:"command"`
	Model        string   `yaml:"model"`
	AllowedTools []string `yaml:"allowed_tools"`
}

type Codex struct {
	Command string `yaml:"command"`
	Model   string `yaml:"model"`
}

// Load starts from defaults, overlays the config file ($FOREMAN_CONFIG or
// ~/.config/foreman/config.yaml), then FOREMAN_TASKS_DIR. Unset file keys keep their defaults.
func Load() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	data := filepath.Join(home, ".local", "share", "foreman")
	cfg := Config{
		TasksDir:     filepath.Join(data, "tasks"),
		WorktreesDir: filepath.Join(data, "worktrees"),
		LogDir:       filepath.Join(data, "logs"),
		Claude:       Claude{Command: "claude", AllowedTools: []string{"Bash", "WebFetch", "WebSearch"}},
		Codex:        Codex{Command: "codex"},
		Cleanup:      Cleanup{OnDone: true, DeleteMergedBranch: true},
	}
	path := os.Getenv("FOREMAN_CONFIG")
	if path == "" {
		path = filepath.Join(home, ".config", "foreman", "config.yaml")
	}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("%s: %w", path, err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return Config{}, err
	}
	cfg.StagesDir = filepath.Join(filepath.Dir(path), "stages")
	if env := os.Getenv("FOREMAN_TASKS_DIR"); env != "" {
		cfg.TasksDir = env
	}
	for _, p := range []*string{&cfg.TasksDir, &cfg.WorktreesDir, &cfg.LogDir} {
		*p = expandHome(*p, home)
	}
	return cfg, nil
}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return p
}
