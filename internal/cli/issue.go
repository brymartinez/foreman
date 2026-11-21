package cli

import (
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

// The local issue commands. None of them starts the dashboard or an agent.
const (
	IssueAdd    Name = "issue add"
	IssueList   Name = "issue list"
	IssueShow   Name = "issue show"
	IssueTriage Name = "issue triage"
	IssueExport Name = "issue export"
	IssueImport Name = "issue import"
	ExportNotes Name = "export markdown"
	issueUsage       = "issue needs add, list, show, triage, export, or import"
)

// IssueInput is what the issue commands read from argv.
type IssueInput struct {
	ID              string
	Title           string
	SourceURL       string
	Repo            task.Repo
	DescriptionFile string // "-" reads stdin
	TriageFile      string
	NotesFile       string
	Tags            []string
	// Output is the export file; empty writes to stdout. Dir is the Markdown export directory.
	Output     string
	Dir        string
	Learnings  bool
	DryRun     bool
	OnConflict string // fail, skip, or replace
	File       string // import input
}

func parseIssue(rest []string) (Command, error) {
	if len(rest) == 0 {
		return Command{}, usagef("%s", issueUsage)
	}
	verb, args := rest[0], rest[1:]
	var name Name
	switch verb {
	case "add", "list", "show", "triage", "export", "import":
		name = Name("issue " + verb)
	default:
		return Command{}, usagef("%s", issueUsage)
	}
	in := IssueInput{OnConflict: "fail"}
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value := func() (string, error) {
			i++
			if i >= len(args) {
				return "", usagef("%s needs a value", arg)
			}
			return args[i], nil
		}
		var err error
		var v string
		switch {
		case name == IssueAdd && arg == "--title":
			in.Title, err = value()
		case name == IssueAdd && arg == "--url":
			in.SourceURL, err = value()
		case name == IssueAdd && arg == "--repo":
			if v, err = value(); err == nil {
				in.Repo, err = task.ParseRepo(v)
			}
		case name == IssueAdd && arg == "--description-file":
			in.DescriptionFile, err = value()
		case name == IssueAdd && arg == "--notes-file":
			in.NotesFile, err = value()
		case name == IssueAdd && arg == "--tag":
			if v, err = value(); err == nil {
				in.Tags = append(in.Tags, v)
			}
		case (name == IssueAdd || name == IssueTriage) && (arg == "--triage-file" || arg == "--file"):
			in.TriageFile, err = value()
		case name == IssueExport && (arg == "-o" || arg == "--output"):
			in.Output, err = value()
		case name == IssueExport && arg == "--learnings":
			in.Learnings = true
		case name == IssueImport && arg == "--dry-run":
			in.DryRun = true
		case name == IssueImport && arg == "--on-conflict":
			if in.OnConflict, err = value(); err == nil && in.OnConflict != "fail" && in.OnConflict != "skip" && in.OnConflict != "replace" {
				err = usagef("--on-conflict is fail, skip, or replace")
			}
		case strings.HasPrefix(arg, "-") && arg != "-":
			err = usagef("%s: unknown flag %s", name, arg)
		default:
			positional = append(positional, arg)
		}
		if err != nil {
			if _, ok := err.(UsageError); !ok {
				err = usagef("%s: %v", name, err)
			}
			return Command{}, err
		}
	}
	want := func(n int, what string) error {
		if len(positional) != n {
			return usagef("%s needs %s", name, what)
		}
		return nil
	}
	switch name {
	case IssueAdd:
		if err := want(0, "no positional argument"); err != nil {
			return Command{}, err
		}
		if strings.TrimSpace(in.Title) == "" {
			return Command{}, usagef("issue add needs --title")
		}
	case IssueList:
		if err := want(0, "no argument"); err != nil {
			return Command{}, err
		}
	case IssueShow:
		if err := want(1, "one issue ID"); err != nil {
			return Command{}, err
		}
		in.ID = positional[0]
	case IssueTriage:
		if err := want(1, "one issue ID"); err != nil {
			return Command{}, err
		}
		if in.TriageFile == "" {
			return Command{}, usagef("issue triage needs --file")
		}
		in.ID = positional[0]
	case IssueExport:
		if len(positional) > 1 {
			return Command{}, usagef("issue export takes at most one issue ID")
		}
		if len(positional) == 1 {
			in.ID = positional[0]
		}
		if in.ID != "" && in.Learnings {
			return Command{}, usagef("--learnings needs a full export; leave out the issue ID")
		}
	case IssueImport:
		if err := want(1, "one file"); err != nil {
			return Command{}, err
		}
		in.File = positional[0]
	}
	if in.ID != "" && !localIssueID.MatchString(in.ID) {
		return Command{}, usagef("%q is not a local issue ID (like L-0A1B2C3D)", in.ID)
	}
	return Command{Name: name, Local: &in}, nil
}

func parseExport(rest []string) (Command, error) {
	if len(rest) != 2 || rest[0] != "markdown" {
		return Command{}, usagef("export needs: markdown DIR")
	}
	return Command{Name: ExportNotes, Local: &IssueInput{Dir: rest[1]}}, nil
}
