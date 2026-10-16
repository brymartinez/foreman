package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/cli"
	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/knowledge"
)

// runKnowledge runs `foreman knowledge export` or `ingest`. Neither needs the stage
// files, the dashboard, or a task.
func runKnowledge(ctx context.Context, cfg config.Config, cmd cli.Command, stdout, stderr io.Writer) int {
	claude := agent.Claude{Bin: cfg.Claude.Command, Model: cfg.Claude.Model, PlanModel: cfg.Claude.PlanModel, Exec: agent.RunCommand}
	codex := agent.Codex{Bin: cfg.Codex.Command, Model: cfg.Codex.Model, PlanModel: cfg.Codex.PlanModel,
		ReasoningEffort: cfg.Codex.ReasoningEffort, PlanReasoningEffort: cfg.Codex.PlanReasoningEffort, Exec: agent.RunCommand}
	a := app.App{
		Knowledge:         knowledge.Store{Dir: cfg.KnowledgeDir},
		Merger:            agent.Mergers{Claude: claude, Codex: codex},
		LearningBudgetUSD: cfg.Learning.BudgetUSD,
		Now:               func() time.Time { return time.Now().Truncate(time.Second) },
	}
	failed := func(err error) int {
		fmt.Fprintf(stderr, "foreman: %v\n", err)
		return 1
	}
	switch cmd.Name {
	case cli.KnowledgeExport:
		data, err := a.ExportKnowledge(string(cmd.Repo))
		if err != nil {
			return failed(err)
		}
		if cmd.File == "" {
			_, err = stdout.Write(data)
		} else {
			err = os.WriteFile(cmd.File, data, 0o600)
		}
		if err != nil {
			return failed(err)
		}
		if cmd.File != "" {
			fmt.Fprintf(stdout, "Exported %s's knowledge to %s.\n", cmd.Repo, cmd.File)
		}
	case cli.KnowledgeIngest:
		data, err := os.ReadFile(cmd.File)
		if err != nil {
			return failed(err)
		}
		in := app.IngestInput{Data: data, Repo: string(cmd.Repo), Rebind: cmd.Rebind, Agent: cmp.Or(cmd.Agent, cfg.Auto.Agent), DryRun: cmd.DryRun, Log: stderr}
		res, err := a.IngestKnowledge(ctx, in)
		var conflict *knowledge.ConflictError
		if errors.As(err, &conflict) {
			fmt.Fprintf(stderr, "foreman: %v\nNothing was saved. Settle the competing guidance on one machine, then export and ingest again.\n", err)
			return 1
		}
		if errors.Is(err, knowledge.ErrUnfinished) {
			return failed(err)
		}
		if err != nil {
			return failed(fmt.Errorf("%w; nothing was saved", err))
		}
		fmt.Fprintln(stdout, ingestSummary(res))
		if cmd.DryRun && res.Mode != app.IngestUnchanged {
			fmt.Fprintf(stdout, "\nDry run: nothing was saved. The resulting knowledge.md:\n\n%s", res.Result.Guidance())
		}
	}
	return 0
}

func ingestSummary(res app.IngestResult) string {
	from := ""
	if res.Source != res.Repo {
		from = fmt.Sprintf(" (exported from %s)", res.Source)
	}
	x := res.Result
	active := 0
	for _, l := range x.Learnings {
		if l.Status == knowledge.Active {
			active++
		}
	}
	counts := fmt.Sprintf("%d learnings (%d active) and %d evaluations", len(x.Learnings), active, len(x.Evaluations))
	switch res.Mode {
	case app.IngestUnchanged:
		return fmt.Sprintf("%s already holds everything in the export%s; nothing changed.", res.Repo, from)
	case app.IngestRestored:
		return fmt.Sprintf("Restored %s%s: %s.", res.Repo, from, counts)
	}
	return fmt.Sprintf("Merged the export%s into %s: %s.", from, res.Repo, counts)
}
