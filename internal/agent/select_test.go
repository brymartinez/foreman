package agent

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func TestParseSelection(t *testing.T) {
	got, err := ParseSelection([]byte(`{"rule":2,"reason":"a typo fix"}`))
	if err != nil || got != (Selection{Rule: 2, Reason: "a typo fix"}) {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got, err := ParseSelection([]byte(`{"rule":0,"reason":"unsure"}`)); err != nil || got.Rule != 0 {
		t.Fatalf("rule 0: got %+v, %v", got, err)
	}
}

func TestParseSelectionRejects(t *testing.T) {
	for name, raw := range map[string]string{
		"negative rule":  `{"rule":-1,"reason":"x"}`,
		"missing rule":   `{"reason":"x"}`,
		"missing reason": `{"rule":1}`,
		"not json":       `rule 1`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSelection([]byte(raw)); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

const selectionJSON = `{"rule":1,"reason":"broad refactor"}`

func TestSelectorClaudeArgs(t *testing.T) {
	var got Command
	exec := func(_ context.Context, c Command) error {
		got = c
		fmt.Fprintf(c.Stdout, `{"type":"result","subtype":"success","structured_output":%s}`, selectionJSON)
		return nil
	}
	s := Selectors{Claude: Claude{Bin: "claude", Model: "opus", Exec: exec}}
	sel, err := s.Select(context.Background(), SelectInput{Agent: task.Claude, Model: "sonnet", Prompt: "pick"})
	if err != nil || sel != (Selection{Rule: 1, Reason: "broad refactor"}) {
		t.Fatalf("got %+v, %v", sel, err)
	}
	if argValue(got.Args, "-p") != "pick" || argValue(got.Args, "--model") != "sonnet" || argValue(got.Args, "--permission-mode") != "dontAsk" ||
		argValue(got.Args, "--tools") != "" || !slices.Contains(got.Args, "--tools") || !slices.Contains(got.Args, "--no-session-persistence") ||
		argValue(got.Args, "--json-schema") != string(SelectionSchema) {
		t.Errorf("argv = %q", got.Args)
	}
	if got.Dir == "" || strings.Contains(got.Dir, "worktree") {
		t.Errorf("dir = %q, want a scratch directory", got.Dir)
	}
	if _, err := os.Stat(got.Dir); err == nil {
		t.Errorf("scratch directory %s was left behind", got.Dir)
	}
}

func TestSelectorCodexArgs(t *testing.T) {
	var got Command
	exec := func(_ context.Context, c Command) error {
		got = c
		fmt.Fprintln(c.Stdout, `{"type":"thread.started","thread_id":"th-1"}`)
		return os.WriteFile(argValue(c.Args, "-o"), []byte(selectionJSON), 0o600)
	}
	s := Selectors{Codex: Codex{Bin: "codex", Model: "big", ReasoningEffort: "high", Exec: exec}}
	sel, err := s.Select(context.Background(), SelectInput{Agent: task.Codex, Model: "gpt-6-luna", ReasoningEffort: "low", Prompt: "pick"})
	if err != nil || sel.Rule != 1 {
		t.Fatalf("got %+v, %v", sel, err)
	}
	if argValue(got.Args, "-s") != "read-only" || !slices.Contains(got.Args, "--ephemeral") || !slices.Contains(got.Args, "--skip-git-repo-check") ||
		argValue(got.Args, "-m") != "gpt-6-luna" || !slices.Contains(got.Args, `model_reasoning_effort="low"`) || got.Args[len(got.Args)-1] != "pick" ||
		argValue(got.Args, "--output-schema") == "" {
		t.Errorf("argv = %q", got.Args)
	}
	if argValue(got.Args, "-C") != got.Dir || got.Dir == "" {
		t.Errorf("dir = %q, -C = %q", got.Dir, argValue(got.Args, "-C"))
	}
}
