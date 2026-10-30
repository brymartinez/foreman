// Package config resolves foreman's directories and agent CLI settings.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
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
	// KnowledgeDir holds what foreman learns, one directory per repository.
	KnowledgeDir string `yaml:"knowledge_dir"`
	ReviewRounds int    `yaml:"review_rounds"` // reviewer passes per review; 0 means 3
	FixRounds    int    `yaml:"fix_rounds"`    // fix and re-review cycles before asking; 0 means 3
	// ReviewBeforePR has the agent review finish on the local branch before the PR
	// opens. A task keeps the value it started with.
	ReviewBeforePR bool `yaml:"review_before_pr"`
	// Mentions shows items the searches found only because they mention you.
	Mentions bool `yaml:"mentions"`
	// DefaultSource is the issue tracker the dashboard shows when -s is not given:
	// github, linear, or all.
	DefaultSource task.Source `yaml:"default_source"`
	// Refresh is how often the dashboard reads GitHub, in seconds.
	Refresh       int           `yaml:"refresh"`
	Budgets       Budgets       `yaml:"budgets"`
	Learning      Learning      `yaml:"learning"`
	Notifications Notifications `yaml:"notifications"`
	Claude        Claude        `yaml:"claude"`
	Codex         Codex         `yaml:"codex"`
	Cleanup       Cleanup       `yaml:"cleanup"`
	Auto          Auto          `yaml:"auto"`
	// IncludedRepos, when nonempty, limits the inbox to these repositories and
	// overrides ExcludedRepos.
	IncludedRepos  []task.Repo          `yaml:"include_repositories"`
	ExcludedRepos  []task.Repo          `yaml:"exclude_repositories"`
	LinearProjects map[task.Repo]string `yaml:"linear_projects"`
	// Path is the resolved config file used by config show/edit.
	Path string `yaml:"-"`
	// StagesDir holds the global stage instructions: stages/ next to the config file,
	// so ~/.config/foreman/stages by default. It is a convention, not a setting.
	StagesDir string `yaml:"-"`
}

// SourceAll selects every issue tracker. It is a dashboard choice, not a task's source.
const SourceAll task.Source = "all"

// DefaultRefresh and MinRefresh bound the dashboard's GitHub refresh, in seconds.
// The floor keeps a run of searches, the readiness gate, and the cleanup from
// overlapping each other and from spending the developer's gh rate limit.
const (
	DefaultRefresh = 60
	MinRefresh     = 5
)

type Budgets struct {
	TaskTokens         int64   `yaml:"task_tokens"`
	PlanningTokens     int64   `yaml:"planning_tokens"`
	CallTimeoutSeconds int     `yaml:"call_timeout_seconds"`
	ImplementerUSD     float64 `yaml:"implementer_usd"`
	ReviewerUSD        float64 `yaml:"reviewer_usd"`
}

// Learning controls the evaluation of finished tasks. Off stops new evaluations;
// prompts still read the knowledge already learned.
type Learning struct {
	Enabled bool `yaml:"enabled"`
	// BudgetUSD caps one Claude evaluation's spend; 0 sets no cap.
	BudgetUSD float64 `yaml:"budget_usd"`
}

type Notifications struct {
	Enabled bool `yaml:"enabled"`
	// WebhookURL is the Discord webhook alerts post to. It is a secret.
	WebhookURL string `yaml:"webhook_url"`
}

// Cleanup controls what happens to a task's worktree once the task is DONE.
type Cleanup struct {
	// OnDone removes DONE tasks' worktrees when inbox runs. Off keeps them.
	OnDone bool `yaml:"on_done"`
	// DeleteMergedBranch deletes the local branch too, when its PR is merged.
	DeleteMergedBranch bool `yaml:"delete_merged_branch"`
}

