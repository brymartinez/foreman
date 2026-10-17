package cli

import (
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func TestSourceFlagIsAOneLaunchOverride(t *testing.T) {
	for name, tc := range map[string]struct {
		args   []string
		source task.Source
	}{
		"no flag leaves the source to config":    {nil, ""},
		"an auto flag leaves the source alone":   {[]string{"-a"}, ""},
		"-s github selects GitHub":               {[]string{"-s", "github"}, task.GitHub},
		"-s linear selects Linear":               {[]string{"-s", "linear"}, task.Linear},
		"-s all selects both trackers":           {[]string{"-s", "all"}, "all"},
		"--source works with other flags":        {[]string{"--source", "linear", "-a"}, task.Linear},
		"a repeated identical source is allowed": {[]string{"-s", "all", "--source", "all"}, "all"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Parse(tc.args)
			if err != nil || got.Name != TUI || got.Source != tc.source {
				t.Fatalf("Parse(%q) = %+v, %v; want source %q", tc.args, got, err, tc.source)
			}
		})
	}
}

func TestSourceFlagRejectsMissingUnknownAndConflictingValues(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"missing value":      {[]string{"-s"}, "github, linear, or all"},
		"unknown value":      {[]string{"-s", "other"}, "github, linear, or all"},
		"conflicting values": {[]string{"-s", "github", "-s", "all"}, "conflicting"},
		"with the editor":    {[]string{"-c", "-s", "linear"}, "-c"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse(%q) err = %v; want a usage error containing %q", tc.args, err, tc.want)
			}
		})
	}
}

func TestMentionsFlagsOverrideConfigForOneLaunch(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want MentionsMode
	}{
		"no flag keeps the config":            {nil, MentionsConfig},
		"--mentions shows mention-only items": {[]string{"--mentions"}, MentionsOn},
		"--no-mentions hides them":            {[]string{"--no-mentions"}, MentionsOff},
		"a repeated flag is allowed":          {[]string{"--no-mentions", "-s", "all", "--no-mentions"}, MentionsOff},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Parse(tc.args)
			if err != nil || got.Name != TUI || got.Mentions != tc.want {
				t.Fatalf("Parse(%q) = %+v, %v; want mentions %q", tc.args, got, err, tc.want)
			}
		})
	}
	for _, args := range [][]string{
		{"--mentions", "--no-mentions"},
		{"--no-mentions", "--mentions"},
		{"-c", "--mentions"},
		{"--no-mentions", "-c"},
	} {
		if _, err := Parse(args); err == nil {
			t.Errorf("Parse(%q) succeeded; want usage error", args)
		}
	}
}

func TestParseOpensTheDashboardWithTheGivenAutoMode(t *testing.T) {
	for name, tc := range map[string]struct {
		argv []string
		want Command
	}{
		"no arguments keep the saved choice": {nil, Command{Name: TUI}},
		"-a turns auto mode on":              {[]string{"-a"}, Command{Name: TUI, Auto: AutoOn}},
		"--auto turns auto mode on":          {[]string{"--auto"}, Command{Name: TUI, Auto: AutoOn}},
		"--no-auto turns auto mode off":      {[]string{"--no-auto"}, Command{Name: TUI, Auto: AutoOff}},
		"a repeated auto flag is accepted":   {[]string{"-a", "--auto"}, Command{Name: TUI, Auto: AutoOn}},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := Parse(tc.argv); err != nil || got != tc.want {
				t.Fatalf("Parse(%q) = %+v, %v; want %+v", tc.argv, got, err, tc.want)
			}
		})
	}
}

func TestParseCOpensTheConfigEditor(t *testing.T) {
	if got, err := Parse([]string{"-c"}); err != nil || got != (Command{Name: Edit}) {
		t.Fatalf("Parse(-c) = %+v, %v; want the config editor", got, err)
	}
}

func TestParseHelpAndVersionFlags(t *testing.T) {
	for argv, want := range map[string]Name{
		"help": Help, "-h": Help, "--help": Help,
		"version": Version, "--version": Version,
	} {
		if got, err := Parse([]string{argv}); err != nil || got != (Command{Name: want}) {
			t.Errorf("Parse(%q) = %+v, %v; want %s", argv, got, err, want)
		}
	}
}

func TestParseRejectsRemovedCommandsAndConflictingFlags(t *testing.T) {
	for _, argv := range [][]string{
		{"inbox"}, {"status"}, {"show", "1"}, {"start", "1", "--agent", "claude"},
		{"respond", "1"}, {"resume", "1"}, {"cancel", "1"}, {"clean"},
		{"config", "--edit"}, {"logs", "1"}, {"tui"}, {"deploy"}, {"--bogus"},
		{"-a", "--no-auto"},
		{"--no-auto", "--auto"},
		{"-c", "-a"},
		{"--no-auto", "-c"},
		{"-a", "extra"},
		{"-c", "extra"},
		{"--version", "extra"},
		{"__run", "825"},
		{"__run"},
		{"__run", "a/b#1", "a/b#2"},
		{"__run", "not a repo#1"},
		{"__learn", "825"},
		{"__learn"},
		{"__learn", "not a repo#1"},
	} {
		if _, err := Parse(argv); err == nil {
			t.Errorf("Parse(%q) succeeded; want usage error", argv)
		}
	}
}

