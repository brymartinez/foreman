// Package cli parses argv into a Command. It knows nothing about GitHub, storage, or agents.
package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/brymartinez/foreman/internal/config"
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
	// Learn is the hidden verb the detached learner process executes. It takes the
	// same task key as Run.
	Learn Name = "__learn"
	// KnowledgeExport and KnowledgeIngest move a repository's knowledge between
	// machines.
	KnowledgeExport Name = "knowledge export"
	KnowledgeIngest Name = "knowledge ingest"
)

// AutoMode is a launch override for the dashboard's auto mode. AutoSaved keeps the
// saved choice.
type AutoMode string

const (
	AutoSaved AutoMode = ""
	AutoOn    AutoMode = "on"
	AutoOff   AutoMode = "off"
)

// MentionsMode is a launch override for the mentions setting. MentionsConfig keeps
// the config's value.
type MentionsMode string

const (
	MentionsConfig MentionsMode = ""
	MentionsOn     MentionsMode = "on"
	MentionsOff    MentionsMode = "off"
)

type Command struct {
	Name       Name
	Repo       task.Repo // set for __run and __learn, given as owner/repo#825
	Issue      int
	ExternalID string
	Auto       AutoMode
	Mentions   MentionsMode
	// Source is the task's tracker for __run and __learn. For the dashboard it is the
	// -s override: github, linear, or config.SourceAll, and empty to use default_source.
	Source task.Source
	// File is the export's output path (empty for stdout) or the ingest's input.
	File   string
	Rebind bool
	Agent  task.Agent
	DryRun bool
}

const Usage = `foreman: delegate GitHub issues to Claude Code or Codex

Usage:
  foreman [launch overrides]  Open the dashboard: inbox, delegated work, and logs
  foreman -c                  Edit the configuration file
  foreman knowledge export owner/repo [-o FILE]
                              Write the repository's learned knowledge to FILE or stdout
  foreman knowledge ingest FILE [--repo owner/repo [--rebind]] [--agent claude|codex] [--dry-run]
                              Store an export, merging with this machine's knowledge
  foreman -h, --help          Show this help
  foreman --version           Show the version

Dashboard launch overrides (this launch only; nothing is saved):
  -s, --source github|linear|all
                      Show only this tracker's issues in INBOX and its tasks in
                      DELEGATED WORK, and auto mode starts only from them; all
                      shows both. Overrides default_source.
  -a, --auto          Turn auto mode on. Overrides the choice saved with the a key.
  --no-auto           Turn auto mode off. Overrides the choice saved with the a key.
  --mentions          Show items found only because they mention you.
                      Overrides mentions.
  --no-mentions       Hide those items. Overrides mentions.

Operation inputs:
  knowledge export:
    -o, --output FILE     Write to FILE instead of stdout
  knowledge ingest:
    --repo owner/repo     Store under this repository; it must match the
                          export unless --rebind is given
    --rebind              Store the export under a different --repo
    --agent claude|codex  Agent that merges the knowledge (default auto.agent)
    --dry-run             Print the merged result without saving it

Precedence:
  Source and mentions: launch flag, then config.yaml (default_source, mentions),
  then the built-in default (github; mention-only items shown).
  Auto mode: launch flag, then the choice saved with the a key in the dashboard.
  With no saved choice it is off. config.yaml has no auto on/off setting.
  The built-in defaults apply to keys missing from config.yaml. The shipped
  defaults/config.yaml that make setup installs sets some different values,
  such as mentions: false, models, and budgets.

Settings without launch flags:
  config.yaml: refresh, repository filters and linear_projects, directories,
  agent commands and models, budgets, review policy, learning, notifications,
  cleanup, auto.agent, and auto.max_concurrent. Unknown keys are an error.
  routing.yaml: selector, profiles, and rules.

Press ? in the dashboard for its keys.
`

type UsageError struct{ msg string }

func (e UsageError) Error() string { return e.msg }

func usagef(format string, args ...any) error { return UsageError{fmt.Sprintf(format, args...)} }

func Parse(argv []string) (Command, error) {
	if len(argv) == 0 {
		return parseFlags(nil)
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
	case string(Run), string(Learn):
		verb := Name(name)
		if len(rest) == 1 {
			repoPart, id, found := strings.Cut(rest[0], "#linear:")
			if found {
				repo, err := task.ParseRepo(repoPart)
				if err != nil || id == "" || strings.ContainsAny(id, "/#:") {
					return Command{}, usagef("%s needs owner/repo#linear:ID", name)
				}
				return Command{Name: verb, Repo: repo, Source: task.Linear, ExternalID: id}, nil
			}
		}
		repo, n, err := issueArg(name, rest)
		if err == nil && repo == "" {
			err = usagef("%s needs owner/repo#N", name)
		}
		return Command{Name: verb, Repo: repo, Issue: n}, err
	case "knowledge":
		return parseKnowledge(rest)
	}
	return parseFlags(argv)
}

