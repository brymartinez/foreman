// Package routing reads routing.yaml: the profiles a new dashboard task can start
// with, the rules that say which profiles suit which work, and the default.
package routing

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/task"
)

type Config struct {
	Selector Selector           `yaml:"selector"`
	Profiles map[string]Profile `yaml:"profiles"`
	Rules    []Rule             `yaml:"rules"`
	Default  string             `yaml:"default"`
}

// Selector is the agent that reads the issue and picks a rule. Empty fields take
// claude.plan_model (else claude.model), or the Codex equivalents.
type Selector struct {
	Agent           task.Agent `yaml:"agent"`
	Model           string     `yaml:"model"`
	ReasoningEffort string     `yaml:"reasoning_effort"`
}

// Profile is one way to run a task. Omitted fields inherit config.yaml.
type Profile struct {
	Agent               task.Agent `yaml:"agent"`
	PlanModel           string     `yaml:"plan_model"`
	Model               string     `yaml:"model"`
	PlanReasoningEffort string     `yaml:"plan_reasoning_effort"`
	ReasoningEffort     string     `yaml:"reasoning_effort"`
}

// Rule names the profiles that suit work described by When, cheapest first.
type Rule struct {
	When     string   `yaml:"when"`
	Profiles []string `yaml:"profiles"`
	// Why is the short reason shown when the rule is chosen; "" shows When.
	Why string `yaml:"why"`
}

// Defaults are the agent settings in config.yaml that a profile inherits.
type Defaults struct {
	Claude config.Claude
	Codex  config.Codex
}

// Path is routing.yaml next to the config file, so FOREMAN_CONFIG moves both.
func Path(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "routing.yaml")
}

// Load reads and validates path. A missing file returns nil and no error: routing
// is off.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// Validate reports the first problem, naming its key.
func (c Config) Validate() error {
	if c.Selector.Agent != "" {
		if _, err := task.ParseAgent(string(c.Selector.Agent)); err != nil {
			return fmt.Errorf("selector.agent: %w", err)
		}
	}
	if c.Selector.Agent != task.Codex && c.Selector.ReasoningEffort != "" {
		return errors.New("selector: reasoning_effort applies only to codex")
	}
	if len(c.Profiles) == 0 {
		return errors.New("profiles: none defined")
	}
	for _, name := range slices.Sorted(maps.Keys(c.Profiles)) {
		p := c.Profiles[name]
		if _, err := task.ParseAgent(string(p.Agent)); err != nil {
			return fmt.Errorf("profiles.%s.agent: %w", name, err)
		}
		// Foreman passes no effort setting to Claude.
		if p.Agent == task.Claude && (p.ReasoningEffort != "" || p.PlanReasoningEffort != "") {
			return fmt.Errorf("profiles.%s: reasoning effort applies only to codex", name)
		}
	}
	if c.Default == "" {
		return errors.New("default: no profile named")
	}
	if _, ok := c.Profiles[c.Default]; !ok {
		return fmt.Errorf("default: unknown profile %q", c.Default)
	}
	for i, r := range c.Rules {
		if strings.TrimSpace(r.When) == "" {
			return fmt.Errorf("rules[%d].when: empty", i)
		}
		if len(r.Profiles) == 0 {
			return fmt.Errorf("rules[%d].profiles: none listed", i)
		}
		for _, name := range r.Profiles {
			if _, ok := c.Profiles[name]; !ok {
				return fmt.Errorf("rules[%d].profiles: unknown profile %q", i, name)
			}
		}
	}
	return nil
}

// Resolve fills a profile's omitted fields from config.yaml. Planning falls back to
// the implementation model and effort, as the adapters do without a profile.
func (c Config) Resolve(name string, d Defaults) task.Profile {
	p := c.Profiles[name]
	out := task.Profile{Name: name, Agent: p.Agent, Model: p.Model, PlanModel: p.PlanModel}
	if p.Agent == task.Codex {
		out.Model = or(out.Model, d.Codex.Model)
		out.PlanModel = or(out.PlanModel, d.Codex.PlanModel, out.Model)
		out.ReasoningEffort = or(p.ReasoningEffort, d.Codex.ReasoningEffort)
		out.PlanReasoningEffort = or(p.PlanReasoningEffort, d.Codex.PlanReasoningEffort, out.ReasoningEffort)
		return out
	}
	out.Model = or(out.Model, d.Claude.Model)
	out.PlanModel = or(out.PlanModel, d.Claude.PlanModel, out.Model)
	return out
}

// SelectorFor resolves the selector's agent, model, and effort.
func (c Config) SelectorFor(d Defaults) Selector {
	s := c.Selector
	if s.Agent == "" {
		s.Agent = task.Claude
	}
	if s.Agent == task.Codex {
		s.Model = or(s.Model, d.Codex.PlanModel, d.Codex.Model)
		s.ReasoningEffort = or(s.ReasoningEffort, d.Codex.PlanReasoningEffort, d.Codex.ReasoningEffort)
		return s
	}
	s.Model = or(s.Model, d.Claude.PlanModel, d.Claude.Model)
	return s
}

// Reason is the rule's short reason: Why, else When.
func (r Rule) Reason() string {
	if r.Why != "" {
		return r.Why
	}
	return r.When
}

func or(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
