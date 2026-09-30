// Package cli parses argv into a Command. It knows nothing about GitHub, storage, or agents.
package cli

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

type Name string

const (
	Help    Name = "help"
	Inbox   Name = "inbox"
	Status  Name = "status"
	Show    Name = "show"
	Start   Name = "start"
	Respond Name = "respond"
	Cancel  Name = "cancel"
	Clean   Name = "clean"
	Config  Name = "config"
	Logs    Name = "logs"
	Version Name = "version"
	// TUI is the dashboard, and the command a bare `foreman` runs.
	TUI Name = "tui"
	// Run is the hidden verb the detached runner process executes (see internal/runner).
	Run Name = "__run"
)

type Command struct {
	Name        Name
	Repo        task.Repo // set when the issue is given as owner/repo#825
	Issue       int
	Agent       task.Agent
	ContextFile string
	BranchType  task.BranchType
	DryRun      bool
	// NonInteractive keeps report output for scripts; inbox is interactive by default.
	NonInteractive bool
	ConfigStages   bool
	ConfigShow     bool
	ConfigEdit     bool
	Follow         bool
	RawLogs        bool
}

const Usage = `foreman: delegate GitHub issues to Claude Code or Codex

Usage:
  foreman                                         Dashboard: inbox, delegated work, and logs
  foreman inbox [--non-interactive]               Pick work to delegate (or print a report)
  foreman status                                  Delegated work and its state
  foreman show <issue>                            Durable state for one task
  foreman logs <issue> [--follow] [--raw]          Read a task's runner log
  foreman start <issue> --agent claude|codex [--context path.md] [--type fix]
  foreman respond <issue>                         Answer a NEEDS_DECISION task
  foreman cancel <issue>                          Stop a task's running agent
  foreman clean [<issue>] [--dry-run]             Remove worktrees of DONE tasks (or one DONE/FAILED task)
  foreman config --stages                         Where each stage's instructions come from
  foreman config --show                           Show the resolved configuration
  foreman config --edit                           Edit the configuration file
  foreman version

<issue> is 825, #825, or owner/repo#825 when the number exists in several repos.
`

type UsageError struct{ msg string }

func (e UsageError) Error() string { return e.msg }

func usagef(format string, args ...any) error { return UsageError{fmt.Sprintf(format, args...)} }

func Parse(argv []string) (Command, error) {
	if len(argv) == 0 {
		return Command{Name: TUI}, nil
	}
	name, rest := argv[0], argv[1:]
	switch name {
	case "help", "-h", "--help":
		return Command{Name: Help}, nil
	case string(TUI):
		if len(rest) > 0 {
			return Command{}, usagef("%s takes no arguments", name)
		}
		return Command{Name: TUI}, nil
	case "version", "--version":
		return Command{Name: Version}, nil
	case "clean":
		return parseClean(rest)
	case "config":
		return parseConfig(rest)
	case "inbox":
		return parseInbox(rest)
	case "status":
		if len(rest) > 0 {
			return Command{}, usagef("%s takes no arguments", name)
		}
		return Command{Name: Name(name)}, nil
	case "show", "respond", "cancel":
		repo, n, err := issueArg(name, rest)
		return Command{Name: Name(name), Repo: repo, Issue: n}, err
	case "logs":
		return parseLogs(rest)
	case "start":
		return parseStart(rest)
	case string(Run):
		repo, n, err := issueArg(name, rest)
		if err == nil && repo == "" {
			err = usagef("%s needs owner/repo#N", name)
		}
		return Command{Name: Run, Repo: repo, Issue: n}, err
	}
	return Command{}, usagef("unknown command %q", name)
}

