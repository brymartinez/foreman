package render

import (
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/task"
)

func TestConfigShowsResolvedValues(t *testing.T) {
	var out strings.Builder
	Config(&out, config.Config{
		Path: "/tmp/foreman.yaml", TasksDir: "/vault/tasks", WorktreesDir: "/tmp/worktrees", RepositoriesDir: "/tmp/repositories", LogDir: "/tmp/logs",
		StagesDir: "/tmp/stages", IgnoredRepos: []task.Repo{"acme/widgets"}, Claude: config.Claude{Command: "claude"}, Codex: config.Codex{Command: "codex"},
		Cleanup: config.Cleanup{OnDone: true, DeleteMergedBranch: false}, Mentions: true, Refresh: config.DefaultRefresh,
	})
	for _, want := range []string{"Config file: /tmp/foreman.yaml", "Ignore repositories: acme/widgets", "Review rounds: 3 passes per review", "Claude plan model: CLI default", "Claude implement model: CLI default", "Claude review model: CLI default", "Codex plan reasoning effort: CLI default", "Codex implement reasoning effort: CLI default", "Codex review reasoning effort: CLI default", "per Claude turn (Codex has no cap)", "Fix rounds: 3 before asking you", "Delete merged branch: false", "Mentions: true", "GitHub refresh: every 60 seconds"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}

func TestConfigShowsStageModelsAndEffort(t *testing.T) {
	var out strings.Builder
	Config(&out, config.Config{
		Claude: config.Claude{Model: "opus", PlanModel: "sonnet", ReviewModel: "opus"},
		Codex:  config.Codex{Model: "gpt-6-sol", PlanModel: "gpt-6-luna", ReviewModel: "gpt-6-sol", ReasoningEffort: "medium", PlanReasoningEffort: "xhigh", ReviewReasoningEffort: "medium"},
	})
	for _, want := range []string{
		"Claude plan model: sonnet", "Claude implement model: opus", "Claude review model: opus",
		"Codex plan model: gpt-6-luna", "Codex plan reasoning effort: xhigh",
		"Codex implement model: gpt-6-sol", "Codex implement reasoning effort: medium",
		"Codex review model: gpt-6-sol", "Codex review reasoning effort: medium",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}

func TestConfigHidesWebhookURL(t *testing.T) {
	var out strings.Builder
	Config(&out, config.Config{Notifications: config.Notifications{Enabled: true, WebhookURL: "https://discord.com/api/webhooks/1/secret-token"}})
	if strings.Contains(out.String(), "secret-token") || !strings.Contains(out.String(), "Notification webhook: set\n") {
		t.Errorf("config output shows the URL or lacks the webhook status:\n%s", out.String())
	}
}

func TestConfigShowsMissingWebhookWarning(t *testing.T) {
	var out strings.Builder
	Config(&out, config.Config{Path: "/tmp/foreman.yaml", Notifications: config.Notifications{Enabled: true}})
	if !strings.Contains(out.String(), "Notification webhook: not set\n! notifications are enabled but notifications.webhook_url is not set in /tmp/foreman.yaml") {
		t.Errorf("config output lacks the setup warning:\n%s", out.String())
	}
}

func TestConfigShowPrintsAutoStart(t *testing.T) {
	var out strings.Builder
	Config(&out, config.Config{Auto: config.Auto{Agent: task.Codex, MaxConcurrent: 5}})
	if want := "Auto start: codex, up to 5 runs"; !strings.Contains(out.String(), want) {
		t.Errorf("missing %q in:\n%s", want, out.String())
	}
}
