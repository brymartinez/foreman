package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/cli"
	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/github"
	"github.com/brymartinez/foreman/internal/logs"
	"github.com/brymartinez/foreman/internal/notifications"
	"github.com/brymartinez/foreman/internal/render"
	"github.com/brymartinez/foreman/internal/runner"
	"github.com/brymartinez/foreman/internal/stages"
	"github.com/brymartinez/foreman/internal/store"
	"github.com/brymartinez/foreman/internal/task"
	"github.com/brymartinez/foreman/internal/worktree"
)

// version is set at release time with -ldflags "-X main.version=v1.2.3".
var version = "0.1.0-dev"

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
		Store:        store.Markdown{Dir: cfg.TasksDir},
		GitHub:       github.New(),
		Repositories: github.New(),
		Worktrees:    worktree.Git{},
		Agents: map[task.Agent]app.AgentAdapter{
			task.Claude: agent.Claude{Bin: cfg.Claude.Command, Model: cfg.Claude.Model, AllowedTools: cfg.Claude.AllowedTools, Exec: agent.RunCommand},
			task.Codex:  agent.Codex{Bin: cfg.Codex.Command, Model: cfg.Codex.Model, Exec: agent.RunCommand},
		},
		Runners:              runner.Processes{LogDir: cfg.LogDir},
		Stages:               stages.Files{GlobalDir: cfg.StagesDir},
		WorktreesDir:         cfg.WorktreesDir,
		RepositoriesDir:      cfg.RepositoriesDir,
		ReviewRounds:         cfg.ReviewRounds,
		ImplementerBudgetUSD: cfg.Budgets.ImplementerUSD,
		ReviewerBudgetUSD:    cfg.Budgets.ReviewerUSD,
		Notifier:             notification(cfg.Notifications.Enabled),
		CleanOnDone:          cfg.Cleanup.OnDone,
		DeleteMergedBranches: cfg.Cleanup.DeleteMergedBranch,
		IgnoredRepositories:  cfg.IgnoredRepos,
		Now:                  func() time.Time { return time.Now().Truncate(time.Second) },
	}

	switch cmd.Name {
	case cli.Inbox:
		stdin := bufio.NewReader(os.Stdin)
		if cmd.NonInteractive {
			if _, err := os.Stat(cfg.TasksDir); err != nil {
				return fail(fmt.Errorf("tasks directory %s is not ready; run interactive foreman inbox first: %w", cfg.TasksDir, err))
			}
		} else {
			ready, err := config.EnsureTasksDir(stdin, os.Stdout, cfg.TasksDir)
			if err != nil {
				return fail(err)
			}
			if !ready {
				return 0
			}
		}
		report, err := a.Inbox(ctx)
		if err != nil {
			return fail(err)
		}
		if cmd.NonInteractive {
			render.Inbox(os.Stdout, report)
			break
		}
		render.SearchLimits(os.Stdout, report.Truncated)
		selection, err := app.ChooseInbox(stdin, os.Stdout, report)
		if err != nil {
			return fail(err)
		}
		if selection == nil {
			break
		}
		contextFile := selection.ContextFile
		if contextFile != "" {
			if contextFile, err = filepath.Abs(contextFile); err != nil {
				return fail(err)
			}
		}
		rec, err := a.Start(ctx, app.StartRequest{Repo: selection.Key.Repo, Issue: selection.Key.Number, Agent: selection.Agent, ContextFile: contextFile})
		if err != nil {
			return fail(err)
		}
		render.Started(os.Stdout, rec)
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
	case cli.Logs:
		rec, err := a.Find(ctx, cmd.Repo, cmd.Issue)
		if err != nil {
			return fail(err)
		}
		if rec.Delegation == nil || rec.Delegation.RunnerLog == "" {
			return fail(errors.New("task has no runner log"))
		}
		if err := showLogs(ctx, rec.Delegation.RunnerLog, cmd.RawLogs, cmd.Follow); err != nil {
			return fail(err)
		}
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
	case cli.Config:
		switch {
		case cmd.ConfigShow:
			render.Config(os.Stdout, cfg)
			return 0
		case cmd.ConfigEdit:
			return editConfig(cfg.Path, cfg)
		default:
			return stageConfig(ctx, cfg)
		}
	case cli.Run:
		log := &logs.Writer{W: os.Stdout}
		defer log.Flush()
		fmt.Fprintf(log, "foreman: runner %d started\n", os.Getpid())
		if err := a.RunTask(ctx, task.Key{Repo: cmd.Repo, Number: cmd.Issue}, os.Getpid(), log); err != nil {
			fmt.Fprintf(log, "foreman: %v\n", err)
			return 1
		}
		fmt.Fprintf(log, "foreman: runner %d finished\n", os.Getpid())
	}
	return 0
}

func notification(enabled bool) app.Notifier {
	if enabled {
		return notifications.Native{}
	}
	return nil
}

func showLogs(ctx context.Context, path string, raw, follow bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return logs.Show(ctx, os.Stdout, f, raw, follow, 500*time.Millisecond)
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
	fmt.Printf("%s#%d %s\n\n", rec.Repo, rec.Number, rec.Title)
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

// stageConfig shows the stage instructions in effect for the repository at the
// current directory. An empty stage lists every stage's sources.
func stageConfig(ctx context.Context, cfg config.Config) int {
	dir, err := os.Getwd()
	if err != nil {
		return fail(err)
	}
	root, err := stages.RepoRoot(ctx, dir)
	if err != nil {
		return fail(err)
	}
	files := stages.Files{GlobalDir: cfg.StagesDir}
	var reports []render.StageReport
	code := 0
	for _, st := range task.Stages {
		r := render.StageReport{GlobalWhere: filepath.Join(cfg.StagesDir, string(st)+".md")}
		if root != "" {
			r.RepoWhere = filepath.Join(root, stages.RepoDir, string(st)+".md")
		}
		if r.Instructions, r.Err = files.Resolve(root, st); r.Err != nil {
			r.Instructions.Stage, code = st, 1
		}
		reports = append(reports, r)
	}
	home, _ := os.UserHomeDir()
	render.StageSources(os.Stdout, reports, home, root)
	return code
}

func editConfig(path string, cfg config.Config) int {
	if err := config.Materialize(path, cfg); err != nil {
		return fail(fmt.Errorf("prepare config %s: %w", path, err))
	}
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	// Through the shell, as git does, so an editor with arguments ("code -w") works.
	cmd := exec.Command("sh", "-c", editor+` "$1"`, "sh", path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fail(fmt.Errorf("edit config with %s: %w", editor, err))
	}
	return 0
}

func fail(err error) int {
	fmt.Fprintf(os.Stderr, "foreman: %v\n", err)
	return 1
}
