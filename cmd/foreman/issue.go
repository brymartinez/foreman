package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/brymartinez/foreman/internal/cli"
	"github.com/brymartinez/foreman/internal/localissues"
	"github.com/brymartinez/foreman/internal/portable"
	"github.com/brymartinez/foreman/internal/store"
	"github.com/brymartinez/foreman/internal/task"
)

func isIssueCommand(n cli.Name) bool {
	switch n {
	case cli.IssueAdd, cli.IssueList, cli.IssueShow, cli.IssueTriage, cli.IssueExport, cli.IssueImport, cli.ExportNotes:
		return true
	}
	return false
}

// readText reads a file, or stdin for "-". An empty name reads as empty text.
func readText(name string, stdin io.Reader) (string, error) {
	switch name {
	case "":
		return "", nil
	case "-":
		raw, err := io.ReadAll(stdin)
		return string(raw), err
	}
	raw, err := os.ReadFile(name)
	return string(raw), err
}

func createLocalIssue(ctx context.Context, tx *sql.Tx, in localissues.Input, decisions string) (localissues.Issue, error) {
	issue, err := (localissues.Store{}).CreateIn(ctx, tx, in)
	if err == nil && decisions != "" {
		err = (store.SQLiteTriage{}).SaveIn(tx, task.Key{Source: task.Local, ExternalID: issue.ID}, decisions)
	}
	return issue, err
}

