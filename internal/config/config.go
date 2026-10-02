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
	TasksDir        string `yaml:"tasks_dir"`
	WorktreesDir    string `yaml:"worktrees_dir"`
	RepositoriesDir string `yaml:"repositories_dir"`
	LogDir          string `yaml:"log_dir"`
	ReviewRounds    int    `yaml:"review_rounds"` // reviewer passes per review; 0 means 3
	FixRounds       int    `yaml:"fix_rounds"`    // fix and re-review cycles before asking; 0 means 3
	// Mentions shows items the searches found only because they mention you.
	Mentions bool `yaml:"mentions"`
	// Refresh is how often the dashboard reads GitHub, in seconds.
	Refresh       int           `yaml:"refresh"`
	Budgets       Budgets       `yaml:"budgets"`
	Notifications Notifications `yaml:"notifications"`
	Claude        Claude        `yaml:"claude"`
	Codex         Codex         `yaml:"codex"`
	Cleanup       Cleanup       `yaml:"cleanup"`
	IgnoredRepos  []task.Repo   `yaml:"ignore_repositories"`
	// Path is the resolved config file used by config show/edit.
	Path string `yaml:"-"`
	// StagesDir holds the global stage instructions: stages/ next to the config file,
	// so ~/.config/foreman/stages by default. It is a convention, not a setting (D48).
	StagesDir string `yaml:"-"`
}

// DefaultRefresh and MinRefresh bound the dashboard's GitHub refresh, in seconds.
// The floor keeps a run of searches, the readiness gate, and the cleanup from
// overlapping each other and from spending the developer's gh rate limit.
const (
	DefaultRefresh = 60
	MinRefresh     = 5
)

type Budgets struct {
	ImplementerUSD float64 `yaml:"implementer_usd"`
	ReviewerUSD    float64 `yaml:"reviewer_usd"`
}

type Notifications struct {
	Enabled bool `yaml:"enabled"`
	// WebhookURL is the Discord webhook alerts post to. It is a secret.
	WebhookURL string `yaml:"webhook_url"`
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
	Model       string `yaml:"model"`        // implementation, including PR creation
	PlanModel   string `yaml:"plan_model"`   // "" plans with Model
	ReviewModel string `yaml:"review_model"` // "" reviews with Model
}

type Codex struct {
	Command               string `yaml:"command"`
	Model                 string `yaml:"model"`        // implementation, including PR creation
	PlanModel             string `yaml:"plan_model"`   // "" plans with Model
	ReviewModel           string `yaml:"review_model"` // "" reviews with Model
	ReasoningEffort       string `yaml:"reasoning_effort"`
	PlanReasoningEffort   string `yaml:"plan_reasoning_effort"`   // "" uses ReasoningEffort
	ReviewReasoningEffort string `yaml:"review_reasoning_effort"` // "" uses ReasoningEffort
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
		Mentions:        true,
		Refresh:         DefaultRefresh,
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
	if cfg.Refresh < MinRefresh {
		return Config{}, fmt.Errorf("%s: refresh is %d seconds; the minimum is %d", path, cfg.Refresh, MinRefresh)
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

// WebhookProblems returns the setup warnings for notifications: enabled without a
// webhook URL, or a URL kept in a file that other users can read. Neither stops
// foreman, and neither names the URL.
func (c Config) WebhookProblems() []string {
	n := c.Notifications
	switch {
	case !n.Enabled:
		return nil
	case n.WebhookURL == "":
		return []string{fmt.Sprintf("notifications are enabled but notifications.webhook_url is not set in %s; add a Discord webhook URL or set notifications.enabled: false", c.Path)}
	}
	if info, err := os.Stat(c.Path); err == nil && info.Mode().Perm()&0o077 != 0 {
		return []string{fmt.Sprintf("%s holds the Discord webhook URL and is readable by other users; run chmod 600 %s", c.Path, c.Path)}
	}
	return nil
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