func parseLogs(rest []string) (Command, error) {
	cmd := Command{Name: Logs}
	var positionals []string
	for _, arg := range rest {
		switch arg {
		case "--follow", "-f":
			cmd.Follow = true
		case "--raw":
			cmd.RawLogs = true
		default:
			if strings.HasPrefix(arg, "-") {
				return Command{}, usagef("logs: unknown flag %s", arg)
			}
			positionals = append(positionals, arg)
		}
	}
	repo, n, err := issueArg("logs", positionals)
	cmd.Repo, cmd.Issue = repo, n
	return cmd, err
}

func parseInbox(rest []string) (Command, error) {
	cmd := Command{Name: Inbox}
	for _, arg := range rest {
		switch arg {
		case "--non-interactive":
			cmd.NonInteractive = true
		default:
			if strings.HasPrefix(arg, "-") {
				return Command{}, usagef("inbox: unknown flag %s", arg)
			}
			return Command{}, usagef("inbox takes no arguments")
		}
	}
	return cmd, nil
}

func parseStart(rest []string) (Command, error) {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	agentFlag := fs.String("agent", "", "")
	contextFlag := fs.String("context", "", "")
	typeFlag := fs.String("type", "", "")
	// Go's flag package stops at the first positional, so pull the issue number out first.
	var positionals, flags []string
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			if !strings.Contains(arg, "=") && i+1 < len(rest) {
				flags = append(flags, rest[i+1])
				i++
			}
			continue
		}
		positionals = append(positionals, arg)
	}
	if err := fs.Parse(flags); err != nil {
		return Command{}, usagef("start: %v", err)
	}
	repo, n, err := issueArg("start", positionals)
	if err != nil {
		return Command{}, err
	}
	a, err := task.ParseAgent(*agentFlag)
	if err != nil {
		return Command{}, usagef("start needs --agent claude|codex")
	}
	var bt task.BranchType
	if *typeFlag != "" {
		if bt, err = task.ParseBranchType(*typeFlag); err != nil {
			return Command{}, usagef("start: %v", err)
		}
	}
	return Command{Name: Start, Repo: repo, Issue: n, Agent: a, ContextFile: *contextFlag, BranchType: bt}, nil
}

func parseConfig(rest []string) (Command, error) {
	cmd := Command{Name: Config}
	for _, arg := range rest {
		switch arg {
		case "--stages":
			cmd.ConfigStages = true
		case "--show":
			cmd.ConfigShow = true
		case "--edit":
			cmd.ConfigEdit = true
		default:
			return Command{}, usagef("config: unknown flag %s", arg)
		}
	}
	if (boolCount(cmd.ConfigStages, cmd.ConfigShow, cmd.ConfigEdit)) != 1 {
		return Command{}, usagef("config needs exactly one of --stages, --show, or --edit")
	}
	return cmd, nil
}

func boolCount(values ...bool) int {
	n := 0
	for _, value := range values {
		if value {
			n++
		}
	}
	return n
}

func parseClean(rest []string) (Command, error) {
	cmd := Command{Name: Clean}
	var positionals []string
	for _, arg := range rest {
		switch {
		case arg == "--dry-run" || arg == "-n":
			cmd.DryRun = true
		case strings.HasPrefix(arg, "-"):
			return Command{}, usagef("clean: unknown flag %s", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) == 0 {
		return cmd, nil
	}
	repo, n, err := issueArg("clean", positionals)
	cmd.Repo, cmd.Issue = repo, n
	return cmd, err
}

func issueArg(name string, rest []string) (task.Repo, int, error) {
	if len(rest) != 1 {
		return "", 0, usagef("%s needs one issue number", name)
	}
	repoPart, numPart, qualified := strings.Cut(rest[0], "#")
	if !qualified {
		numPart = repoPart
	}
	var repo task.Repo
	if qualified && repoPart != "" {
		r, err := task.ParseRepo(repoPart)
		if err != nil {
			return "", 0, usagef("%s: %v", name, err)
		}
		repo = r
	}
	n, err := strconv.Atoi(numPart)
	if err != nil || n <= 0 {
		return "", 0, usagef("%s needs one issue number", name)
	}
	return repo, n, nil
}
