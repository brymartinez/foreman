// Package cli parses argv into a Command. It knows nothing about GitHub, storage, or agents.
package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

type Name string

const (
	Help    Name = "help"
	Version Name = "version"
	// TUI is the dashboard, and the command a bare `foreman` runs.
	TUI Name = "tui"
	// Edit opens the config file in the developer's editor.
	Edit Name = "edit"
	// Run is the hidden verb the detached runner process executes (see internal/runner).
	Run Name = "__run"
)

// AutoMode is a launch override for the dashboard's auto mode. AutoSaved keeps the
// saved choice.
type AutoMode string

const (
	AutoSaved AutoMode = ""
	AutoOn    AutoMode = "on"
	AutoOff   AutoMode = "off"
)

type Command struct {
	Name  Name
	Repo  task.Repo // set for __run, given as owner/repo#825
	Issue int
	Auto  AutoMode
}

const Usage = `foreman: delegate GitHub issues to Claude Code or Codex

Usage:
  foreman             Open the dashboard: inbox, delegated work, and logs
  foreman -a, --auto  Open the dashboard with auto mode on for this session
  foreman --no-auto   Open the dashboard with auto mode off for this session
  foreman -c          Edit the configuration file
  foreman -h, --help  Show this help
  foreman --version   Show the version

Press ? in the dashboard for its keys.
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
	case "version", "--version":
		if len(rest) > 0 {
			return Command{}, usagef("%s takes no arguments", name)
		}
		return Command{Name: Version}, nil
	case string(Run):
		repo, n, err := issueArg(name, rest)
		if err == nil && repo == "" {
			err = usagef("%s needs owner/repo#N", name)
		}
		return Command{Name: Run, Repo: repo, Issue: n}, err
	}
	return parseFlags(argv)
}

// parseFlags reads the launch flags. -c opens the editor instead of the dashboard,
// so it takes no auto flag.
func parseFlags(argv []string) (Command, error) {
	cmd := Command{Name: TUI}
	edit := false
	for _, arg := range argv {
		var mode AutoMode
		switch arg {
		case "-a", "--auto":
			mode = AutoOn
		case "--no-auto":
			mode = AutoOff
		case "-c":
			edit = true
			continue
		default:
			if strings.HasPrefix(arg, "-") {
				return Command{}, usagef("unknown flag %s", arg)
			}
			return Command{}, usagef("unknown command %q", arg)
		}
		if cmd.Auto != AutoSaved && cmd.Auto != mode {
			return Command{}, usagef("--auto and --no-auto cannot be used together")
		}
		cmd.Auto = mode
	}
	if edit {
		if cmd.Auto != AutoSaved {
			return Command{}, usagef("-c cannot be used with --auto or --no-auto")
		}
		return Command{Name: Edit}, nil
	}
	return cmd, nil
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
