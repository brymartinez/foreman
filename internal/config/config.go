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
	TasksDir     string `yaml:"tasks_dir"`
	WorktreesDir string `yaml:"worktrees_dir"`
	LogDir       string `yaml:"log_dir"`
	ReviewRounds int    `yaml:"review_rounds"` // 0 means the default (3)
	Claude       Claude `yaml:"claude"`
	Codex        Codex  `yaml:"codex"`
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
