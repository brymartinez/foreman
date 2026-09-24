// Package config resolves where foreman keeps its task files.
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
	TasksDir string `yaml:"tasks_dir"`
}

// Load reads, in order of precedence: FOREMAN_TASKS_DIR, then tasks_dir in
// ~/.config/foreman/config.yaml, then the default ~/.local/share/foreman/tasks.
func Load() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	cfg := Config{TasksDir: filepath.Join(home, ".local", "share", "foreman", "tasks")}
	path := filepath.Join(home, ".config", "foreman", "config.yaml")
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		var file Config
		if err := yaml.Unmarshal(raw, &file); err != nil {
			return Config{}, fmt.Errorf("%s: %w", path, err)
		}
		if file.TasksDir != "" {
			cfg.TasksDir = file.TasksDir
		}
	case !errors.Is(err, fs.ErrNotExist):
		return Config{}, err
	}
	if env := os.Getenv("FOREMAN_TASKS_DIR"); env != "" {
		cfg.TasksDir = env
	}
	cfg.TasksDir = expandHome(cfg.TasksDir, home)
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
