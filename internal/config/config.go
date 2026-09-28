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

	"github.com/brymartinez/foreman/internal/task"
)

type Config struct {
	TasksDir        string        `yaml:"tasks_dir"`
	WorktreesDir    string        `yaml:"worktrees_dir"`
	RepositoriesDir string        `yaml:"repositories_dir"`
	LogDir          string        `yaml:"log_dir"`
	ReviewRounds    int           `yaml:"review_rounds"` // reviewer passes per review; 0 means 3
	FixRounds       int           `yaml:"fix_rounds"`    // fix and re-review cycles before asking; 0 means 3
	Budgets         Budgets       `yaml:"budgets"`
	Notifications   Notifications `yaml:"notifications"`
	Claude          Claude        `yaml:"claude"`
	Codex           Codex         `yaml:"codex"`
	Cleanup         Cleanup       `yaml:"cleanup"`
	IgnoredRepos    []task.Repo   `yaml:"ignore_repositories"`
	// Path is the resolved config file used by config show/edit.
	Path string `yaml:"-"`
	// StagesDir holds the global stage instructions: stages/ next to the config file,
	// so ~/.config/foreman/stages by default. It is a convention, not a setting (D48).
	StagesDir string `yaml:"-"`
}

type Budgets struct {
	ImplementerUSD float64 `yaml:"implementer_usd"`
	ReviewerUSD    float64 `yaml:"reviewer_usd"`
}

type Notifications struct {
	Enabled bool `yaml:"enabled"`
}

// Cleanup controls what happens to a task's worktree once the task is DONE (D42).
type Cleanup struct {
	// OnDone removes DONE tasks' worktrees when inbox runs. Off means only `foreman clean`.
	OnDone bool `yaml:"on_done"`
	// DeleteMergedBranch deletes the local branch too, when its PR is merged.
	DeleteMergedBranch bool `yaml:"delete_merged_branch"`
}

type Claude struct {
	Command     string `yaml:"command"`
	Model       string `yaml:"model"`
	ReviewModel string `yaml:"review_model"` // "" reviews with Model
}

type Codex struct {
	Command     string `yaml:"command"`
	Model       string `yaml:"model"`
	ReviewModel string `yaml:"review_model"` // "" reviews with Model
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
		TasksDir:        filepath.Join(data, "tasks"),
		WorktreesDir:    filepath.Join(data, "worktrees"),
		RepositoriesDir: filepath.Join(data, "repositories"),
		LogDir:          filepath.Join(data, "logs"),
		ReviewRounds:    3,
		FixRounds:       3,
		Claude:          Claude{Command: "claude", Model: "claude-opus-5"},
		Codex:           Codex{Command: "codex", Model: "gpt-6-sol"},
		Cleanup:         Cleanup{OnDone: true, DeleteMergedBranch: true},
		Budgets:         Budgets{ImplementerUSD: 3, ReviewerUSD: 1},
		Notifications:   Notifications{Enabled: true},
	}
	path, err := FilePath()
	if err != nil {
		return Config{}, err
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
	cfg.Path = path
	if env := os.Getenv("FOREMAN_TASKS_DIR"); env != "" {
		cfg.TasksDir = env
	}
	for _, p := range []*string{&cfg.TasksDir, &cfg.WorktreesDir, &cfg.RepositoriesDir, &cfg.LogDir} {
		*p = expandHome(*p, home)
	}
	for i, repo := range cfg.IgnoredRepos {
		parsed, err := task.ParseRepo(string(repo))
		if err != nil {
			return Config{}, fmt.Errorf("ignore_repositories[%d]: %w", i, err)
		}
		cfg.IgnoredRepos[i] = parsed
	}
	return cfg, nil
}

// FilePath is the config file: $FOREMAN_CONFIG, or ~/.config/foreman/config.yaml.
// It doesn't read the file, so `config --edit` can open one that fails to load.
func FilePath() (string, error) {
	if path := os.Getenv("FOREMAN_CONFIG"); path != "" {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "foreman", "config.yaml"), nil
}

// Materialize writes the resolved configuration only when the config file is
// missing or empty. This gives `config --edit` useful defaults without replacing
// an existing user's file.
func Materialize(path string, cfg Config) error {
	info, err := os.Stat(path)
	if err == nil && info.Size() > 0 {
		return nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
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
