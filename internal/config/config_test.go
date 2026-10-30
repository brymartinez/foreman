package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
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
	if cfg.ReviewRounds != 1 || cfg.FixRounds != 3 {
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

func TestStageModelsAndEffortLoadFromConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("FOREMAN_CONFIG", path)
	t.Setenv("FOREMAN_TASKS_DIR", "")
	raw := "claude:\n  model: opus\n  plan_model: sonnet\n  review_model: opus\ncodex:\n  model: gpt-6-sol\n  plan_model: gpt-6-luna\n  review_model: gpt-6-sol\n  reasoning_effort: medium\n  plan_reasoning_effort: xhigh\n  review_reasoning_effort: medium\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Claude.Model != "opus" || cfg.Claude.PlanModel != "sonnet" || cfg.Claude.ReviewModel != "opus" {
		t.Errorf("claude stage models = %+v", cfg.Claude)
	}
	if cfg.Codex.Model != "gpt-6-sol" || cfg.Codex.PlanModel != "gpt-6-luna" || cfg.Codex.ReviewModel != "gpt-6-sol" || cfg.Codex.ReasoningEffort != "medium" || cfg.Codex.PlanReasoningEffort != "xhigh" || cfg.Codex.ReviewReasoningEffort != "medium" {
		t.Errorf("codex stage settings = %+v", cfg.Codex)
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

func TestLoadValidatesRepositoryLists(t *testing.T) {
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

	write("include_repositories: [midsoln/foreman]\nexclude_repositories: [midsoln/foreman, acme/widgets]\n")
	cfg, err := Load()
	if err != nil || len(cfg.IncludedRepos) != 1 || cfg.IncludedRepos[0] != "midsoln/foreman" || len(cfg.ExcludedRepos) != 2 || cfg.ExcludedRepos[1] != "acme/widgets" {
		t.Fatalf("included = %+v, excluded = %+v, err = %v", cfg.IncludedRepos, cfg.ExcludedRepos, err)
	}

	for _, key := range []string{"include_repositories", "exclude_repositories"} {
		write(key + ": [midsoln/foreman, not-a-repo]\n")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), key+"[1]") {
			t.Errorf("an invalid %s entry gave err = %v, want one naming %s[1]", key, err, key)
		}
	}
}

func TestLinearProjectsSelectMappedRepositories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("FOREMAN_CONFIG", path)
	t.Setenv("FOREMAN_TASKS_DIR", "")
	if err := os.WriteFile(path, []byte("linear_projects:\n  acme/web: https://linear.app/acme/project/web-site-123/overview\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.IssueSource("acme/web"); got != task.Linear {
		t.Errorf("mapped repo source = %q, want linear", got)
	}
	if got := cfg.IssueSource("acme/api"); got != task.GitHub {
		t.Errorf("unmapped repo source = %q, want github", got)
	}
	if got := cfg.LinearProjects["acme/web"]; got != "https://linear.app/acme/project/web-site-123" {
		t.Errorf("normalized project URL = %q", got)
	}
}

