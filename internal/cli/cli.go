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
	// Run is the hidden verb the detached runner process executes (see internal/runner).
	Run Name = "__run"
)

type Command struct {
	Name        Name
	Repo        task.Repo // set when the issue is given as owner/repo#825
	Issue       int
	Agent       task.Agent
	ContextFile string
}

const Usage = `foreman: delegate GitHub issues to Claude Code or Codex

Usage:
  foreman inbox                                   What changed / needs me?
  foreman status                                  Delegated work and its state
  foreman show <issue>                            Durable state for one task
  foreman start <issue> --agent claude|codex [--context path.md]
  foreman respond <issue>                         Answer a NEEDS_DECISION task

<issue> is 825, #825, or owner/repo#825 when the number exists in several repos.
`

type UsageError struct{ msg string }

func (e UsageError) Error() string { return e.msg }

func usagef(format string, args ...any) error { return UsageError{fmt.Sprintf(format, args...)} }

func Parse(argv []string) (Command, error) {
	if len(argv) == 0 {
		return Command{Name: Help}, nil
	}
	name, rest := argv[0], argv[1:]
	switch name {
	case "help", "-h", "--help":
		return Command{Name: Help}, nil
	case "inbox", "status":
		if len(rest) > 0 {
			return Command{}, usagef("%s takes no arguments", name)
		}
		return Command{Name: Name(name)}, nil
	case "show", "respond":
		repo, n, err := issueArg(name, rest)
		return Command{Name: Name(name), Repo: repo, Issue: n}, err
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

func parseStart(rest []string) (Command, error) {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	agentFlag := fs.String("agent", "", "")
	contextFlag := fs.String("context", "", "")
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
	return Command{Name: Start, Repo: repo, Issue: n, Agent: a, ContextFile: *contextFlag}, nil
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