func TestParseRunNeedsAQualifiedIssue(t *testing.T) {
	got, err := Parse([]string{"__run", "a/b#3"})
	if want := (Command{Name: Run, Repo: "a/b", Issue: 3}); err != nil || got != want {
		t.Fatalf("Parse(__run a/b#3) = %+v, %v; want %+v", got, err, want)
	}
}

func TestParseRunKeepsLinearIssueIdentity(t *testing.T) {
	got, err := Parse([]string{"__run", "a/b#linear:11111111-1111-4111-8111-111111111111"})
	want := Command{Name: Run, Repo: "a/b", Source: task.Linear, ExternalID: "11111111-1111-4111-8111-111111111111"}
	if err != nil || got != want {
		t.Fatalf("Parse Linear runner key = %+v, %v; want %+v", got, err, want)
	}
}

func TestParseLearnTakesTheSameTaskKeyAsRun(t *testing.T) {
	got, err := Parse([]string{"__learn", "a/b#3"})
	if want := (Command{Name: Learn, Repo: "a/b", Issue: 3}); err != nil || got != want {
		t.Fatalf("Parse(__learn a/b#3) = %+v, %v; want %+v", got, err, want)
	}
	got, err = Parse([]string{"__learn", "a/b#linear:ENG-1"})
	if want := (Command{Name: Learn, Repo: "a/b", Source: task.Linear, ExternalID: "ENG-1"}); err != nil || got != want {
		t.Fatalf("Parse Linear learn key = %+v, %v; want %+v", got, err, want)
	}
}

func TestUsageListsOnlyTheLaunchFlags(t *testing.T) {
	for _, flag := range []string{"-a, --auto", "--no-auto", "-c", "-h, --help", "--version"} {
		if !strings.Contains(Usage, flag) {
			t.Errorf("usage is missing %s:\n%s", flag, Usage)
		}
	}
	for _, removed := range []string{"inbox", "status", "show", "start", "respond", "resume", "cancel", "clean", "config --", "logs", "__run", "__learn"} {
		if strings.Contains(Usage, "foreman "+removed) {
			t.Errorf("usage still lists foreman %s:\n%s", removed, Usage)
		}
	}
}

func TestParseKnowledgeExport(t *testing.T) {
	for name, tc := range map[string]struct {
		argv []string
		want Command
	}{
		"to stdout": {[]string{"knowledge", "export", "a/b"}, Command{Name: KnowledgeExport, Repo: "a/b"}},
		"to a file": {[]string{"knowledge", "export", "a/b", "-o", "k.md"}, Command{Name: KnowledgeExport, Repo: "a/b", File: "k.md"}},
		"long flag": {[]string{"knowledge", "export", "--output", "k.md", "a/b"}, Command{Name: KnowledgeExport, Repo: "a/b", File: "k.md"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := Parse(tc.argv); err != nil || got != tc.want {
				t.Fatalf("Parse(%q) = %+v, %v; want %+v", tc.argv, got, err, tc.want)
			}
		})
	}
}

func TestParseKnowledgeIngest(t *testing.T) {
	for name, tc := range map[string]struct {
		argv []string
		want Command
	}{
		"export's repository": {[]string{"knowledge", "ingest", "k.md"}, Command{Name: KnowledgeIngest, File: "k.md"}},
		"every flag": {[]string{"knowledge", "ingest", "--repo", "c/d", "--rebind", "--agent", "codex", "--dry-run", "k.md"},
			Command{Name: KnowledgeIngest, File: "k.md", Repo: "c/d", Rebind: true, Agent: task.Codex, DryRun: true}},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := Parse(tc.argv); err != nil || got != tc.want {
				t.Fatalf("Parse(%q) = %+v, %v; want %+v", tc.argv, got, err, tc.want)
			}
		})
	}
}

func TestParseRejectsInvalidKnowledgeCommands(t *testing.T) {
	for _, argv := range [][]string{
		{"knowledge"},
		{"knowledge", "sync"},
		{"knowledge", "export"},
		{"knowledge", "export", "not-a-repo"},
		{"knowledge", "export", "a/b", "c/d"},
		{"knowledge", "export", "a/b", "-o"},
		{"knowledge", "export", "a/b", "--dry-run"},
		{"knowledge", "ingest"},
		{"knowledge", "ingest", "a.md", "b.md"},
		{"knowledge", "ingest", "k.md", "--agent", "gpt"},
		{"knowledge", "ingest", "k.md", "--repo", "bad"},
		{"knowledge", "ingest", "k.md", "--rebind"},
		{"knowledge", "ingest", "k.md", "-o", "x.md"},
	} {
		if _, err := Parse(argv); err == nil {
			t.Errorf("Parse(%q) succeeded; want usage error", argv)
		}
	}
}

func TestUsageListsTheKnowledgeCommands(t *testing.T) {
	for _, want := range []string{"foreman knowledge export owner/repo [-o FILE]", "foreman knowledge ingest FILE", "--rebind", "--agent claude|codex", "--dry-run"} {
		if !strings.Contains(Usage, want) {
			t.Errorf("usage lacks %q:\n%s", want, Usage)
		}
	}
}
