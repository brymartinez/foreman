package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/task"
)

const evaluationJSON = `{
	"assessment": "the plan held up",
	"signals": ["two review rounds"],
	"proposals": [
		{"kind": "convention", "text": "Run make lint before pushing.", "scope": "reusable", "why": "the reviewer flagged lint twice", "source": "observed", "supports": ["L2"], "cites": []},
		{"kind": "fact", "text": "The fixture needs a token.", "scope": "one_off", "why": "only this task", "source": "maintainer", "supports": [], "cites": ["D1"]}
	],
	"revisions": [
		{"id": "L1", "action": "retire", "text": null, "reason": "the maintainer chose otherwise", "source": "maintainer", "cites": ["C2"]}
	]
}`

// compactEvaluation is evaluationJSON on one line, as a stream-json event needs it.
func compactEvaluation(t *testing.T) string {
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(evaluationJSON)); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestParseEvaluationReadsProposalsAndRevisions(t *testing.T) {
	got, err := ParseEvaluation([]byte(evaluationJSON))
	if err != nil {
		t.Fatal(err)
	}
	if got.Assessment != "the plan held up" || !slices.Equal(got.Signals, []string{"two review rounds"}) {
		t.Errorf("assessment and signals = %q %q", got.Assessment, got.Signals)
	}
	want := []knowledge.Proposal{
		{Kind: knowledge.Convention, Text: "Run make lint before pushing.", Scope: knowledge.Reusable, Why: "the reviewer flagged lint twice", Source: knowledge.Observed, Supports: []string{"L2"}},
		{Kind: knowledge.Fact, Text: "The fixture needs a token.", Scope: knowledge.OneOff, Why: "only this task", Source: knowledge.Maintainer, Cites: []string{"D1"}},
	}
	if len(got.Proposals) != 2 {
		t.Fatalf("proposals = %+v", got.Proposals)
	}
	for i, p := range got.Proposals {
		w := want[i]
		if p.Kind != w.Kind || p.Text != w.Text || p.Scope != w.Scope || p.Why != w.Why || p.Source != w.Source || !slices.Equal(p.Supports, w.Supports) || !slices.Equal(p.Cites, w.Cites) {
			t.Errorf("proposal %d = %+v, want %+v", i, p, w)
		}
	}
	if r := got.Revisions; len(r) != 1 || r[0].ID != "L1" || r[0].Action != knowledge.Retire || r[0].Reason != "the maintainer chose otherwise" ||
		r[0].Source != knowledge.Maintainer || !slices.Equal(r[0].Cites, []string{"C2"}) {
		t.Errorf("revisions = %+v", got.Revisions)
	}
}

