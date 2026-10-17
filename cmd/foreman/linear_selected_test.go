package main

import (
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/cli"
	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/task"
)

func TestLinearCredentialsNeededOnlyForVisibleLinearProjects(t *testing.T) {
	cfg := config.Config{LinearProjects: map[task.Repo]string{"acme/web": "project"}}
	if !linearSelected(cfg, "") || !linearSelected(cfg, task.Linear) || linearSelected(cfg, task.GitHub) {
		t.Fatal("source selection did not match the mapped repository")
	}
	cfg.IncludedRepos = []task.Repo{"acme/api"}
	if linearSelected(cfg, "") {
		t.Fatal("excluded Linear project required credentials")
	}
	cfg.IncludedRepos = nil
	cfg.ExcludedRepos = []task.Repo{"acme/web"}
	if linearSelected(cfg, "") {
		t.Fatal("excluded Linear project required credentials")
	}
}

func TestResolveSourcePrefersTheFlagThenConfig(t *testing.T) {
	for name, tc := range map[string]struct {
		flag, config, want task.Source
	}{
		"built-in default shows GitHub":       {"", task.GitHub, task.GitHub},
		"config linear shows Linear":          {"", task.Linear, task.Linear},
		"config all shows both trackers":      {"", config.SourceAll, ""},
		"flag github overrides config all":    {task.GitHub, config.SourceAll, task.GitHub},
		"flag all overrides config github":    {config.SourceAll, task.GitHub, ""},
		"flag linear overrides config github": {task.Linear, task.GitHub, task.Linear},
	} {
		t.Run(name, func(t *testing.T) {
			got := resolveSource(config.Config{DefaultSource: tc.config}, cli.Command{Source: tc.flag})
			if got != tc.want {
				t.Fatalf("resolveSource(flag %q, config %q) = %q; want %q", tc.flag, tc.config, got, tc.want)
			}
		})
	}
}

func TestResolveMentionsPrefersTheFlagThenConfig(t *testing.T) {
	for name, tc := range map[string]struct {
		flag   cli.MentionsMode
		config bool
		want   bool
	}{
		"no flag uses config on":            {cli.MentionsConfig, true, true},
		"no flag uses config off":           {cli.MentionsConfig, false, false},
		"--mentions overrides config off":   {cli.MentionsOn, false, true},
		"--no-mentions overrides config on": {cli.MentionsOff, true, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := resolveMentions(config.Config{Mentions: tc.config}, cli.Command{Mentions: tc.flag}); got != tc.want {
				t.Fatalf("resolveMentions(flag %q, config %t) = %t; want %t", tc.flag, tc.config, got, tc.want)
			}
		})
	}
}

func TestMissingLinearKeyStopsTheDashboardOnlyWhenTheSourceShowsAVisibleLinearRepo(t *testing.T) {
	mapped := map[task.Repo]string{"acme/web": "https://linear.app/acme/project/web"}
	for name, tc := range map[string]struct {
		cfg     config.Config
		cmd     cli.Command
		wantErr bool
	}{
		"default_source all needs the key":    {config.Config{DefaultSource: config.SourceAll, LinearProjects: mapped}, cli.Command{}, true},
		"-s all needs the key":                {config.Config{DefaultSource: task.GitHub, LinearProjects: mapped}, cli.Command{Source: config.SourceAll, Auto: cli.AutoOn}, true},
		"default_source linear needs the key": {config.Config{DefaultSource: task.Linear, LinearProjects: mapped}, cli.Command{}, true},
		"-s github skips the key":             {config.Config{DefaultSource: config.SourceAll, LinearProjects: mapped}, cli.Command{Source: task.GitHub}, false},
		"built-in github skips the key":       {config.Config{DefaultSource: task.GitHub, LinearProjects: mapped}, cli.Command{}, false},
		"all with the Linear repo excluded":   {config.Config{DefaultSource: config.SourceAll, LinearProjects: mapped, ExcludedRepos: []task.Repo{"acme/web"}}, cli.Command{}, false},
		"all with no Linear mapping":          {config.Config{DefaultSource: config.SourceAll}, cli.Command{}, false},
	} {
		t.Run(name, func(t *testing.T) {
			err := requireLinearKey(tc.cfg, resolveSource(tc.cfg, tc.cmd), "")
			if (err != nil) != tc.wantErr {
				t.Fatalf("requireLinearKey err = %v; want error %t", err, tc.wantErr)
			}
			if err != nil && (!strings.Contains(err.Error(), "LINEAR_API_KEY") || !strings.Contains(err.Error(), "-s github") || !strings.Contains(err.Error(), "default_source")) {
				t.Fatalf("error %q does not name the key and both ways out", err)
			}
			if err := requireLinearKey(tc.cfg, resolveSource(tc.cfg, tc.cmd), "lin_key"); err != nil {
				t.Fatalf("with a key set: %v", err)
			}
		})
	}
}
