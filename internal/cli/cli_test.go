package cli

import (
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func TestParseStart(t *testing.T) {
	cases := map[string][]string{
		"flags after issue":  {"start", "825", "--agent", "claude", "--context", "./ctx/825.md"},
		"flags before issue": {"start", "--agent=claude", "--context", "./ctx/825.md", "#825"},
	}
	want := Command{Name: Start, Issue: 825, Agent: task.Claude, ContextFile: "./ctx/825.md"}
	for name, argv := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Parse(argv)
			if err != nil || got != want {
				t.Fatalf("Parse(%q) = %+v, %v; want %+v", argv, got, err, want)
			}
		})
	}
}

func TestParseCancel(t *testing.T) {
	got, err := Parse([]string{"cancel", "ridezoomo/armada#351"})
	if want := (Command{Name: Cancel, Repo: "ridezoomo/armada", Issue: 351}); err != nil || got != want {
		t.Fatalf("Parse = %+v, %v; want %+v", got, err, want)
	}
}

func TestParseRejects(t *testing.T) {
	for _, argv := range [][]string{
		{"start", "825"},
		{"start", "825", "--agent", "gpt"},
		{"start", "--agent", "claude"},
		{"start", "825", "--agent"},
		{"start", "825", "--bogus", "x", "--agent", "codex"},
		{"show", "abc"},
		{"show", "1", "2"},
		{"show", "not a repo#1"},
		{"show", "a/b#"},
		{"respond"},
		{"cancel"},
		{"cancel", "1", "2"},
		{"inbox", "extra"},
		{"deploy"},
		{"__run", "825"},
		{"clean", "--force"},
		{"config"},
		{"config", "--stage"},
		{"config", "--stages", "--show"},
		{"start", "825", "--agent", "claude", "--type", "agent"},
		{"clean", "1", "2"},
	} {
		if _, err := Parse(argv); err == nil {
			t.Errorf("Parse(%q) succeeded; want usage error", argv)
		}
	}
}

func TestParseNoArgsOpensTheDashboard(t *testing.T) {
	for _, argv := range [][]string{nil, {"tui"}} {
		if got, err := Parse(argv); err != nil || got != (Command{Name: TUI}) {
			t.Errorf("Parse(%q) = %+v, %v; want the dashboard", argv, got, err)
		}
	}
	for _, argv := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		if got, err := Parse(argv); err != nil || got != (Command{Name: Help}) {
			t.Errorf("Parse(%q) = %+v, %v; want help", argv, got, err)
		}
	}
	if _, err := Parse([]string{"tui", "x"}); err == nil {
		t.Error("Parse(tui x) succeeded; want usage error")
	}
}

func TestParseSimple(t *testing.T) {
	for argv, want := range map[string]Command{
		"inbox":     {Name: Inbox},
		"status":    {Name: Status},
		"clean":     {Name: Clean},
		"version":   {Name: Version},
		"--version": {Name: Version},
	} {
		if got, err := Parse([]string{argv}); err != nil || got != want {
			t.Errorf("Parse(%q) = %+v, %v", argv, got, err)
		}
	}
	if got, _ := Parse([]string{"show", "#12"}); got != (Command{Name: Show, Issue: 12}) {
		t.Errorf("show #12 parsed as %+v", got)
	}
	if got, err := Parse([]string{"start", "825", "--type", "Fix", "--agent", "codex"}); err != nil || got.BranchType != "fix" {
		t.Errorf("start --type parsed as %+v, %v", got, err)
	}
	if got, _ := Parse([]string{"config", "--stages"}); !got.ConfigStages {
		t.Errorf("config --stages parsed as %+v", got)
	}
	if got, _ := Parse([]string{"config", "--show"}); !got.ConfigShow {
		t.Errorf("config --show parsed as %+v", got)
	}
	if got, _ := Parse([]string{"config", "--edit"}); !got.ConfigEdit {
		t.Errorf("config --edit parsed as %+v", got)
	}
	if got, err := Parse([]string{"inbox", "--non-interactive"}); err != nil || !got.NonInteractive {
		t.Errorf("inbox --non-interactive = %+v, %v", got, err)
	}
	if got, _ := Parse([]string{"clean", "--dry-run", "a/b#3"}); got != (Command{Name: Clean, Repo: "a/b", Issue: 3, DryRun: true}) {
		t.Errorf("clean parsed as %+v", got)
	}
	if got, _ := Parse([]string{"__run", "a/b#3"}); got != (Command{Name: Run, Repo: "a/b", Issue: 3}) {
		t.Errorf("__run parsed as %+v", got)
	}
	if got, _ := Parse([]string{"show", "ridezoomo/armada-backend#825"}); got != (Command{Name: Show, Repo: "ridezoomo/armada-backend", Issue: 825}) {
		t.Errorf("qualified show parsed as %+v", got)
	}
	if got, err := Parse([]string{"logs", "a/b#3", "--follow", "--raw"}); err != nil || got != (Command{Name: Logs, Repo: "a/b", Issue: 3, Follow: true, RawLogs: true}) {
		t.Errorf("logs parsed as %+v, %v", got, err)
	}
}
