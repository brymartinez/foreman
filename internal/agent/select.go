package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

// SelectionSchema constrains the routing selector's reply: the number of the rule
// that fits the issue, or 0 when none fits or the selector is unsure.
//
//go:embed selection.schema.json
var SelectionSchema []byte

type Selection struct {
	Rule   int
	Reason string
}

// SelectInput is one selector call. Empty Model or ReasoningEffort leaves the
// adapter's own setting.
type SelectInput struct {
	Agent           task.Agent
	Model           string
	ReasoningEffort string
	Prompt          string
}

func ParseSelection(raw []byte) (Selection, error) {
	var w struct {
		Rule   *int    `json:"rule"`
		Reason *string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return Selection{}, fmt.Errorf("selection is not valid JSON: %w", err)
	}
	if w.Rule == nil || w.Reason == nil {
		return Selection{}, errors.New("selection needs a rule and a reason")
	}
	if *w.Rule < 0 {
		return Selection{}, fmt.Errorf("selection has a negative rule %d", *w.Rule)
	}
	return Selection{Rule: *w.Rule, Reason: strings.TrimSpace(*w.Reason)}, nil
}

// Selectors runs the routing selector with either agent: one disposable turn with
// no tools, in a scratch directory, so the repository's instructions and files play
// no part.
type Selectors struct {
	Claude Claude
	Codex  Codex
}

func (s Selectors) Select(ctx context.Context, in SelectInput) (Selection, error) {
	dir, err := os.MkdirTemp("", "foreman-select-*")
	if err != nil {
		return Selection{}, err
	}
	defer os.RemoveAll(dir)
	start := StartInput{Worktree: dir, Prompt: in.Prompt}
	var raw []byte
	switch in.Agent {
	case task.Claude:
		c := s.Claude
		if in.Model != "" {
			c.Model = in.Model
		}
		raw, _, err = c.exec(ctx, start, []string{
			"-p", in.Prompt,
			"--no-session-persistence",
			"--output-format", "stream-json", "--verbose",
			"--json-schema", string(SelectionSchema),
			"--permission-mode", "dontAsk",
			"--tools", "",
		})
	case task.Codex:
		c := s.Codex
		if in.Model != "" {
			c.Model = in.Model
		}
		if in.ReasoningEffort != "" {
			c.ReasoningEffort = in.ReasoningEffort
		}
		_, raw, _, err = c.exec(ctx, start, SelectionSchema, func(schema, last string) []string {
			return []string{
				"exec", "--json", "--ephemeral", "--skip-git-repo-check",
				"-C", dir,
				"-s", "read-only",
				"--output-schema", schema,
				"-o", last,
			}
		}, "", func(task.Session) {})
	default:
		return Selection{}, fmt.Errorf("unknown selector agent %q", in.Agent)
	}
	if err != nil {
		return Selection{}, err
	}
	return ParseSelection(raw)
}
