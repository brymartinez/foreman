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
	"runtime/debug"
	"slices"
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
	"github.com/brymartinez/foreman/internal/tui"
	"github.com/brymartinez/foreman/internal/worktree"

	"golang.org/x/term"
)

// localRefresh is how often the dashboard re-reads the task files and the selected
// log. It is cheap, so it is fixed; the GitHub refresh runs the searches, the
// readiness gate, and the cleanup, so its interval is the `refresh` setting.
const localRefresh = time.Second

// version is set at release time with -ldflags "-X main.version=v1.2.3".
var version = developmentVersion

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
		info, _ := debug.ReadBuildInfo()
		fmt.Println("foreman", buildVersion(version, info))
		return 0
	case cli.TUI:
		// A pipe or a script gets the usage text, the way a bare `foreman` always has.
		// This runs before the config is loaded so that a broken config or a missing
		// stage file cannot fail a piped `foreman` that used to print usage (R12).
		if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
			fmt.Print(cli.Usage)
			return 0
		}
	}
	// An invalid config must not lock the developer out of the command that fixes it.
	if cmd.Name == cli.Config && cmd.ConfigEdit {
		return editConfig()
	}
	cfg, err := config.Load()
	if err != nil {
		return fail(err)
	}
	// The detached runner has no one to read its stderr, and config --show prints
	// these warnings itself.
	if cmd.Name != cli.Run && !(cmd.Name == cli.Config && cmd.ConfigShow) {
		for _, problem := range cfg.WebhookProblems() {
			fmt.Fprintf(os.Stderr, "foreman: %s\n", problem)
		}
	}
	// config stays usable so a missing stage file can be diagnosed.
	if cmd.Name != cli.Config {
		if err := (stages.Files{GlobalDir: cfg.StagesDir}).RequireGlobal(); err != nil {
			return fail(err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	a := app.App{
		Store:        store.Markdown{Dir: cfg.TasksDir},
		GitHub:       github.New(),
		Repositories: github.New(),
		Worktrees:    worktree.Git{},
		Agents: map[task.Agent]app.AgentAdapter{
			task.Claude: agent.Claude{Bin: cfg.Claude.Command, Model: cfg.Claude.Model, PlanModel: cfg.Claude.PlanModel, ReviewModel: cfg.Claude.ReviewModel, Exec: agent.RunCommand},
			task.Codex: agent.Codex{Bin: cfg.Codex.Command, Model: cfg.Codex.Model, PlanModel: cfg.Codex.PlanModel, ReviewModel: cfg.Codex.ReviewModel,
				ReasoningEffort: cfg.Codex.ReasoningEffort, PlanReasoningEffort: cfg.Codex.PlanReasoningEffort, ReviewReasoningEffort: cfg.Codex.ReviewReasoningEffort, Exec: agent.RunCommand},
		},
		Runners:              runner.Processes{LogDir: cfg.LogDir},
		Stages:               stages.Files{GlobalDir: cfg.StagesDir},
		WorktreesDir:         cfg.WorktreesDir,
		RepositoriesDir:      cfg.RepositoriesDir,
		ReviewRounds:         cfg.ReviewRounds,
		FixRounds:            cfg.FixRounds,
		ImplementerBudgetUSD: cfg.Budgets.ImplementerUSD,
		ReviewerBudgetUSD:    cfg.Budgets.ReviewerUSD,
		Notifier:             notification(cfg),
		CleanOnDone:          cfg.Cleanup.OnDone,
		DeleteMergedBranches: cfg.Cleanup.DeleteMergedBranch,
		IncludedRepositories: cfg.IncludedRepos,
		ExcludedRepositories: cfg.ExcludedRepos,
		HideMentionOnly:      !cfg.Mentions,
		Now:                  func() time.Time { return time.Now().Truncate(time.Second) },
	}

	switch cmd.Name {
	case cli.TUI:
		ready, err := config.EnsureTasksDir(bufio.NewReader(os.Stdin), os.Stdout, cfg.TasksDir)
		if err != nil {
			return fail(err)
		}
		if !ready {
			return 0
		}
		if err := tui.Run(ctx, tui.Options{
			In: os.Stdin, Out: os.Stdout, Source: a,
			GitHubEvery: time.Duration(cfg.Refresh) * time.Second, LocalEvery: localRefresh,
			Auto: autoMode(cfg.Auto),
		}); err != nil {
			return fail(err)
		}
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
		render.InboxActivity(os.Stdout, report)
		selection, err := app.ChooseInbox(stdin, os.Stdout, report)
		if err != nil {
			return fail(err)
		}
		if selection == nil {
			break
		}
		if selection.Resume {
			rec, err := a.Resume(ctx, selection.Key)
			if err != nil {
				return fail(err)
			}
			render.Resumed(os.Stdout, rec)
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
		recs, warnings, err := a.Status(ctx)
		if err != nil {
			return fail(err)
		}
		render.Status(os.Stdout, recs)
		render.Warnings(os.Stdout, warnings)
	case cli.Show:
		rec, err := a.Find(ctx, cmd.Repo, cmd.Issue)
		if err != nil {
			return fail(err)
		}
		render.Show(os.Stdout, rec)
	case cli.Cancel:
		rec, err := a.Find(ctx, cmd.Repo, cmd.Issue)
		if err != nil {
			return fail(err)
		}
		if rec, err = a.Cancel(ctx, rec); err != nil {
			return fail(err)
		}
		render.Cancelled(os.Stdout, rec)
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
	case cli.Resume:
		rec, err := a.Find(ctx, cmd.Repo, cmd.Issue)
		if err != nil {
			return fail(err)
		}
		if rec, err = a.Resume(ctx, rec.Key); err != nil {
			return fail(err)
		}
		render.Resumed(os.Stdout, rec)
		if rec.State == task.Failed {
			return 1
		}
	case cli.Respond:
		return respond(ctx, a, cmd)
	case cli.Config:
		switch {
		case cmd.ConfigShow:
			render.Config(os.Stdout, cfg)
			return 0
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

// autoMode reads the saved auto mode choice. A state file that cannot be read
// leaves auto mode on, and the dashboard shows why.
func autoMode(cfg config.Auto) tui.Auto {
	auto := tui.Auto{Enabled: true, Agent: cfg.Agent, Max: cfg.MaxConcurrent}
	path, err := config.StatePath()
	if err != nil {
		auto.LoadErr = err
		return auto
	}
	auto.Enabled, auto.LoadErr = config.LoadAutoEnabled(path)
	auto.Save = func(on bool) error { return config.SaveAutoEnabled(path, on) }
	return auto
}

func notification(cfg config.Config) app.Notifier {
	n := cfg.Notifications
	if !n.Enabled || n.WebhookURL == "" {
		return nil
	}
	return notifications.Discord{WebhookURL: n.WebhookURL, LogPath: filepath.Join(cfg.LogDir, "notifications.log")}
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
	if rec.State != task.NeedsDecision || rec.Pending() == nil {
		return fail(fmt.Errorf("%s#%d is %s, not waiting for a decision", rec.Repo, rec.Number, rec.State))
	}
	fmt.Printf("%s#%d %s\n", rec.Repo, rec.Number, rec.Title)
	stdin := bufio.NewReader(os.Stdin)
	total := rec.PendingCount()
	for i := 1; rec.State == task.NeedsDecision; i++ {
		idx := slices.IndexFunc(rec.Decisions, func(d task.Decision) bool { return d.Answer == "" })
		pending := rec.Decisions[idx]
		fmt.Println()
		if total > 1 {
			fmt.Printf("Question %d of %d\n", i, total)
		}
		render.Blocked(os.Stdout, pending.Question)
		prompt := "\nDecision:\n> "
		if o, ok := pending.Question.Recommended(); ok {
			prompt = fmt.Sprintf("\nDecision (option label, your own answer, or Enter for %s):\n> ", o.Label)
		}
		fmt.Print(prompt)
		line, err := stdin.ReadString('\n')
		if err != nil && line == "" {
			if errors.Is(err, io.EOF) {
				// No answer typed: never accept a recommendation the developer didn't see through.
				fmt.Printf("\n%d %s still waiting; run foreman respond %s#%d again.\n", rec.PendingCount(), plural(rec.PendingCount(), "question is", "questions are"), rec.Repo, rec.Number)
				return 1
			}
			return fail(err)
		}
		if rec, err = a.Respond(ctx, rec.Key, line); err != nil {
			return fail(err)
		}
		fmt.Printf("\nRecorded: %s\n", rec.Decisions[idx].Answer)
	}
	d := rec.Delegation
	if rec.State == task.Failed {
		return fail(errors.New(rec.LastOutcome.Summary))
	}
	fmt.Printf("%s continues in %s (log: %s)\nCheck progress with: foreman status\n", d.Implementer.Agent, d.Worktree, d.RunnerLog)
	return 0
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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
		if r.GlobalMissing() {
			code = 1
		}
		reports = append(reports, r)
	}
	home, _ := os.UserHomeDir()
	render.StageSources(os.Stdout, reports, home, root)
	return code
}

// editConfig opens the config file in the developer's editor. A file that fails to
// load is opened as it is, with the error shown first; a missing or empty one is
// filled with the defaults. The file is checked again once the editor exits.
func editConfig() int {
	path, err := config.FilePath()
	if err != nil {
		return fail(err)
	}
	if cfg, err := config.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "foreman: %v\nOpening %s so you can fix it.\n", err, path)
	} else if err := config.Materialize(path, cfg); err != nil {
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
	if _, err := config.Load(); err != nil {
		return fail(fmt.Errorf("%v; run foreman config --edit again to fix it", err))
	}
	return 0
}

func fail(err error) int {
	fmt.Fprintf(os.Stderr, "foreman: %v\n", err)
	return 1
}