func TestParseEvaluationAcceptsNoLearnings(t *testing.T) {
	got, err := ParseEvaluation([]byte(`{"assessment":"routine","signals":[],"proposals":[],"revisions":[]}`))
	if err != nil || got.Assessment != "routine" || len(got.Proposals) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestParseEvaluationRejectsMalformedShapes(t *testing.T) {
	proposal := func(field, value string) string {
		p := map[string]string{"kind": `"convention"`, "text": `"x"`, "scope": `"reusable"`, "why": `"y"`, "source": `"observed"`, "supports": `[]`}
		p[field] = value
		var parts []string
		for k, v := range p {
			parts = append(parts, fmt.Sprintf("%q:%s", k, v))
		}
		return `{"assessment":"a","signals":[],"revisions":[],"proposals":[{` + strings.Join(parts, ",") + `}]}`
	}
	revision := func(body string) string {
		return `{"assessment":"a","signals":[],"proposals":[],"revisions":[` + body + `]}`
	}
	for name, raw := range map[string]string{
		"not json":                   `assessment`,
		"missing assessment":         `{"signals":[],"proposals":[],"revisions":[]}`,
		"missing proposals":          `{"assessment":"a","signals":[],"revisions":[]}`,
		"unknown kind":               proposal("kind", `"opinion"`),
		"unknown scope":              proposal("scope", `"forever"`),
		"unknown source":             proposal("source", `"ci"`),
		"empty proposal text":        proposal("text", `" "`),
		"unknown revision action":    revision(`{"id":"L1","action":"delete","text":null,"reason":"r","source":"maintainer"}`),
		"revision without id":        revision(`{"id":"","action":"retire","text":null,"reason":"r","source":"maintainer"}`),
		"revise without text":        revision(`{"id":"L1","action":"revise","text":null,"reason":"r","source":"maintainer"}`),
		"revision of unknown source": revision(`{"id":"L1","action":"retire","text":null,"reason":"r","source":"guess"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseEvaluation([]byte(raw)); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestEvaluatorClaudeRunsWithoutToolsInScratchDirUnderCap(t *testing.T) {
	var got Command
	exec := func(_ context.Context, c Command) error {
		got = c
		fmt.Fprintf(c.Stdout, `{"type":"result","subtype":"success","total_cost_usd":0.12,"structured_output":%s}`, compactEvaluation(t))
		return nil
	}
	e := Evaluators{Claude: Claude{Bin: "claude", Model: "sonnet", PlanModel: "opus", Exec: exec}}
	ev, err := e.Evaluate(context.Background(), EvaluateInput{Agent: task.Claude, Prompt: "judge", MaxBudgetUSD: 0.5})
	if err != nil || ev.Assessment != "the plan held up" || ev.Usage.ClaudeCostUSD != 0.12 {
		t.Fatalf("got %+v, %v", ev, err)
	}
	if argValue(got.Args, "-p") != "judge" || argValue(got.Args, "--model") != "opus" || argValue(got.Args, "--permission-mode") != "dontAsk" ||
		!slices.Contains(got.Args, "--tools") || argValue(got.Args, "--tools") != "" || !slices.Contains(got.Args, "--no-session-persistence") ||
		!slices.Contains(got.Args, "--safe-mode") || !slices.Contains(got.Args, "--restricted") || !slices.Contains(got.Args, "--strict-mcp-config") ||
		argValue(got.Args, "--json-schema") != string(EvaluationSchema) || argValue(got.Args, "--max-budget-usd") != "0.50" {
		t.Errorf("argv = %q", got.Args)
	}
	if got.Dir == "" {
		t.Error("want a scratch directory")
	}
	if _, err := os.Stat(got.Dir); err == nil {
		t.Errorf("scratch directory %s was left behind", got.Dir)
	}
}

func TestEvaluatorUsesTheGivenModelOverThePlanModel(t *testing.T) {
	var got Command
	exec := func(_ context.Context, c Command) error {
		got = c
		fmt.Fprintf(c.Stdout, `{"type":"result","subtype":"success","structured_output":%s}`, compactEvaluation(t))
		return nil
	}
	e := Evaluators{Claude: Claude{Bin: "claude", PlanModel: "opus", Exec: exec}}
	if _, err := e.Evaluate(context.Background(), EvaluateInput{Agent: task.Claude, Model: "haiku", Prompt: "judge"}); err != nil {
		t.Fatal(err)
	}
	if argValue(got.Args, "--model") != "haiku" || slices.Contains(got.Args, "--max-budget-usd") {
		t.Errorf("argv = %q", got.Args)
	}
}

func TestEvaluatorCodexRunsReadOnlyAndEphemeralInScratchDir(t *testing.T) {
	var got Command
	exec := func(_ context.Context, c Command) error {
		got = c
		fmt.Fprintln(c.Stdout, `{"type":"thread.started","thread_id":"th-1"}`)
		fmt.Fprintln(c.Stdout, `{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":4}}`)
		return os.WriteFile(argValue(c.Args, "-o"), []byte(evaluationJSON), 0o600)
	}
	e := Evaluators{Codex: Codex{Bin: "codex", Model: "small", PlanModel: "big", ReasoningEffort: "low", PlanReasoningEffort: "high", Exec: exec}}
	ev, err := e.Evaluate(context.Background(), EvaluateInput{Agent: task.Codex, Prompt: "judge"})
	if err != nil || len(ev.Proposals) != 2 || ev.Usage.CodexInputTokens != 10 || ev.Usage.ClaudeCostUSD != 0 {
		t.Fatalf("got %+v, %v", ev, err)
	}
	if argValue(got.Args, "-s") != "read-only" || !slices.Contains(got.Args, "--ephemeral") || !slices.Contains(got.Args, "--skip-git-repo-check") ||
		argValue(got.Args, "-m") != "big" || !slices.Contains(got.Args, `model_reasoning_effort="high"`) || got.Args[len(got.Args)-1] != "judge" {
		t.Errorf("argv = %q", got.Args)
	}
	if argValue(got.Args, "-C") != got.Dir || got.Dir == "" {
		t.Errorf("dir = %q, -C = %q", got.Dir, argValue(got.Args, "-C"))
	}
}

func TestEvaluatorRejectsInvalidOutput(t *testing.T) {
	exec := func(_ context.Context, c Command) error {
		fmt.Fprintln(c.Stdout, `{"type":"result","subtype":"success","structured_output":{"assessment":"a"}}`)
		return nil
	}
	e := Evaluators{Claude: Claude{Bin: "claude", Exec: exec}}
	if _, err := e.Evaluate(context.Background(), EvaluateInput{Agent: task.Claude, Prompt: "judge"}); err == nil {
		t.Fatal("want an error")
	}
}

func TestEvaluationPromptHoldsTheEvidenceAndLabelsTimeAndCostAsContext(t *testing.T) {
	brief := EvaluationBrief{
		Repo:  "acme/api",
		Title: "Add retries",
		Decisions: []EvidenceDecision{{
			Question: "Which backoff?", Options: []string{"A: fixed", "B: exponential"}, Recommendation: "A", Answer: "B", Overrode: true,
		}},
		Reviews: []EvidenceReview{
			{Round: 1, Findings: []string{"missing test for retry cap"}, HeadAdvanced: true},
			{Round: 2, Clean: true},
		},
		Feedback: []string{"please keep the old flag"},
		Phases:   map[task.Phase]time.Duration{task.PhasePlanning: 10 * time.Minute, task.PhaseReview: 25 * time.Minute},
		Usage:    task.Usage{ClaudeCostUSD: 1.5, CodexInputTokens: 2000, CodexOutputTokens: 300, ToolCalls: map[string]int{"Bash": 7, "Read": 3}},
		Outcome:  "merged",
		Learnings: []knowledge.Learning{
			{ID: "L1", Kind: knowledge.Convention, Text: "Use fixed backoff.", Status: knowledge.Active},
		},
		Earlier: []knowledge.Evaluation{{Task: "12", Assessment: "smooth"}},
	}
	p := EvaluationPrompt(brief)
	for _, want := range []string{
		"acme/api", "Which backoff?", "B: exponential", "overrode the recommendation",
		"missing test for retry cap", "commits followed", "please keep the old flag",
		"planning: 10m", "review: 25m", "$1.50", "2000 input", "Bash: 7", "Read: 3", "merged",
		"L1", "Use fixed backoff.", "smooth",
		"context, not a score", "CI", "one_off", "maintainer", "observed",
		"D1. Which backoff?", "C1: please keep the old flag", "cites",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
}

func TestEvaluatorCodexTurnsOffToolsAndTheUsersConfig(t *testing.T) {
	var got Command
	exec := func(_ context.Context, c Command) error {
		got = c
		fmt.Fprintln(c.Stdout, `{"type":"thread.started","thread_id":"th-1"}`)
		return os.WriteFile(argValue(c.Args, "-o"), []byte(evaluationJSON), 0o600)
	}
	e := Evaluators{Codex: Codex{Bin: "codex", Exec: exec}}
	if _, err := e.Evaluate(context.Background(), EvaluateInput{Agent: task.Codex, Prompt: "judge"}); err != nil {
		t.Fatal(err)
	}
	// The user's config adds MCP servers and plugins; the features are Codex's own tools.
	for _, want := range []string{"--ignore-user-config", "--ignore-rules", `web_search="disabled"`} {
		if !slices.Contains(got.Args, want) {
			t.Errorf("argv lacks %s: %q", want, got.Args)
		}
	}
	for _, feature := range codexEvaluatorOff {
		if !slices.Contains(got.Args, "features."+feature+"=false") {
			t.Errorf("argv leaves %s on: %q", feature, got.Args)
		}
	}
}

func TestEvaluatorDiscardsAnAnswerFromATurnThatUsedTools(t *testing.T) {
	t.Run("codex", func(t *testing.T) {
		exec := func(_ context.Context, c Command) error {
			fmt.Fprintln(c.Stdout, `{"type":"thread.started","thread_id":"th-1"}`)
			fmt.Fprintln(c.Stdout, `{"type":"item.completed","item":{"id":"1","type":"mcp_tool_call","server":"node_repl","tool":"js"}}`)
			return os.WriteFile(argValue(c.Args, "-o"), []byte(evaluationJSON), 0o600)
		}
		e := Evaluators{Codex: Codex{Bin: "codex", Exec: exec}}
		if _, err := e.Evaluate(context.Background(), EvaluateInput{Agent: task.Codex, Prompt: "judge"}); err == nil || !strings.Contains(err.Error(), "tool") {
			t.Fatalf("err = %v, want a tool use error", err)
		}
	})
	t.Run("claude", func(t *testing.T) {
		exec := func(_ context.Context, c Command) error {
			fmt.Fprintln(c.Stdout, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{}}]}}`)
			fmt.Fprintf(c.Stdout, `{"type":"result","subtype":"success","structured_output":%s}`+"\n", compactEvaluation(t))
			return nil
		}
		e := Evaluators{Claude: Claude{Bin: "claude", Exec: exec}}
		if _, err := e.Evaluate(context.Background(), EvaluateInput{Agent: task.Claude, Prompt: "judge"}); err == nil || !strings.Contains(err.Error(), "tool") {
			t.Fatalf("err = %v, want a tool use error", err)
		}
	})
}

func TestEvaluatorClaudeAcceptsSchemaOutputButRejectsOtherTools(t *testing.T) {
	for name, extra := range map[string]string{
		"schema only": "",
		"schema and read": `,{"type":"tool_use","name":"Read","input":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			exec := func(_ context.Context, c Command) error {
			fmt.Fprintf(c.Stdout, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"StructuredOutput","input":%s}%s]}}`+"\n", compactEvaluation(t), extra)
			fmt.Fprintf(c.Stdout, `{"type":"result","subtype":"success","structured_output":%s}`+"\n", compactEvaluation(t))
			return nil
			}
			e := Evaluators{Claude: Claude{Bin: "claude", Exec: exec}}
			_, err := e.Evaluate(context.Background(), EvaluateInput{Agent: task.Claude, Prompt: "judge"})
			if (err != nil) != (extra != "") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
