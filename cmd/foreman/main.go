package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/cli"
	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/github"
	"github.com/brymartinez/foreman/internal/render"
	"github.com/brymartinez/foreman/internal/runner"
	"github.com/brymartinez/foreman/internal/stages"
	"github.com/brymartinez/foreman/internal/store"
	"github.com/brymartinez/foreman/internal/task"
	"github.com/brymartinez/foreman/internal/worktree"
)

// version is set at release time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	cmd, err := cli.Parse(argv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n\n%s", err, cli.Usage)
		return 64
	}
	switch cmd.Name {
	case cli.Help:
		fmt.Print(cli.Usage)
		return 0
	case cli.Version:
		fmt.Println("foreman", version)
		return 0
	}
	cfg, err := config.Load()
	if err != nil {
		return fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	a := app.App{
		Store:     store.Markdown{Dir: cfg.TasksDir},
		GitHub:    github.New(),
		Worktrees: worktree.Git{},
		Agents: map[task.Agent]app.AgentAdapter{
			task.Claude: agent.Claude{Bin: cfg.Claude.Command, Model: cfg.Claude.Model, AllowedTools: cfg.Claude.AllowedTools, Exec: agent.RunCommand},
			task.Codex:  agent.Codex{Bin: cfg.Codex.Command, Model: cfg.Codex.Model, Exec: agent.RunCommand},
		},
		Runners:              runner.Processes{LogDir: cfg.LogDir},
		Stages:               stages.Files{GlobalDir: cfg.StagesDir},
		WorktreesDir:         cfg.WorktreesDir,
		ReviewRounds:         cfg.ReviewRounds,
		CleanOnDone:          cfg.Cleanup.OnDone,
		DeleteMergedBranches: cfg.Cleanup.DeleteMergedBranch,
		Now:                  func() time.Time { return time.Now().Truncate(time.Second) },
	}

	switch cmd.Name {
	case cli.Inbox:
		report, err := a.Inbox(ctx)
		if err != nil {
			return fail(err)
		}
		render.Inbox(os.Stdout, report)
	case cli.Status:
		recs, err := a.Status(ctx)
		if err != nil {
			return fail(err)
		}
		render.Status(os.Stdout, recs)
	case cli.Show:
		rec, err := a.Find(ctx, cmd.Repo, cmd.Issue)
		if err != nil {
			return fail(err)
		}
		render.Show(os.Stdout, rec)
	case cli.Start:
		dir, err := os.Getwd()
		if err != nil {
			return fail(err)
		}
		contextFile := cmd.ContextFile
		if contextFile != "" {
			if contextFile, err = filepath.Abs(contextFile); err != nil {
				return fail(err)
			}
		}
		rec, err := a.Start(ctx, app.StartRequest{Repo: cmd.Repo, Issue: cmd.Issue, Agent: cmd.Agent, ContextFile: contextFile, BranchType: cmd.BranchType, Dir: dir})
		if err != nil {
			return fail(err)
		}
		render.Started(os.Stdout, rec)
		if rec.State == task.Failed {
			return 1
		}
	case cli.Clean:
		results, err := a.Clean(ctx, app.CleanRequest{Repo: cmd.Repo, Issue: cmd.Issue, DryRun: cmd.DryRun})
		if err != nil {
			return fail(err)
		}
		render.Cleaned(os.Stdout, results, cmd.DryRun)
		for _, r := range results {
			if r.Err != nil {
				return 1
			}
		}
	case cli.Respond:
		return respond(ctx, a, cmd)
	case cli.Run:
		fmt.Printf("foreman: runner %d started %s\n", os.Getpid(), time.Now().Format(time.RFC3339))
		if err := a.RunTask(ctx, task.Key{Repo: cmd.Repo, Number: cmd.Issue}, os.Getpid(), os.Stdout); err != nil {
			return fail(err)
		}
		fmt.Printf("foreman: runner %d finished %s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	}
	return 0
}

func respond(ctx context.Context, a app.App, cmd cli.Command) int {
	rec, err := a.Find(ctx, cmd.Repo, cmd.Issue)
	if err != nil {
		return fail(err)
	}
	pending := rec.Pending()
	if rec.State != task.NeedsDecision || pending == nil {
		return fail(fmt.Errorf("%s#%d is %s, not waiting for a decision", rec.Repo, rec.Number, rec.State))
	}
	fmt.Printf("#%d %s\n\n", rec.Number, rec.Title)
	render.Blocked(os.Stdout, pending.Question)
	prompt := "\nDecision:\n> "
	if o, ok := pending.Question.Recommended(); ok {
		prompt = fmt.Sprintf("\nDecision (option label, your own answer, or Enter for %s):\n> ", o.Label)
	}
	fmt.Print(prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" && !errors.Is(err, io.EOF) {
		return fail(err)
	}
	updated, err := a.Respond(ctx, rec.Key, line)
	if err != nil {
		return fail(err)
	}
	d := updated.Delegation
	fmt.Printf("\nRecorded: %s\n", updated.Decisions[len(updated.Decisions)-1].Answer)
	if updated.State == task.Failed {
		return fail(errors.New(updated.LastOutcome.Summary))
	}
	fmt.Printf("%s continues in %s (log: %s)\nCheck progress with: foreman status\n", d.Implementer.Agent, d.Worktree, d.RunnerLog)
	return 0
}

func fail(err error) int {
	fmt.Fprintf(os.Stderr, "foreman: %v\n", err)
	return 1
}
