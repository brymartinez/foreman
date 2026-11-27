package main

import (
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
	"github.com/brymartinez/foreman/internal/workspace"
	"go.yaml.in/yaml/v3"
)

// runKnowledge runs `foreman knowledge export` or `ingest`. Neither needs the stage
// files, the dashboard, or a task.
func runKnowledge(ctx context.Context, cfg config.Config, st state, cmd cli.Command, stdout, stderr io.Writer) int {
	a := app.App{
		Knowledge: st.Knowledge,
		Workspace: st.Workspace,
		WorkspaceLog: func(format string, args ...any) {
			fmt.Fprintf(stderr, "foreman: "+format+"\n", args...)
		},
		Merger: agent.Mergers{
			Claude: agent.Claude{Bin: "claude", Exec: agent.RunCommand},
			Codex:  agent.Codex{Bin: "codex", Exec: agent.RunCommand},
		},
		Models:            cfg.Models,
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
		in := app.IngestInput{Data: data, Repo: string(cmd.Repo), Rebind: cmd.Rebind, DryRun: cmd.DryRun, Log: stderr}
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
	case cli.KnowledgeReview:
		sync, err := sharedWorkspace(st)
		if err != nil {
			return failed(err)
		}
		review, err := sync.ReviewKnowledge(ctx, string(cmd.Repo))
		if err != nil {
			return failed(err)
		}
		if err := writeKnowledgeReview(stdout, review); err != nil {
			return failed(err)
		}
	case cli.KnowledgeApprove:
		sync, err := sharedWorkspace(st)
		if err != nil {
			return failed(err)
		}
		if err := sync.ApproveKnowledge(ctx, string(cmd.Repo), cmd.Digest, cmd.HostDigest); err != nil {
			return failed(err)
		}
		fmt.Fprintf(stdout, "Approved %s at shared digest %s.\n", cmd.Repo, cmd.Digest)
	}
	return 0
}

func sharedWorkspace(st state) (*workspace.Sync, error) {
	sync, ok := st.Workspace.(*workspace.Sync)
	if !ok || sync == nil {
		return nil, errors.New("knowledge review and approval require storage.backend to use notion or obsidian")
	}
	return sync, nil
}

func writeKnowledgeReview(w io.Writer, review workspace.KnowledgeReview) error {
	fmt.Fprintf(w, "Repository: %s\nShared digest: %s\nHost digest: %s\n\n", review.Repo, review.SharedDigest, review.HostDigest)
	fmt.Fprintln(w, "Current host snapshot:")
	if err := writeReviewSnapshot(w, review.Host); err != nil {
		return err
	}
	fmt.Fprintln(w, "Pending shared snapshot:")
	if err := writeReviewSnapshot(w, review.Shared); err != nil {
		return err
	}
	fmt.Fprintf(w, "Approve the snapshot you reviewed with:\nforeman knowledge approve %s --digest %s --host-digest %s\n", review.Repo, review.SharedDigest, review.HostDigest)
	return nil
}

func writeReviewSnapshot(w io.Writer, snapshot knowledge.Snapshot) error {
	snapshot.ExportedAt = time.Time{}
	snapshot.Format = knowledge.FormatVersion
	raw, err := yaml.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = w.Write(raw)
	return err
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
