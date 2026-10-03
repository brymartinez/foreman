package routing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "routing.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const valid = `
profiles:
  claude: {agent: claude}
  codex: {agent: codex}
rules:
  - when: big work
    profiles: [claude]
default: claude
`

func TestLoadMissingFileReturnsNil(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "routing.yaml"))
	if c != nil || err != nil {
		t.Fatalf("got %+v, %v; want nil, nil", c, err)
	}
}

func TestLoadStarterFile(t *testing.T) {
	c, err := Load("../../defaults/routing.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Rules) != 3 || c.Default != "claude" {
		t.Fatalf("rules %d, default %q", len(c.Rules), c.Default)
	}
	codex := c.Profiles["codex"]
	if codex.Agent != task.Codex || codex.PlanModel != "gpt-6-luna" || codex.PlanReasoningEffort != "xhigh" || codex.Model != "gpt-6-sol" || codex.ReasoningEffort != "medium" {
		t.Errorf("codex profile: %+v", codex)
	}
	claude := c.Profiles["claude"]
	if claude.Agent != task.Claude || claude.PlanModel != "sonnet" || claude.Model != "opus" {
		t.Errorf("claude profile: %+v", claude)
	}
	if got := c.Rules[2].Profiles; len(got) != 2 || got[0] != "claude" || got[1] != "codex" {
		t.Errorf("other work offers %v, want both with claude first", got)
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	path := write(t, valid+"defualt: codex\n")
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "defualt") {
		t.Fatalf("got %v, want an error naming the file and the key", err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"no profiles", "rules: [{when: x, profiles: [a]}]\ndefault: a\n", "profiles"},
		{"unknown default", strings.Replace(valid, "default: claude", "default: gpt", 1), `default: unknown profile "gpt"`},
		{"missing default", strings.Replace(valid, "default: claude", "", 1), "default"},
		{"rule with unknown profile", strings.Replace(valid, "profiles: [claude]", "profiles: [gpt]", 1), `rules[0].profiles: unknown profile "gpt"`},
		{"rule with no profiles", strings.Replace(valid, "profiles: [claude]", "profiles: []", 1), "rules[0].profiles"},
		{"empty when", strings.Replace(valid, "when: big work", "when: ' '", 1), "rules[0].when"},
		{"bad agent", strings.Replace(valid, "{agent: codex}", "{agent: gemini}", 1), "profiles.codex.agent"},
		{"claude effort", strings.Replace(valid, "{agent: claude}", "{agent: claude, reasoning_effort: high}", 1), "profiles.claude"},
		{"claude selector effort", valid + "selector: {agent: claude, reasoning_effort: high}\n", "selector"},
		{"bad selector agent", valid + "selector: {agent: gemini}\n", "selector.agent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(write(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

var defaults = Defaults{
	task.Claude: {Model: "opus", PlanModel: "sonnet"},
	task.Codex:  {Model: "gpt-6-sol", PlanModel: "gpt-6-luna", ReasoningEffort: "medium", PlanReasoningEffort: "xhigh"},
}

func TestResolveInheritsOmittedFields(t *testing.T) {
	c := Config{Profiles: map[string]Profile{"c": {Agent: task.Claude}, "x": {Agent: task.Codex}}}
	if got, want := c.Resolve("c", defaults), (task.Profile{Name: "c", Agent: task.Claude, Model: "opus", PlanModel: "sonnet"}); got != want {
		t.Errorf("claude: got %+v, want %+v", got, want)
	}
	want := task.Profile{Name: "x", Agent: task.Codex, Model: "gpt-6-sol", PlanModel: "gpt-6-luna", ReasoningEffort: "medium", PlanReasoningEffort: "xhigh"}
	if got := c.Resolve("x", defaults); got != want {
		t.Errorf("codex: got %+v, want %+v", got, want)
	}
}

func TestResolvePlanFallsBackToModelWhenNoPlanModelIsSet(t *testing.T) {
	c := Config{Profiles: map[string]Profile{"x": {Agent: task.Codex, Model: "m", ReasoningEffort: "low"}}}
	got := c.Resolve("x", Defaults{})
	if got.PlanModel != "m" || got.PlanReasoningEffort != "low" {
		t.Errorf("got %+v, want planning to use the implementation model and effort", got)
	}
}

func TestResolveKeepsProfileOverrides(t *testing.T) {
	c := Config{Profiles: map[string]Profile{"x": {Agent: task.Codex, Model: "m", PlanModel: "p", ReasoningEffort: "low", PlanReasoningEffort: "high"}}}
	want := task.Profile{Name: "x", Agent: task.Codex, Model: "m", PlanModel: "p", ReasoningEffort: "low", PlanReasoningEffort: "high"}
	if got := c.Resolve("x", defaults); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestSelectorDefaultsToClaudePlanModel(t *testing.T) {
	var c Config
	if got := c.SelectorFor(defaults); got.Agent != task.Claude || got.Model != "sonnet" {
		t.Errorf("got %+v, want claude with the plan model", got)
	}
	if got := c.SelectorFor(Defaults{task.Claude: {Model: "opus"}}); got.Model != "opus" {
		t.Errorf("got %+v, want claude.model when no plan model is set", got)
	}
	c.Selector = Selector{Agent: task.Codex, ReasoningEffort: "low"}
	if got := c.SelectorFor(defaults); got.Agent != task.Codex || got.Model != "gpt-6-luna" || got.ReasoningEffort != "low" {
		t.Errorf("codex selector: got %+v", got)
	}
}

func TestPathSitsNextToConfig(t *testing.T) {
	if got := Path("/x/config.yaml"); got != "/x/routing.yaml" {
		t.Errorf("got %q", got)
	}
}
