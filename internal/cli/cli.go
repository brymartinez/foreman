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
)

type Command struct {
	Name        Name
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
		n, err := issueArg(name, rest)
		return Command{Name: Name(name), Issue: n}, err
	case "start":
		return parseStart(rest)
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
	n, err := issueArg("start", positionals)
	if err != nil {
		return Command{}, err
	}
	a, err := task.ParseAgent(*agentFlag)
	if err != nil {
		return Command{}, usagef("start needs --agent claude|codex")
	}
	return Command{Name: Start, Issue: n, Agent: a, ContextFile: *contextFlag}, nil
}

func issueArg(name string, rest []string) (int, error) {
	if len(rest) != 1 {
		return 0, usagef("%s needs one issue number", name)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(rest[0], "#"))
	if err != nil || n <= 0 {
		return 0, usagef("%s needs one issue number", name)
	}
	return n, nil
}