// Auto sets what the dashboard's auto mode starts. Whether auto mode is on is
// saved in the state file, not here, so toggling it never rewrites this file.
type Auto struct {
	Agent task.Agent `yaml:"agent"`
	// MaxConcurrent caps active runs, manual ones included, before auto mode waits.
	MaxConcurrent int `yaml:"max_concurrent"`
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
		KnowledgeDir:    filepath.Join(data, "knowledge"),
		ReviewRounds:    3,
		FixRounds:       3,
		Mentions:        true,
		DefaultSource:   task.GitHub,
		Refresh:         DefaultRefresh,
		Claude:          Claude{Command: "claude", Model: "claude-opus-5"},
		Codex:           Codex{Command: "codex", Model: "gpt-6-sol"},
		Cleanup:         Cleanup{OnDone: true, DeleteMergedBranch: true},
		Budgets:         Budgets{ImplementerUSD: 3, ReviewerUSD: 1, TaskTokens: 2000000, PlanningTokens: 400000, CallTimeoutSeconds: 1800},
		Learning:        Learning{Enabled: true, BudgetUSD: 0.5},
		Notifications:   Notifications{Enabled: true},
		Auto:            Auto{Agent: task.Claude, MaxConcurrent: 3},
	}
	path, err := FilePath()
	if err != nil {
		return Config{}, err
	}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		// KnownFields makes a misspelled or retired key an error that names it,
		// instead of a setting that silently keeps its default.
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
			return Config{}, fmt.Errorf("%s: %w", path, err)
		}
		// A later document would be ignored, along with any key in it. An empty one,
		// such as a trailing ---, holds no setting.
		for {
			var extra yaml.Node
			err := dec.Decode(&extra)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return Config{}, fmt.Errorf("%s: %w", path, err)
			}
			if len(extra.Content) > 0 && !emptyDocument(extra.Content[0]) {
				return Config{}, fmt.Errorf("%s: line %d: config must be one YAML document", path, extra.Content[0].Line)
			}
		}
	case !errors.Is(err, fs.ErrNotExist):
		return Config{}, err
	}
	cfg.StagesDir = filepath.Join(filepath.Dir(path), "stages")
	cfg.Path = path
	if env := os.Getenv("FOREMAN_TASKS_DIR"); env != "" {
		cfg.TasksDir = env
	}
	for _, p := range []*string{&cfg.TasksDir, &cfg.WorktreesDir, &cfg.RepositoriesDir, &cfg.LogDir, &cfg.KnowledgeDir} {
		*p = expandHome(*p, home)
	}
	if cfg.Refresh < MinRefresh {
		return Config{}, fmt.Errorf("%s: refresh is %d seconds; the minimum is %d", path, cfg.Refresh, MinRefresh)
	}
	if cfg.DefaultSource == "" { // default_source: with no value means unset
		cfg.DefaultSource = task.GitHub
	}
	if err := ValidSource(cfg.DefaultSource); err != nil {
		return Config{}, fmt.Errorf("%s: default_source: %w", path, err)
	}
	if cfg.Budgets.TaskTokens < 0 || cfg.Budgets.PlanningTokens < 0 || cfg.Budgets.CallTimeoutSeconds < 0 || cfg.Budgets.ImplementerUSD < 0 || cfg.Budgets.ReviewerUSD < 0 {
		return Config{}, fmt.Errorf("%s: budgets cannot be negative", path)
	}
	if cfg.Learning.BudgetUSD < 0 {
		return Config{}, fmt.Errorf("%s: learning.budget_usd is %g; it cannot be negative", path, cfg.Learning.BudgetUSD)
	}
	if _, err := task.ParseAgent(string(cfg.Auto.Agent)); err != nil {
		return Config{}, fmt.Errorf("%s: auto.agent: %w", path, err)
	}
	if cfg.Auto.MaxConcurrent < 1 {
		return Config{}, fmt.Errorf("%s: auto.max_concurrent is %d; the minimum is 1", path, cfg.Auto.MaxConcurrent)
	}
	if err := parseRepoList("include_repositories", cfg.IncludedRepos); err != nil {
		return Config{}, err
	}
	if err := parseRepoList("exclude_repositories", cfg.ExcludedRepos); err != nil {
		return Config{}, err
	}
	seenProjects := make(map[string]task.Repo)
	for repo, projectURL := range cfg.LinearProjects {
		if _, err := task.ParseRepo(string(repo)); err != nil {
			return Config{}, fmt.Errorf("linear_projects: %w", err)
		}
		canonical, err := linearProjectURL(projectURL)
		if err != nil {
			return Config{}, fmt.Errorf("linear_projects[%s]: %w", repo, err)
		}
		if other, ok := seenProjects[canonical]; ok {
			return Config{}, fmt.Errorf("linear project %s mapped to both %s and %s", canonical, other, repo)
		}
		seenProjects[canonical] = repo
		cfg.LinearProjects[repo] = canonical
	}
	return cfg, nil
}

// emptyDocument reports whether n is what yaml reads for a document with no
// content: an untagged, unanchored null scalar with no text. An explicit null, ~,
// tag, or anchor is content.
func emptyDocument(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null" && n.Value == "" && n.Style == 0 && n.Anchor == ""
}

// ValidSource accepts the dashboard's issue source choices: github, linear, or all.
func ValidSource(s task.Source) error {
	switch s {
	case task.GitHub, task.Linear, SourceAll:
		return nil
	}
	return fmt.Errorf("unknown issue source %q (want github, linear, or all)", s)
}

func (c Config) IssueSource(repo task.Repo) task.Source {
	if _, ok := c.LinearProjects[repo]; ok {
		return task.Linear
	}
	return task.GitHub
}

func linearProjectURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "linear.app" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid Linear project URL %q", raw)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || len(parts) > 4 || parts[0] == "" || parts[1] != "project" || parts[2] == "" || (len(parts) == 4 && parts[3] != "overview" && parts[3] != "issues") {
		return "", fmt.Errorf("invalid Linear project URL %q", raw)
	}
	return "https://linear.app/" + strings.Join(parts[:3], "/"), nil
}

// parseRepoList validates each entry of the config list key and stores the parsed form in place.
func parseRepoList(key string, repos []task.Repo) error {
	for i, repo := range repos {
		parsed, err := task.ParseRepo(string(repo))
		if err != nil {
			return fmt.Errorf("%s[%d]: %w", key, i, err)
		}
		repos[i] = parsed
	}
	return nil
}

// FilePath is the config file: $FOREMAN_CONFIG, or ~/.config/foreman/config.yaml.
// It doesn't read the file, so `foreman -c` can open one that fails to load.
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

// StatePath is the file holding choices the dashboard saves between launches.
func StatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "foreman", "state.json"), nil
}

type state struct {
	Auto *bool `json:"auto,omitempty"`
}

// LoadAutoEnabled reads the saved auto mode choice. A missing file or key means off.
func LoadAutoEnabled(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var s state
	if err := json.Unmarshal(raw, &s); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if s.Auto == nil {
		return false, nil
	}
	return *s.Auto, nil
}

// SaveAutoEnabled writes the auto mode choice through a temp file and a rename,
// so a crash mid-write never leaves a corrupt state file.
func SaveAutoEnabled(path string, on bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(state{Auto: &on})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Materialize writes the resolved configuration only when the config file is
// missing or empty. This gives `foreman -c` useful defaults without replacing
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