func TestLinearProjectsRejectInvalidRepositoriesAndDuplicateProjects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("FOREMAN_CONFIG", path)
	t.Setenv("FOREMAN_TASKS_DIR", "")
	for _, tc := range []struct{ name, body, want string }{
		{"invalid repository", "linear_projects:\n  invalid: https://linear.app/acme/project/web-site-123\n", "invalid repo"},
		{"duplicate project", "linear_projects:\n  acme/web: https://linear.app/acme/project/web-site-123\n  acme/api: https://linear.app/acme/project/web-site-123/overview\n", "mapped to both"},
		{"non project URL", "linear_projects:\n  acme/web: https://linear.app/acme/issue/ABC-123\n", "project URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestMaterializeWritesDefaultsOnlyForEmptyFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := Config{TasksDir: "/tasks", Claude: Claude{Model: "opus", PlanModel: "sonnet"}, Codex: Codex{Model: "gpt-6-sol", PlanModel: "gpt-6-luna", ReasoningEffort: "medium", PlanReasoningEffort: "xhigh"}, Budgets: Budgets{ImplementerUSD: 3, ReviewerUSD: 1}, Notifications: Notifications{Enabled: true}}
	if err := Materialize(path, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 || !strings.Contains(string(raw), "implementer_usd: 3") || !strings.Contains(string(raw), "plan_model: sonnet") || !strings.Contains(string(raw), "plan_reasoning_effort: xhigh") {
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

func TestLoadReadsWebhookURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("FOREMAN_CONFIG", path)
	os.WriteFile(path, []byte("notifications:\n  webhook_url: https://discord.com/api/webhooks/1/token\n"), 0o600)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Notifications.Enabled || cfg.Notifications.WebhookURL != "https://discord.com/api/webhooks/1/token" {
		t.Errorf("notifications = %+v, want enabled with the webhook URL", cfg.Notifications)
	}
}

func webhookConfig(t *testing.T, enabled bool, url string, mode os.FileMode) Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("notifications: {}\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return Config{Path: path, Notifications: Notifications{Enabled: enabled, WebhookURL: url}}
}

func TestWebhookProblemsWarnsWhenURLMissing(t *testing.T) {
	cfg := webhookConfig(t, true, "", 0o600)
	got := cfg.WebhookProblems()
	if len(got) != 1 || !strings.Contains(got[0], "notifications.webhook_url is not set in "+cfg.Path) {
		t.Errorf("problems = %q, want one naming the key and %s", got, cfg.Path)
	}
}

func TestWebhookProblemsQuietWhenDisabled(t *testing.T) {
	if got := webhookConfig(t, false, "", 0o644).WebhookProblems(); len(got) != 0 {
		t.Errorf("problems = %q, want none", got)
	}
}

func TestWebhookProblemsFlagsReadableFile(t *testing.T) {
	cfg := webhookConfig(t, true, "https://discord.com/api/webhooks/1/token", 0o644)
	got := cfg.WebhookProblems()
	if len(got) != 1 || !strings.Contains(got[0], "readable by other users") || !strings.Contains(got[0], "chmod 600 "+cfg.Path) || strings.Contains(got[0], "token") {
		t.Errorf("problems = %q, want one permission warning without the URL", got)
	}
}

func TestWebhookProblemsAcceptsOwnerOnlyFile(t *testing.T) {
	if got := webhookConfig(t, true, "https://discord.com/api/webhooks/1/token", 0o600).WebhookProblems(); len(got) != 0 {
		t.Errorf("problems = %q, want none", got)
	}
}

func autoConfig(t *testing.T, body string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("FOREMAN_CONFIG", path)
	t.Setenv("FOREMAN_TASKS_DIR", "")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load()
}

func TestAutoDefaults(t *testing.T) {
	cfg, err := autoConfig(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auto.Agent != task.Claude || cfg.Auto.MaxConcurrent != 3 {
		t.Fatalf("auto defaults = %+v, want claude and 3", cfg.Auto)
	}
	cfg, err = autoConfig(t, "auto:\n  agent: codex\n  max_concurrent: 5\n")
	if err != nil || cfg.Auto.Agent != task.Codex || cfg.Auto.MaxConcurrent != 5 {
		t.Fatalf("auto from file = %+v, err %v", cfg.Auto, err)
	}
}

func TestAutoRejectsUnknownAgent(t *testing.T) {
	_, err := autoConfig(t, "auto:\n  agent: gemini\n")
	if err == nil || !strings.Contains(err.Error(), "auto.agent") {
		t.Fatalf("an unknown auto agent was accepted: %v", err)
	}
}

func TestAutoRejectsZeroMax(t *testing.T) {
	_, err := autoConfig(t, "auto:\n  max_concurrent: 0\n")
	if err == nil || !strings.Contains(err.Error(), "auto.max_concurrent") {
		t.Fatalf("a zero auto limit was accepted: %v", err)
	}
}

func TestAutoStateDefaultsToOff(t *testing.T) {
	on, err := LoadAutoEnabled(filepath.Join(t.TempDir(), "state.json"))
	if err != nil || on {
		t.Fatalf("a missing state file = %t, %v; want off", on, err)
	}
	path := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(path, []byte("{}"), 0o600)
	if on, err := LoadAutoEnabled(path); err != nil || on {
		t.Fatalf("a state file without auto = %t, %v; want off", on, err)
	}
}

func TestSaveAutoEnabledRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	if err := SaveAutoEnabled(path, false); err != nil {
		t.Fatal(err)
	}
	if on, err := LoadAutoEnabled(path); err != nil || on {
		t.Fatalf("saved off read back as %t, %v", on, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	if err := SaveAutoEnabled(path, true); err != nil {
		t.Fatal(err)
	}
	if on, err := LoadAutoEnabled(path); err != nil || !on {
		t.Fatalf("saved on read back as %t, %v", on, err)
	}
}

func TestCorruptAutoStateIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(path, []byte("{not json"), 0o600)
	if _, err := LoadAutoEnabled(path); err == nil {
		t.Fatal("a corrupt state file loaded without an error")
	}
}

func TestLearningDefaultsToOnWithAKnowledgeDirUnderTheDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FOREMAN_CONFIG", "")
	t.Setenv("FOREMAN_TASKS_DIR", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KnowledgeDir != filepath.Join(home, ".local/share/foreman/knowledge") || !cfg.Learning.Enabled || cfg.Learning.BudgetUSD != 0.5 {
		t.Fatalf("knowledge_dir %q learning %+v", cfg.KnowledgeDir, cfg.Learning)
	}
}

func TestLearningSettingsOverlayDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FOREMAN_CONFIG", "")
	t.Setenv("FOREMAN_TASKS_DIR", "")
	os.MkdirAll(filepath.Join(home, ".config/foreman"), 0o755)
	os.WriteFile(filepath.Join(home, ".config/foreman/config.yaml"), []byte("knowledge_dir: ~/kb\nlearning:\n  enabled: false\n  budget_usd: 0.2\n"), 0o644)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KnowledgeDir != filepath.Join(home, "kb") || cfg.Learning.Enabled || cfg.Learning.BudgetUSD != 0.2 {
		t.Fatalf("knowledge_dir %q learning %+v", cfg.KnowledgeDir, cfg.Learning)
	}
}

func TestLoadRejectsANegativeLearningBudget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FOREMAN_CONFIG", "")
	t.Setenv("FOREMAN_TASKS_DIR", "")
	os.MkdirAll(filepath.Join(home, ".config/foreman"), 0o755)
	os.WriteFile(filepath.Join(home, ".config/foreman/config.yaml"), []byte("learning:\n  budget_usd: -1\n"), 0o644)
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "learning.budget_usd") {
		t.Fatalf("err = %v", err)
	}
}

func TestReviewBeforePRDefaultsToOffAndLoadsFromConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FOREMAN_CONFIG", "")
	t.Setenv("FOREMAN_TASKS_DIR", "")
	cfg, err := Load()
	if err != nil || cfg.ReviewBeforePR {
		t.Fatalf("default review_before_pr %t err %v", cfg.ReviewBeforePR, err)
	}
	os.MkdirAll(filepath.Join(home, ".config/foreman"), 0o755)
	os.WriteFile(filepath.Join(home, ".config/foreman/config.yaml"), []byte("review_before_pr: true\n"), 0o644)
	if cfg, err = Load(); err != nil || !cfg.ReviewBeforePR {
		t.Fatalf("review_before_pr %t err %v", cfg.ReviewBeforePR, err)
	}
}

