package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/cli"
	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/github"
	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/linear"
	"github.com/brymartinez/foreman/internal/logs"
	"github.com/brymartinez/foreman/internal/notifications"
	"github.com/brymartinez/foreman/internal/quota"
	"github.com/brymartinez/foreman/internal/routing"
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
		fmt.Println("foreman", currentVersion())
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
	if cmd.Name == cli.Edit {
		return editConfig()
	}
	cfg, err := config.Load()
	if err != nil {
		return fail(err)
	}
	// The detached runner and learner have no one to read their stderr.
	if cmd.Name != cli.Run && cmd.Name != cli.Learn {
		for _, problem := range cfg.WebhookProblems() {
			fmt.Fprintf(os.Stderr, "foreman: %s\n", problem)
		}
	}
	if err := (stages.Files{GlobalDir: cfg.StagesDir}).RequireGlobal(); err != nil {
		return fail(err)
	}
	if cmd.Name == cli.TUI && linearSelected(cfg, cmd.Source) && os.Getenv("LINEAR_API_KEY") == "" {
		return fail(fmt.Errorf("set LINEAR_API_KEY to use Linear issues; use -s github for GitHub only"))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	claude := agent.Claude{Bin: cfg.Claude.Command, Model: cfg.Claude.Model, PlanModel: cfg.Claude.PlanModel, ReviewModel: cfg.Claude.ReviewModel, Exec: agent.RunCommand}
	codex := agent.Codex{Bin: cfg.Codex.Command, Model: cfg.Codex.Model, PlanModel: cfg.Codex.PlanModel, ReviewModel: cfg.Codex.ReviewModel,
		ReasoningEffort: cfg.Codex.ReasoningEffort, PlanReasoningEffort: cfg.Codex.PlanReasoningEffort, ReviewReasoningEffort: cfg.Codex.ReviewReasoningEffort, Exec: agent.RunCommand}
	// A routing.yaml that fails to load stops only the dashboard starts that route.
	routes, routesErr := routing.Load(routing.Path(cfg.Path))
	linearClient := linear.New(os.Getenv("LINEAR_API_KEY"))
	if endpoint := os.Getenv("FOREMAN_LINEAR_ENDPOINT"); endpoint != "" {
		linearClient.Endpoint = endpoint
	}
	a := app.App{
		ReplyToPRFeedback: false,
		Store:             store.Markdown{Dir: cfg.TasksDir},
		GitHub:            github.New(),
		Linear:            linearClient,
		LinearProjects:    cfg.LinearProjects,
		Repositories:      github.New(),
		Worktrees:         worktree.Git{},
		Accounts:          github.New(),
		RemoteAuth:        worktree.Git{},
		Agents:            map[task.Agent]app.AgentAdapter{task.Claude: claude, task.Codex: codex},
		BranchProposer:    agent.BranchProposers{Claude: claude, Codex: codex},
		Routing:           routes,
		RoutingErr:        routesErr,
		RoutingDefaults: routing.Defaults{
			task.Claude: {Model: cfg.Claude.Model, PlanModel: cfg.Claude.PlanModel},
			task.Codex:  {Model: cfg.Codex.Model, PlanModel: cfg.Codex.PlanModel, ReasoningEffort: cfg.Codex.ReasoningEffort, PlanReasoningEffort: cfg.Codex.PlanReasoningEffort},
		},
		Selector:             agent.Selectors{Claude: claude, Codex: codex},
		Knowledge:            knowledge.Store{Dir: cfg.KnowledgeDir},
		Evaluator:            agent.Evaluators{Claude: claude, Codex: codex},
		Learning:             cfg.Learning.Enabled,
		LearningBudgetUSD:    cfg.Learning.BudgetUSD,
		Quota:                quota.Axi{Command: "quota-axi", Exec: agent.RunCommand},
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
		LogDir:               cfg.LogDir,
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
			IssueSource: cmd.Source,
			GitHubEvery: time.Duration(cfg.Refresh) * time.Second, LocalEvery: localRefresh,
			Auto:     autoMode(cfg.Auto, cmd.Auto),
			Route:    routes != nil || routesErr != nil,
			Version:  currentVersion(),
			TaskFile: store.Markdown{Dir: cfg.TasksDir}.Path,
		}); err != nil {
			return fail(err)
		}
	case cli.Run:
		log := &logs.Writer{W: os.Stdout}
		defer log.Flush()
		fmt.Fprintf(log, "foreman: runner %d started\n", os.Getpid())
		// The agents and learners this runner starts hold no launch.
		token := os.Getenv(runner.LaunchEnv)
		os.Unsetenv(runner.LaunchEnv)
		if err := a.RunTask(ctx, task.Key{Repo: cmd.Repo, Number: cmd.Issue, Source: cmd.Source, ExternalID: cmd.ExternalID}, os.Getpid(), token, log); err != nil {
			fmt.Fprintf(log, "foreman: %v\n", err)
			return 1
		}
		fmt.Fprintf(log, "foreman: runner %d finished\n", os.Getpid())
	case cli.Learn:
		// Every learner shares learning.log, so each line names the task. A failure
		// here is the log's alone: the task, its alerts, and its runner never see it.
		log := &logs.Writer{W: os.Stdout}
		defer log.Flush()
		key := task.Key{Repo: cmd.Repo, Number: cmd.Issue, Source: cmd.Source, ExternalID: cmd.ExternalID}
		name := fmt.Sprintf("%s#%d", key.Repo, key.Number)
		if key.Source == task.Linear {
			name = fmt.Sprintf("%s#linear:%s", key.Repo, key.ExternalID)
		}
		a.Knowledge = knowledge.Store{Dir: cfg.KnowledgeDir, Log: log}
		err := a.Learn(ctx, key, log)
		switch {
		case errors.Is(err, knowledge.ErrLocked):
			fmt.Fprintf(log, "learn: %s: %s is being evaluated already; a later refresh retries\n", name, key.Repo)
		case err != nil:
			fmt.Fprintf(log, "learn: %s: %v\n", name, err)
			return 1
		}
	}
	return 0
}

