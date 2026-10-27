// Package defaults holds the stage files that `make config` installs. These tests lock
// the parts of the default text that foreman's behavior depends on.
package defaults

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/config"
)

func TestShippingConfigLoadsWithoutPersonalSettings(t *testing.T) {
	path, err := filepath.Abs("config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FOREMAN_CONFIG", path)
	t.Setenv("FOREMAN_TASKS_DIR", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultSource != "github" {
		t.Errorf("shipping default_source = %q, want github", cfg.DefaultSource)
	}
	if cfg.Mentions || cfg.Budgets.ImplementerUSD != 25 || cfg.Budgets.ReviewerUSD != 10 {
		t.Errorf("shipping choices were not loaded: mentions=%t budgets=%+v", cfg.Mentions, cfg.Budgets)
	}
	if cfg.Claude.Model != "opus" || cfg.Claude.PlanModel != "sonnet" || cfg.Codex.PlanModel != "gpt-6-luna" {
		t.Errorf("shipping models were not loaded: claude=%+v codex=%+v", cfg.Claude, cfg.Codex)
	}
	if cfg.Notifications.WebhookURL != "" || len(cfg.IncludedRepos) != 0 || len(cfg.ExcludedRepos) != 0 || len(cfg.LinearProjects) != 0 {
		t.Error("shipping config contains personal settings")
	}
}

func readStage(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("stages/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDefaultPlanStageKeepsFourDocumentsAndOmitsLineLevelDetail(t *testing.T) {
	plan := readStage(t, "plan.md")
	for _, want := range []string{
		"## Analysis", "## PRD", "## Design", "## Implementation plan",
		"The implementation plan must stand alone",
		"No line numbers",
		"no commit messages",
		"Do not list every test by name",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan.md lacks %q", want)
		}
	}
	for _, banned := range []string{"path:lines", "Name the functions, types, and test cases", "commit boundary"} {
		if strings.Contains(plan, banned) {
			t.Errorf("plan.md still asks for line-level detail: %q", banned)
		}
	}
}

func TestDefaultPRStageHasProblemFixRiskAndNoHowToTest(t *testing.T) {
	pr := readStage(t, "pr.md")
	for _, want := range []string{"`## Problem`", "`## Fix`", "`## Risk`", "`## Review notes`", "`Closes #N`", "Leave out test commands, test results, reviewer test steps"} {
		if !strings.Contains(pr, want) {
			t.Errorf("pr.md lacks %q", want)
		}
	}
	if strings.Contains(pr, "How to test") {
		t.Error("pr.md asks for a How to test section")
	}
}
