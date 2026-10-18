package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// instructionFiles are the repository instruction files the implementer reads at the
// worktree root.
var instructionFiles = []string{"AGENTS.md", "CLAUDE.md"}

// diagnoseLimit traces a self-review loop stalled at the fix limit to its cause, with
// the implementer's agent in one tool-less turn. The diagnosis only shapes the
// question, so a failure yields none and the developer is still asked.
func (a App) diagnoseLimit(ctx context.Context, rec *task.Record, title, body string, findings []task.Finding, log io.Writer) (task.Diagnosis, task.Usage) {
	unavailable := func(err error) task.Diagnosis {
		fmt.Fprintf(log, "foreman: limit diagnosis unavailable: %v\n", err)
		return task.Diagnosis{Kind: task.DiagnosedNone, Summary: "Diagnosis unavailable: " + err.Error()}
	}
	if a.Diagnoser == nil {
		return unavailable(errors.New("no diagnoser is configured")), task.Usage{}
	}
	sources, err := a.limitContext(ctx, rec, title, body, log)
	if err != nil {
		return unavailable(err), task.Usage{}
	}
	in := agent.EvaluateInput{Agent: rec.Delegation.Implementer.Agent, Prompt: agent.DiagnosisPrompt(findings, sources), MaxBudgetUSD: a.ReviewerBudgetUSD, Log: log}
	fmt.Fprintf(log, "foreman: %s diagnosing the self-review at the fix limit\n", in.Agent)
	out, err := a.Diagnoser.Diagnose(ctx, in, sources)
	if err != nil {
		return unavailable(err), out.Usage
	}
	return out.Diagnosis, out.Usage
}

// limitContext is the context the implementer was given, as named sources a quoted
// statement must come from: the issue, its planning documents, the developer's context
// file, the answered decisions, the PR or proposed description, the repository
// instruction files, the implement stage instructions, and the repository knowledge in
// its prompt.
func (a App) limitContext(ctx context.Context, rec *task.Record, title, body string, log io.Writer) ([]agent.DiagnosisSource, error) {
	d := rec.Delegation
	issue, err := a.issue(ctx, rec.Key)
	if err != nil {
		return nil, err
	}
	sources := []agent.DiagnosisSource{{Name: "issue", Text: issue.Title + "\n\n" + issue.Body}}
	plan, err := a.readIssuePlan(ctx, rec)
	if err != nil {
		return nil, err
	}
	for _, doc := range task.PlanDocs {
		if f, ok := plan.found[doc]; ok {
			sources = append(sources, agent.DiagnosisSource{Name: "plan: " + doc.Title(), Text: f.text})
		}
	}
	if d.ContextFile != "" {
		text, err := os.ReadFile(d.ContextFile)
		if err != nil {
			return nil, fmt.Errorf("context file: %w", err)
		}
		sources = append(sources, agent.DiagnosisSource{Name: "context file", Text: string(text)})
	}
	n := 0
	for _, dec := range rec.Decisions {
		if dec.Answer == "" {
			continue
		}
		n++
		sources = append(sources, agent.DiagnosisSource{Name: fmt.Sprintf("decision %d", n), Text: dec.Question.Issue + "\n\n" + dec.Answer})
	}
	name := "pull request description"
	if rec.LocalReview() {
		name = "proposed pull request description"
	}
	sources = append(sources, agent.DiagnosisSource{Name: name, Text: title + "\n\n" + body})
	for _, f := range instructionFiles {
		text, err := os.ReadFile(filepath.Join(d.Worktree, f))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		sources = append(sources, agent.DiagnosisSource{Name: f, Text: string(text)})
	}
	stage, err := a.Stages.Resolve(d.Worktree, task.StageImplement)
	if err != nil {
		return nil, err
	}
	if text := stage.Content(); text != "" {
		sources = append(sources, agent.DiagnosisSource{Name: "implement stage instructions", Text: text})
	}
	if k := a.repoKnowledge(rec.Repo, issue, log); strings.TrimSpace(k) != "" {
		sources = append(sources, agent.DiagnosisSource{Name: "repository knowledge", Text: k})
	}
	return sources, nil
}