func TestDefaultSourceIsGitHubWhenUnsetAndLoadsEachValidValue(t *testing.T) {
	cfg, err := autoConfig(t, "")
	if err != nil || cfg.DefaultSource != task.GitHub {
		t.Fatalf("unset default_source = %q, %v; want github", cfg.DefaultSource, err)
	}
	if cfg, err := autoConfig(t, "default_source:\n"); err != nil || cfg.DefaultSource != task.GitHub {
		t.Fatalf("empty default_source = %q, %v; want github", cfg.DefaultSource, err)
	}
	for _, want := range []task.Source{task.GitHub, task.Linear, SourceAll} {
		cfg, err := autoConfig(t, "default_source: "+string(want)+"\n")
		if err != nil || cfg.DefaultSource != want {
			t.Errorf("default_source: %s loaded as %q, %v", want, cfg.DefaultSource, err)
		}
	}
}

func TestDefaultSourceRejectsAnUnknownValueNamingTheKey(t *testing.T) {
	_, err := autoConfig(t, "default_source: jira\n")
	if err == nil || !strings.Contains(err.Error(), "default_source") || !strings.Contains(err.Error(), "jira") {
		t.Fatalf("an unknown default_source gave err = %v; want one naming default_source and jira", err)
	}
}

func TestLoadRejectsUnknownKeysNamingTheKeyAndFile(t *testing.T) {
	for name, tc := range map[string]struct{ body, key string }{
		"top-level misspelling":    {"mentoins: false\n", "mentoins"},
		"nested misspelling":       {"claude:\n  modle: haiku\n", "modle"},
		"retired ignore list":      {"ignore_repositories: [acme/widgets]\n", "ignore_repositories"},
		"auto mode on/off setting": {"auto:\n  enabled: true\n", "enabled"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := autoConfig(t, tc.body)
			if err == nil || !strings.Contains(err.Error(), tc.key) || !strings.Contains(err.Error(), "config.yaml") {
				t.Fatalf("Load(%q) err = %v; want one naming %q and the file", tc.body, err, tc.key)
			}
		})
	}
}

