package agent

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/task"
)

// EvaluationSchema constrains the evaluator's reply. Strict mode, like ReviewSchema:
// a revision's text is null for a retirement.
//
//go:embed evaluation.schema.json
var EvaluationSchema []byte

// Evaluation is one evaluator turn's judgment of a finished task. The knowledge
// store, not the evaluator, decides what becomes active.
type Evaluation struct {
	Assessment string
	Signals    []string
	Proposals  []knowledge.Proposal
	Revisions  []knowledge.Revision
	Usage      task.Usage
}

// ParseEvaluation is the only way evaluator output enters the program. It rejects
// the whole reply on any unknown kind, scope, source, or action.
func ParseEvaluation(raw []byte) (Evaluation, error) {
	var w struct {
		Assessment *string               `json:"assessment"`
		Signals    []string              `json:"signals"`
		Proposals  *[]knowledge.Proposal `json:"proposals"`
		Revisions  []struct {
			ID     string           `json:"id"`
			Action string           `json:"action"`
			Text   *string          `json:"text"`
			Reason string           `json:"reason"`
			Source knowledge.Source `json:"source"`
		} `json:"revisions"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return Evaluation{}, fmt.Errorf("evaluation is not valid JSON: %w", err)
	}
	if w.Assessment == nil || w.Proposals == nil {
		return Evaluation{}, errors.New("evaluation needs an assessment and proposals")
	}
	out := Evaluation{Assessment: strings.TrimSpace(*w.Assessment), Signals: w.Signals}
	for i, p := range *w.Proposals {
		switch {
		case p.Kind != knowledge.Convention && p.Kind != knowledge.Fact && p.Kind != knowledge.Practice:
			return Evaluation{}, fmt.Errorf("proposal %d has unknown kind %q", i+1, p.Kind)
		case p.Scope != knowledge.Reusable && p.Scope != knowledge.OneOff:
			return Evaluation{}, fmt.Errorf("proposal %d has unknown scope %q", i+1, p.Scope)
		case !validSource(p.Source):
			return Evaluation{}, fmt.Errorf("proposal %d has unknown source %q", i+1, p.Source)
		case strings.TrimSpace(p.Text) == "":
			return Evaluation{}, fmt.Errorf("proposal %d has no text", i+1)
		}
		if len(p.Supports) == 0 {
			p.Supports = nil
		}
		out.Proposals = append(out.Proposals, p)
	}
	for i, r := range w.Revisions {
		rev := knowledge.Revision{ID: strings.TrimSpace(r.ID), Action: r.Action, Reason: r.Reason, Source: r.Source}
		if r.Text != nil {
			rev.Text = *r.Text
		}
		switch {
		case rev.ID == "":
			return Evaluation{}, fmt.Errorf("revision %d names no learning", i+1)
		case rev.Action != knowledge.Revise && rev.Action != knowledge.Retire:
			return Evaluation{}, fmt.Errorf("revision %d has unknown action %q", i+1, rev.Action)
		case rev.Action == knowledge.Revise && strings.TrimSpace(rev.Text) == "":
			return Evaluation{}, fmt.Errorf("revision %d revises without text", i+1)
		case !validSource(rev.Source):
			return Evaluation{}, fmt.Errorf("revision %d has unknown source %q", i+1, rev.Source)
		}
		out.Revisions = append(out.Revisions, rev)
	}
	return out, nil
}

func validSource(s knowledge.Source) bool {
	return s == knowledge.Maintainer || s == knowledge.Observed
}

// EvaluateInput is one evaluator call. Empty Model or ReasoningEffort uses the
// adapter's plan setting, then its default.
type EvaluateInput struct {
	Agent           task.Agent
	Model           string
	ReasoningEffort string
	Prompt          string
	MaxBudgetUSD    float64
	Log             io.Writer
}

// codexEvaluatorOff are the Codex features an evaluator turn turns off: the shell,
// code mode, subagents, and the app, browser, and image tools. Codex CLI 0.162 has
// no flag that removes every tool, so Evaluate also rejects a turn that used one.
var codexEvaluatorOff = []string{
	"shell_tool", "unified_exec", "code_mode_host", "multi_agent", "apps", "plugins",
	"memories", "goals", "hooks", "image_generation", "view_image", "browser_use",
	"computer_use", "in_app_browser",
}

// Evaluators runs the evaluator with either agent: one disposable turn with no
// tools, in a scratch directory. It judges the evidence in the prompt alone, so it
// never reads the repository, the logs, or the transcripts.
type Evaluators struct {
	Claude Claude
	Codex  Codex
}

func (e Evaluators) Evaluate(ctx context.Context, in EvaluateInput) (Evaluation, error) {
	dir, err := os.MkdirTemp("", "foreman-evaluate-*")
	if err != nil {
		return Evaluation{}, err
	}
	defer os.RemoveAll(dir)
	start := StartInput{Worktree: dir, Prompt: in.Prompt, Log: in.Log}
	var raw []byte
	var usage task.Usage
	switch in.Agent {
	case task.Claude:
		c := e.Claude
		c.Model = cmp.Or(in.Model, c.PlanModel, c.Model)
		args := []string{
			"-p", in.Prompt,
			"--no-session-persistence",
			"--output-format", "stream-json", "--verbose",
			"--json-schema", string(EvaluationSchema),
			"--permission-mode", "dontAsk",
			"--tools", "",
		}
		if in.MaxBudgetUSD > 0 {
			args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", in.MaxBudgetUSD))
		}
		raw, usage, err = c.exec(ctx, start, args)
	case task.Codex:
		c := e.Codex
		c.Model = cmp.Or(in.Model, c.PlanModel, c.Model)
		c.ReasoningEffort = cmp.Or(in.ReasoningEffort, c.PlanReasoningEffort, c.ReasoningEffort)
		_, raw, usage, err = c.exec(ctx, start, EvaluationSchema, func(schema, last string) []string {
			args := []string{
				"exec", "--json", "--ephemeral", "--skip-git-repo-check",
				"--ignore-user-config", "--ignore-rules",
				"-C", dir,
				"-s", "read-only",
				"-c", `web_search="disabled"`,
				"--output-schema", schema,
				"-o", last,
			}
			for _, f := range codexEvaluatorOff {
				args = append(args, "-c", "features."+f+"=false")
			}
			return args
		}, "", func(task.Session) {})
	default:
		return Evaluation{}, fmt.Errorf("unknown evaluator agent %q", in.Agent)
	}
	if err != nil {
		return Evaluation{Usage: usage}, err
	}
	// Codex has no switch that turns every tool off, so a turn that used one anyway
	// may have read beyond the evidence, and its answer is not trusted.
	if len(usage.ToolCalls) > 0 {
		return Evaluation{Usage: usage}, fmt.Errorf("the evaluator called tools %v; its answer is discarded", usage.ToolCalls)
	}
	out, err := ParseEvaluation(raw)
	out.Usage = usage
	return out, err
}

// EvaluationBrief is the evidence packet. It holds what the task record says and the
// developer's PR comments, and has no field for CI, logs, transcripts, or tool
// arguments, so none can reach the prompt.
type EvaluationBrief struct {
	Repo      task.Repo
	Title     string
	Decisions []EvidenceDecision
	Reviews   []EvidenceReview
	// Feedback holds the developer's PR comments, redacted and capped.
	Feedback []string
	Phases   map[task.Phase]time.Duration
	Usage    task.Usage
	// Outcome is the PR's state: merged, closed, open, or none.
	Outcome string
	// Learnings are the repository's current learnings, which revisions refer to by id.
	Learnings []knowledge.Learning
	// Earlier are other tasks' evaluations in the same repository, newest first.
	Earlier []knowledge.Evaluation
}

type EvidenceDecision struct {
	Question       string
	Options        []string
	Recommendation string
	Answer         string
	Overrode       bool
}

// EvidenceReview is one review round. HeadAdvanced says whether commits followed a
// round that requested changes.
type EvidenceReview struct {
	Round        int
	Clean        bool
	Findings     []string
	HeadAdvanced bool
}

func EvaluationPrompt(b EvaluationBrief) string {
	var s strings.Builder
	fmt.Fprintf(&s, `You evaluate one finished task foreman delegated in the repository %s, so later tasks in this repository go better.

Read the evidence below and answer with the JSON the schema asks for:

- assessment: two or three sentences on how the task went and why.
- signals: short notes on what in the evidence drove the assessment.
- proposals: learnings this task supports. Each has a kind (convention: how this repository wants code written; fact: something true about this repository; practice: how to work on it), short generic text, a scope, why, a source, and the ids of current learnings it supports.
  - scope reusable means the learning helps future tasks in this repository; one_off means it applies to this task only.
  - source maintainer means a recorded decision or a developer's PR comment backs it; observed means you inferred it from the work.
  - Text must be generic: no issue, PR, or commit numbers, no URLs, no file paths outside the repository, no names, no secrets. One sentence.
  - Prefer supporting an existing learning over proposing a near duplicate.
- revisions: changes to current learnings this task contradicts. action revise rewrites the text; retire removes it. Give the learning's id, a reason, and a source. Use null text for retire.

Time and cost are context, not a score. A long or expensive task is not a failure by itself. CI results are not evidence and are left out. Propose nothing when the evidence shows nothing reusable.

## Task

Title: %s
PR outcome: %s
`, b.Repo, b.Title, cmp.Or(b.Outcome, "none"))

	s.WriteString("\n## Decisions\n\n")
	if len(b.Decisions) == 0 {
		s.WriteString("None.\n")
	}
	for i, d := range b.Decisions {
		fmt.Fprintf(&s, "%d. %s\n   Options: %s\n   Recommended: %s. Answer: %s.", i+1, d.Question, strings.Join(d.Options, "; "), d.Recommendation, d.Answer)
		if d.Overrode {
			s.WriteString(" The developer overrode the recommendation.")
		}
		s.WriteString("\n")
	}

	s.WriteString("\n## Review rounds\n\n")
	if len(b.Reviews) == 0 {
		s.WriteString("None.\n")
	}
	for _, r := range b.Reviews {
		if r.Clean {
			fmt.Fprintf(&s, "Round %d: clean.\n", r.Round)
			continue
		}
		followed := "no commits followed"
		if r.HeadAdvanced {
			followed = "commits followed"
		}
		fmt.Fprintf(&s, "Round %d: changes requested, %s.\n", r.Round, followed)
		for _, f := range r.Findings {
			fmt.Fprintf(&s, "- %s\n", f)
		}
	}

	s.WriteString("\n## Developer comments on the PR\n\n")
	if len(b.Feedback) == 0 {
		s.WriteString("None.\n")
	}
	for _, f := range b.Feedback {
		fmt.Fprintf(&s, "- %s\n", f)
	}

	s.WriteString("\n## Time and cost (context, not a score)\n\n")
	for _, p := range task.MeasuredPhases {
		if d, ok := b.Phases[p]; ok {
			fmt.Fprintf(&s, "- %s: %dm\n", p, int(d.Round(time.Minute).Minutes()))
		}
	}
	u := b.Usage
	if u.ClaudeTurns > 0 || u.ClaudeCostUSD > 0 {
		fmt.Fprintf(&s, "- Claude: $%.2f over %d turns\n", u.ClaudeCostUSD, u.ClaudeTurns)
	}
	if u.CodexTurns > 0 || u.CodexInputTokens > 0 {
		fmt.Fprintf(&s, "- Codex: %d input, %d cached, %d output, %d reasoning tokens over %d turns\n",
			u.CodexInputTokens, u.CodexCachedTokens, u.CodexOutputTokens, u.CodexReasoningTokens, u.CodexTurns)
	}
	if len(u.ToolCalls) > 0 {
		var calls []string
		for _, name := range slices.Sorted(maps.Keys(u.ToolCalls)) {
			calls = append(calls, fmt.Sprintf("%s: %d", name, u.ToolCalls[name]))
		}
		fmt.Fprintf(&s, "- Tool calls: %s\n", strings.Join(calls, ", "))
	}

	s.WriteString("\n## Current learnings\n\n")
	if len(b.Learnings) == 0 {
		s.WriteString("None.\n")
	}
	for _, l := range b.Learnings {
		if l.Status == knowledge.Retired {
			continue
		}
		fmt.Fprintf(&s, "- %s (%s, %s, %d tasks): %s\n", l.ID, l.Kind, l.Status, l.Tasks(), l.Text)
	}

	s.WriteString("\n## Earlier evaluations in this repository\n\n")
	if len(b.Earlier) == 0 {
		s.WriteString("None.\n")
	}
	for _, ev := range b.Earlier {
		fmt.Fprintf(&s, "- %s\n", ev.Assessment)
		for _, p := range ev.Proposals {
			if p.Scope == knowledge.Reusable {
				fmt.Fprintf(&s, "  - proposed %s: %s\n", p.Kind, p.Text)
			}
		}
	}
	return s.String()
}