func linearSelected(cfg config.Config, source task.Source) bool {
	if source == task.GitHub {
		return false
	}
	for repo := range cfg.LinearProjects {
		if len(cfg.IncludedRepos) > 0 {
			for _, included := range cfg.IncludedRepos {
				if included == repo {
					return true
				}
			}
			continue
		}
		excluded := false
		for _, entry := range cfg.ExcludedRepos {
			if entry == repo {
				excluded = true
				break
			}
		}
		if !excluded {
			return true
		}
	}
	return false
}

// autoMode reads the saved auto mode choice. A state file that cannot be read
// leaves auto mode on, and the dashboard shows why. A launch flag replaces the
// saved choice for this session without writing it, so its read error is moot;
// toggling with a still saves.
func autoMode(cfg config.Auto, override cli.AutoMode) tui.Auto {
	auto := tui.Auto{Enabled: true, Agent: cfg.Agent, Max: cfg.MaxConcurrent}
	path, err := config.StatePath()
	if err != nil {
		auto.LoadErr = err
	} else {
		auto.Enabled, auto.LoadErr = config.LoadAutoEnabled(path)
		auto.Save = func(on bool) error { return config.SaveAutoEnabled(path, on) }
	}
	if override != cli.AutoSaved {
		auto.Enabled, auto.LoadErr = override == cli.AutoOn, nil
	}
	return auto
}

func currentVersion() string {
	info, _ := debug.ReadBuildInfo()
	return buildVersion(version, info)
}

func notification(cfg config.Config) app.Notifier {
	n := cfg.Notifications
	if !n.Enabled || n.WebhookURL == "" {
		return nil
	}
	return notifications.Discord{WebhookURL: n.WebhookURL, LogPath: filepath.Join(cfg.LogDir, "notifications.log")}
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
		return fail(fmt.Errorf("%v; run foreman -c again to fix it", err))
	}
	return 0
}

func fail(err error) int {
	fmt.Fprintf(os.Stderr, "foreman: %v\n", err)
	return 1
}