// parseKnowledge reads `knowledge export REPO [-o FILE]` and `knowledge ingest FILE`
// with its flags. Flags may come before or after the positional argument.
func parseKnowledge(argv []string) (Command, error) {
	if len(argv) == 0 || (argv[0] != "export" && argv[0] != "ingest") {
		return Command{}, usagef("knowledge needs export or ingest")
	}
	verb, rest := argv[0], argv[1:]
	cmd := Command{Name: KnowledgeExport}
	if verb == "ingest" {
		cmd.Name = KnowledgeIngest
	}
	var positional []string
	value := func(i *int, flag string) (string, error) {
		*i++
		if *i >= len(rest) {
			return "", usagef("%s needs a value", flag)
		}
		return rest[*i], nil
	}
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		var err error
		switch {
		case verb == "export" && (arg == "-o" || arg == "--output"):
			cmd.File, err = value(&i, arg)
		case verb == "ingest" && arg == "--repo":
			var v string
			if v, err = value(&i, arg); err == nil {
				cmd.Repo, err = task.ParseRepo(v)
			}
		case verb == "ingest" && arg == "--rebind":
			cmd.Rebind = true
		case verb == "ingest" && arg == "--dry-run":
			cmd.DryRun = true
		case verb == "ingest" && arg == "--agent":
			var v string
			if v, err = value(&i, arg); err == nil {
				cmd.Agent, err = task.ParseAgent(v)
			}
		case strings.HasPrefix(arg, "-") && arg != "-":
			err = usagef("knowledge %s: unknown flag %s", verb, arg)
		default:
			positional = append(positional, arg)
		}
		if err != nil {
			if _, ok := err.(UsageError); !ok {
				err = usagef("knowledge %s: %v", verb, err)
			}
			return Command{}, err
		}
	}
	if len(positional) != 1 {
		if verb == "export" {
			return Command{}, usagef("knowledge export needs one owner/repo")
		}
		return Command{}, usagef("knowledge ingest needs one file")
	}
	if verb == "export" {
		repo, err := task.ParseRepo(positional[0])
		if err != nil {
			return Command{}, usagef("knowledge export: %v", err)
		}
		cmd.Repo = repo
		return cmd, nil
	}
	if cmd.Rebind && cmd.Repo == "" {
		return Command{}, usagef("--rebind needs --repo")
	}
	cmd.File = positional[0]
	return cmd, nil
}

// parseFlags reads the launch flags. -c opens the editor instead of the dashboard,
// so it takes no launch flag. Each flag records only what was typed; main resolves
// it against config.
func parseFlags(argv []string) (Command, error) {
	cmd := Command{Name: TUI}
	edit := false
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		var auto AutoMode
		var mentions MentionsMode
		switch arg {
		case "-s", "--source":
			i++
			if i >= len(argv) {
				return Command{}, usagef("%s needs github, linear, or all", arg)
			}
			chosen := task.Source(argv[i])
			if err := config.ValidSource(chosen); err != nil {
				return Command{}, usagef("%v", err)
			}
			if cmd.Source != "" && cmd.Source != chosen {
				return Command{}, usagef("conflicting issue sources")
			}
			cmd.Source = chosen
			continue
		case "-a", "--auto":
			auto = AutoOn
		case "--no-auto":
			auto = AutoOff
		case "--mentions":
			mentions = MentionsOn
		case "--no-mentions":
			mentions = MentionsOff
		case "-c":
			edit = true
			continue
		default:
			if strings.HasPrefix(arg, "-") {
				return Command{}, usagef("unknown flag %s", arg)
			}
			return Command{}, usagef("unknown command %q", arg)
		}
		if auto != AutoSaved {
			if cmd.Auto != AutoSaved && cmd.Auto != auto {
				return Command{}, usagef("--auto and --no-auto cannot be used together")
			}
			cmd.Auto = auto
		}
		if mentions != MentionsConfig {
			if cmd.Mentions != MentionsConfig && cmd.Mentions != mentions {
				return Command{}, usagef("--mentions and --no-mentions cannot be used together")
			}
			cmd.Mentions = mentions
		}
	}
	if edit {
		if cmd.Auto != AutoSaved || cmd.Mentions != MentionsConfig || cmd.Source != "" {
			return Command{}, usagef("-c cannot be used with launch flags")
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
