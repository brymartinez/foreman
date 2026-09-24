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
		{"inbox", "extra"},
		{"deploy"},
		{"__run", "825"},
	} {
		if _, err := Parse(argv); err == nil {
			t.Errorf("Parse(%q) succeeded; want usage error", argv)
		}
	}
}

func TestParseSimple(t *testing.T) {
	for argv, want := range map[string]Command{
		"":       {Name: Help},
		"inbox":  {Name: Inbox},
		"status": {Name: Status},
	} {
		args := []string{}
		if argv != "" {
			args = []string{argv}
		}
		if got, err := Parse(args); err != nil || got != want {
			t.Errorf("Parse(%q) = %+v, %v", argv, got, err)
		}
	}
	if got, _ := Parse([]string{"show", "#12"}); got != (Command{Name: Show, Issue: 12}) {
		t.Errorf("show #12 parsed as %+v", got)
	}
	if got, _ := Parse([]string{"__run", "a/b#3"}); got != (Command{Name: Run, Repo: "a/b", Issue: 3}) {
		t.Errorf("__run parsed as %+v", got)
	}
	if got, _ := Parse([]string{"show", "ridezoomo/armada-backend#825"}); got != (Command{Name: Show, Repo: "ridezoomo/armada-backend", Issue: 825}) {
		t.Errorf("qualified show parsed as %+v", got)
	}
}