func TestLoadRejectsASecondYAMLDocumentWithContent(t *testing.T) {
	for name, body := range map[string]string{
		"an unknown key":   "mentions: true\n---\nmentoins: false\n",
		"an explicit null": "mentions: true\n---\nnull\n",
		"a tilde null":     "mentions: true\n---\n~\n",
		"a tagged null":    "mentions: true\n---\n!!null ''\n",
		"a bare null tag":  "mentions: true\n---\n!!null\n",
		"an anchored null": "mentions: true\n---\n&a\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := autoConfig(t, body)
			if err == nil || !strings.Contains(err.Error(), "config.yaml") || !strings.Contains(err.Error(), "one YAML document") {
				t.Fatalf("Load(%q) err = %v; want one naming the file and the one-document rule", body, err)
			}
		})
	}
}

func TestLoadAcceptsEmptyFilesAndEmptyExtraDocuments(t *testing.T) {
	for _, body := range []string{"", "\n", "# nothing set yet\n", "---\nrefresh: 60\n", "refresh: 60\n---\n", "refresh: 60\n---\n# note\n"} {
		if cfg, err := autoConfig(t, body); err != nil || cfg.Refresh != DefaultRefresh {
			t.Errorf("Load(%q) = refresh %d, %v; want the defaults", body, cfg.Refresh, err)
		}
	}
}

func TestLoadAcceptsEveryDocumentedKey(t *testing.T) {
	body := `tasks_dir: ~/t
worktrees_dir: ~/w
repositories_dir: ~/r
log_dir: ~/l
knowledge_dir: ~/k
review_rounds: 2
fix_rounds: 2
review_before_pr: true
mentions: false
default_source: all
refresh: 30
budgets:
  implementer_usd: 5
  reviewer_usd: 2
learning:
  enabled: false
  budget_usd: 1
notifications:
  enabled: false
  webhook_url: ""
claude:
  command: claude
  model: opus
  plan_model: sonnet
  review_model: opus
codex:
  command: codex
  model: m
  plan_model: p
  review_model: r
  reasoning_effort: medium
  plan_reasoning_effort: high
  review_reasoning_effort: low
cleanup:
  on_done: false
  delete_merged_branch: false
auto:
  agent: codex
  max_concurrent: 2
include_repositories: [acme/web]
exclude_repositories: [acme/api]
linear_projects:
  acme/web: https://linear.app/acme/project/web-123
`
	cfg, err := autoConfig(t, body)
	if err != nil || cfg.DefaultSource != SourceAll || cfg.Auto.MaxConcurrent != 2 || cfg.Codex.ReviewReasoningEffort != "low" {
		t.Fatalf("documented config = %+v, %v", cfg, err)
	}
}

func TestMaterializedDefaultsLoadUnderStrictKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("FOREMAN_CONFIG", path)
	t.Setenv("FOREMAN_TASKS_DIR", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := Materialize(path, cfg); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(path); !strings.Contains(string(raw), "default_source: github") {
		t.Errorf("materialized config lacks default_source:\n%s", raw)
	}
	if _, err := Load(); err != nil {
		t.Fatalf("the materialized config does not load: %v", err)
	}
}

func TestRunnerBudgetDefaultsOverridesAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("FOREMAN_CONFIG", path)
	cfg, err := Load()
	if err != nil || cfg.Budgets.TaskTokens != 2000000 || cfg.Budgets.PlanningTokens != 400000 || cfg.Budgets.CallTimeoutSeconds != 1800 {
		t.Fatalf("defaults %+v %v", cfg.Budgets, err)
	}
	for _, key := range []string{"task_tokens", "planning_tokens", "call_timeout_seconds", "implementer_usd", "reviewer_usd"} {
		if err := os.WriteFile(path, []byte("budgets:\n  "+key+": -1\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(); err == nil {
			t.Fatalf("accepted negative %s", key)
		}
	}
	if err := os.WriteFile(path, []byte("budgets:\n  task_tokens: 0\n  planning_tokens: 0\n  call_timeout_seconds: 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load()
	if err != nil || cfg.Budgets.TaskTokens != 0 || cfg.Budgets.PlanningTokens != 0 || cfg.Budgets.CallTimeoutSeconds != 0 {
		t.Fatalf("overrides %+v %v", cfg.Budgets, err)
	}
}
