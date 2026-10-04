package cli

import (
	"strings"
	"testing"
)

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

func TestUsageListsOnlyTheLaunchFlags(t *testing.T) {
	for _, flag := range []string{"-a, --auto", "--no-auto", "-c", "-h, --help", "--version"} {
		if !strings.Contains(Usage, flag) {
			t.Errorf("usage is missing %s:\n%s", flag, Usage)
		}
	}
	for _, removed := range []string{"inbox", "status", "show", "start", "respond", "resume", "cancel", "clean", "config --", "logs", "__run"} {
		if strings.Contains(Usage, "foreman "+removed) {
			t.Errorf("usage still lists foreman %s:\n%s", removed, Usage)
		}
	}
}