// runIssue runs the local issue commands. None needs stage files, an agent, or the
// network, and none writes to a tracker: a local issue's source URL is only context.
func runIssue(ctx context.Context, st state, cmd cli.Command, stdin io.Reader, stdout, stderr io.Writer) int {
	in := *cmd.Local
	failed := func(err error) int {
		fmt.Fprintf(stderr, "foreman: %v\n", err)
		return 1
	}
	switch cmd.Name {
	case cli.IssueAdd:
		description, err := readText(in.DescriptionFile, stdin)
		if err != nil {
			return failed(err)
		}
		notes, err := readText(in.NotesFile, stdin)
		if err != nil {
			return failed(err)
		}
		triage := ""
		if in.TriageFile != "" {
			if in.TriageFile == "-" && in.DescriptionFile == "-" {
				return failed(errors.New("only one of --description-file and --triage-file can read stdin"))
			}
			if triage, err = readText(in.TriageFile, stdin); err != nil {
				return failed(err)
			}
			if strings.TrimSpace(triage) == "" {
				return failed(errors.New("confirmed triage is empty"))
			}
		}
		// The issue and its triage are saved together, so an issue is never left
		// half-confirmed by a failure between the two writes.
		tx, err := st.DB.BeginTx(ctx, nil)
		if err != nil {
			return failed(err)
		}
		defer tx.Rollback()
		issue, err := createLocalIssue(ctx, tx, localissues.Input{Title: in.Title, Description: description, SourceURL: in.SourceURL, Repo: string(in.Repo), Notes: notes, Tags: in.Tags}, triage)
		if err != nil {
			return failed(err)
		}
		if err := tx.Commit(); err != nil {
			return failed(err)
		}
		fmt.Fprintln(stdout, issue.ID)
	case cli.IssueTriage:
		text, err := readText(in.TriageFile, stdin)
		if err != nil {
			return failed(err)
		}
		if _, err := st.Issues.Get(ctx, in.ID); err != nil {
			return failed(err)
		}
		if err := st.Triage.Save(task.Key{Source: task.Local, ExternalID: in.ID}, text); err != nil {
			return failed(err)
		}
		fmt.Fprintln(stdout, in.ID)
	case cli.IssueList:
		issues, err := st.Issues.List(ctx)
		if err != nil {
			return failed(err)
		}
		for _, i := range issues {
			status := "awaiting triage"
			if t, err := st.Triage.Read(task.Key{Source: task.Local, ExternalID: i.ID}); err == nil && t != "" {
				status = "triaged"
			}
			if i.Status == localissues.Done {
				status = "done"
			}
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", i.ID, status, i.Title)
		}
	case cli.IssueShow:
		i, err := st.Issues.Get(ctx, in.ID)
		if err != nil {
			return failed(err)
		}
		fmt.Fprintf(stdout, "%s  %s\nstatus: %s\n", i.ID, i.Title, i.Status)
		if i.SourceURL != "" {
			fmt.Fprintf(stdout, "source: %s\n", i.SourceURL)
		}
		if i.Repo != "" {
			fmt.Fprintf(stdout, "repository: %s\n", i.Repo)
		}
		if len(i.Tags) > 0 {
			fmt.Fprintf(stdout, "tags: %s\n", strings.Join(i.Tags, ", "))
		}
		if i.Description != "" {
			fmt.Fprintf(stdout, "\n%s\n", i.Description)
		}
		if t, err := st.Triage.Read(task.Key{Source: task.Local, ExternalID: i.ID}); err == nil && t != "" {
			fmt.Fprintf(stdout, "\nConfirmed triage:\n\n%s\n", t)
		} else {
			fmt.Fprintln(stdout, "\nAwaiting triage.")
		}
	case cli.IssueExport:
		f, err := (portable.Service{DB: st.DB}).Export(ctx, portable.ExportOptions{IssueID: in.ID, Learnings: in.Learnings})
		if err != nil {
			return failed(err)
		}
		data, err := portable.Marshal(f)
		if err != nil {
			return failed(err)
		}
		if in.Output == "" {
			_, err = stdout.Write(data)
		} else {
			err = os.WriteFile(in.Output, data, 0o600)
		}
		if err != nil {
			return failed(err)
		}
		if in.Output != "" {
			fmt.Fprintf(stdout, "Exported %d local issues to %s.\n", len(f.Issues), in.Output)
		}
	case cli.IssueImport:
		data, err := os.ReadFile(in.File)
		if err != nil {
			return failed(err)
		}
		f, err := portable.Parse(data)
		if err != nil {
			return failed(fmt.Errorf("%w; nothing was imported", err))
		}
		svc := portable.Service{DB: st.DB, Guidance: func(repo string) {
			if err := st.Knowledge.WriteGuidance(repo); err != nil {
				fmt.Fprintf(stderr, "foreman: refresh knowledge.md for %s: %v\n", repo, err)
			}
		}}
		var plan portable.Plan
		if in.DryRun {
			plan, err = svc.Preview(ctx, f)
		} else {
			plan, err = svc.Apply(ctx, f, portable.ConflictPolicy(in.OnConflict))
		}
		printPlan(stdout, plan, in.DryRun)
		if errors.Is(err, portable.ErrConflicts) {
			fmt.Fprintf(stderr, "foreman: %v\nRerun with --on-conflict skip to keep this database's version, or replace to take the file's.\n", err)
			return 1
		}
		if err != nil {
			return failed(fmt.Errorf("%w; nothing was imported", err))
		}
	case cli.ExportNotes:
		recs, err := st.Tasks.List(ctx)
		if err != nil {
			return failed(err)
		}
		if err := os.MkdirAll(in.Dir, 0o755); err != nil {
			return failed(err)
		}
		md := store.Markdown{Dir: in.Dir}
		for _, rec := range recs {
			doc, err := st.Tasks.Document(rec.Key)
			if err != nil {
				return failed(err)
			}
			if err := os.WriteFile(md.Path(rec.Key), []byte(doc), 0o644); err != nil {
				return failed(err)
			}
		}
		fmt.Fprintf(stdout, "Exported %d tasks to %s. This is a one-way copy: edits to these notes stay in %s.\n", len(recs), filepath.Clean(in.Dir), "the Markdown files")
	}
	return 0
}

func printPlan(w io.Writer, p portable.Plan, dry bool) {
	verb := "Imported"
	if dry {
		verb = "Would import"
	}
	fmt.Fprintf(w, "%s %d new records; %d already present and unchanged; %d conflicts.\n", verb, len(p.Create), len(p.Unchanged), len(p.Conflicts))
	for _, c := range p.Conflicts {
		fmt.Fprintf(w, "  conflict: %s %s %s\n", c.Kind, c.ID, c.Detail)
	}
}
