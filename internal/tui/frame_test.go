package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

var escapes = regexp.MustCompile("\x1b\\[[0-9;]*m")

func plain(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = escapes.ReplaceAllString(l, "")
	}
	return out
}

func screen(m Model) string { return strings.Join(plain(Frame(m)), "\n") }

func TestFrameFillsTheScreen(t *testing.T) {
	for _, size := range [][2]int{{minWidth, minHeight}, {100, 30}, {200, 60}} {
		m := model()
		m.Width, m.Height = size[0], size[1]
		m.Log = []string{"a log line"}
		lines := Frame(m)
		if len(lines) != m.Height {
			t.Errorf("%dx%d: %d lines", m.Width, m.Height, len(lines))
		}
		for i, l := range plain(lines) {
			if n := textWidth(l); n > m.Width {
				t.Errorf("%dx%d: line %d is %d columns: %q", m.Width, m.Height, i, n, l)
			}
		}
	}
}

func TestFrameShowsThreePanes(t *testing.T) {
	m := model()
	m.Log = []string{"2026-09-29T10:00:00Z claude Bash go test"}
	got := screen(m)
	for _, want := range []string{"INBOX", "DELEGATED WORK", "LOG", "a/b#1", "First", "a/b#10", "claude Bash go test"} {
		if !strings.Contains(got, want) {
			t.Errorf("frame is missing %q:\n%s", want, got)
		}
	}
}

func TestFrameMarksTheFocusedPane(t *testing.T) {
	m := model()
	inboxFocused := Frame(m)
	m.Focus = TaskPane
	taskFocused := Frame(m)
	if inboxFocused[1] == taskFocused[1] {
		t.Fatalf("the pane headings do not change with focus:\n%q", inboxFocused[1])
	}
	if !strings.Contains(inboxFocused[1], focusMark) || strings.Count(inboxFocused[1], focusMark) != 1 {
		t.Errorf("want exactly one focused heading: %q", inboxFocused[1])
	}
	if !strings.Contains(taskFocused[1], focusMark) || strings.Count(taskFocused[1], focusMark) != 1 {
		t.Errorf("want exactly one focused heading: %q", taskFocused[1])
	}
}

func TestFrameEmptyPanes(t *testing.T) {
	m := Model{Width: 100, Height: 30}
	got := screen(m)
	for _, want := range []string{"Nothing to delegate", "No delegated work", "no task selected"} {
		if !strings.Contains(got, want) {
			t.Errorf("frame is missing %q:\n%s", want, got)
		}
	}
}

func TestFrameShowsALogReadError(t *testing.T) {
	m := model()
	m.LogErr = "open /logs/b.log: permission denied"
	if got := screen(m); !strings.Contains(got, "permission denied") {
		t.Errorf("frame hides the log error:\n%s", got)
	}
}

func TestFrameKeepsTheStateAtTheSmallestSupportedWidth(t *testing.T) {
	// status pads the identity to 25 columns, which is wider than the 60-column
	// layout's task pane. The state must wrap rather than be truncated away.
	m := model()
	m.Width, m.Height = minWidth, 30
	got := screen(m)
	for _, want := range []string{"a/b#10", "Claude · WORKING"} {
		if !strings.Contains(got, want) {
			t.Errorf("a %d-column frame is missing %q:\n%s", minWidth, want, got)
		}
	}
	// A pane wide enough for the padded row keeps status's own layout.
	m.Width = 200
	if !strings.Contains(screen(m), "a/b#10                    Claude  WORKING") {
		t.Errorf("a wide frame lost status's padded row:\n%s", screen(m))
	}
}

func TestFrameShowsPRAtTheSmallestSupportedSize(t *testing.T) {
	m := model()
	m.Width, m.Height = minWidth, minHeight
	m.Focus = TaskPane
	m.Tasks[0].Delegation.PR = 942
	m.Tasks[0].CI = &task.CIStatus{State: task.CIPassing}
	m.Tasks[0].Reviews = []task.Review{{Round: 1, Clean: true}}
	got := screen(m)
	for _, want := range []string{"a/b#10", "Claude · WORKING", "Task", "PR #942", "Codex", "CI ✓", "review ✓"} {
		if !strings.Contains(got, want) {
			t.Errorf("a %dx%d frame is missing %q:\n%s", m.Width, m.Height, want, got)
		}
	}
}

func TestFrameDoesNotRenderTerminalControlsFromTaskData(t *testing.T) {
	m := model()
	m.Inbox[0].Item.Title = "hello\x1b[2Jworld"
	m.Tasks[0].Title = "task\rreplaced"
	m.Log = []string{"agent\x1b[?1049l output"}
	m.Message = "error\x1b]52;c;bad\a"
	got := strings.Join(Frame(m), "\n")
	for _, control := range []string{"\x1b[2J", "\r", "\x1b[?1049l", "\x1b]52", "\a"} {
		if strings.Contains(got, control) {
			t.Errorf("frame retained terminal control %q", control)
		}
	}
}

func TestFrameTooSmall(t *testing.T) {
	m := model()
	m.Width, m.Height = 40, 8
	lines := Frame(m)
	if len(lines) != 8 {
		t.Fatalf("%d lines", len(lines))
	}
	if !strings.Contains(strings.Join(plain(lines), "\n"), "60x12") {
		t.Errorf("small frame does not name the minimum size:\n%s", strings.Join(lines, "\n"))
	}
}

func TestFrameFooterFollowsTheMode(t *testing.T) {
	for _, tc := range []struct {
		mode Mode
		want string
	}{
		{Browsing, "enter delegate"},
		{ChoosingAgent, "[1] Claude"},
		{TypingContext, "Context file"},
		{ConfirmingCancel, "[y/n]"},
	} {
		m := model()
		m.Focus = TaskPane
		m.Mode = tc.mode
		if got := plain(Frame(m)); !strings.Contains(got[len(got)-1], tc.want) {
			t.Errorf("mode %v footer = %q; want %q", tc.mode, got[len(got)-1], tc.want)
		}
	}
}

func TestFrameShowsTheTypedContextFile(t *testing.T) {
	m := model()
	m.Mode, m.Input = TypingContext, "./ctx/1.md"
	got := plain(Frame(m))
	if !strings.Contains(got[len(got)-1], "./ctx/1.md") {
		t.Errorf("footer = %q", got[len(got)-1])
	}
}

func TestFrameHeaderReportsRefreshAndWarnings(t *testing.T) {
	m := model()
	m.Refreshing = true
	if got := plain(Frame(m))[0]; !strings.Contains(got, "refreshing") {
		t.Errorf("header = %q", got)
	}
	m.Refreshing = false
	m.Warnings = []error{errString("refresh a/b#1 PR: gh failed")}
	if got := plain(Frame(m))[0]; !strings.Contains(got, "1 warning") {
		t.Errorf("header = %q", got)
	}
}

func TestFrameMessageLine(t *testing.T) {
	m := model()
	m.Message = "Started a/b#1"
	got := plain(Frame(m))
	if !strings.Contains(got[len(got)-2], "Started a/b#1") {
		t.Errorf("message line = %q", got[len(got)-2])
	}
}

func TestFrameTruncatesLongText(t *testing.T) {
	m := Model{Width: 60, Height: 12}
	m.SetInbox([]app.InboxEntry{entry("acme/a-very-long-repository-name", 123456, strings.Repeat("title ", 40))})
	m.SetTasks([]*task.Record{record(10, task.Working)})
	m.Tasks[0].Title = strings.Repeat("long ", 40)
	m.Log = []string{strings.Repeat("x", 500)}
	for i, l := range plain(Frame(m)) {
		if n := textWidth(l); n > 60 {
			t.Fatalf("line %d is %d columns", i, n)
		}
	}
}

func TestFrameKeepsWideTextInsideItsPane(t *testing.T) {
	// Counting runes made a CJK title twice as wide as the pane it was drawn in, so
	// its tail ran over the pane beside it.
	m := Model{Width: minWidth, Height: 30}
	m.SetInbox([]app.InboxEntry{entry("acme/widgets", 7, strings.Repeat("配車の再試行", 8))})
	m.SetTasks([]*task.Record{record(10, task.Working)})
	m.Tasks[0].Title = strings.Repeat("プロバイダ", 8)
	m.Log = []string{strings.Repeat("ログ行", 40)}
	for i, l := range plain(Frame(m)) {
		if n := textWidth(l); n > minWidth {
			t.Fatalf("line %d is %d columns: %q", i, n, l)
		}
	}
	// Every inbox line is exactly the pane's column count, so the task pane beside it
	// starts where the layout puts it.
	lay, _ := layoutFor(m.Width, m.Height)
	for i, l := range plain(paneLines(inboxPane(m), lay.leftWidth, lay.topHeight, true, 0)) {
		if n := textWidth(l); n != lay.leftWidth {
			t.Errorf("inbox line %d is %d columns; want %d: %q", i, n, lay.leftWidth, l)
		}
	}
}

func TestFrameShowsAWaitingQuestionWithItsPRAtTheSmallestSupportedSize(t *testing.T) {
	// The pane floor is what keeps a task's PR, its badges, and its question on
	// screen, so the wrapped PR row must not push the question out of the pane.
	rec := record(10, task.NeedsDecision)
	rec.Delegation.PR = 912
	rec.CI = &task.CIStatus{State: task.CIFailing, Failing: []task.CheckRun{{Name: "test"}, {Name: "lint"}}}
	rec.Decisions = []task.Decision{{Question: task.Question{Issue: "Retry 4xx?"}}}
	m := Model{Width: minWidth, Height: minHeight, Focus: TaskPane}
	m.SetTasks([]*task.Record{rec})
	got := screen(m)
	for _, want := range []string{"a/b#10", "NEEDS_DECISION", "PR #912", "Retry 4xx?"} {
		if !strings.Contains(got, want) {
			t.Errorf("a %dx%d frame is missing %q:\n%s", minWidth, minHeight, want, got)
		}
	}
}

func TestTaskPaneRowsFitTheLayoutFloor(t *testing.T) {
	// layoutFor sizes the pane's body from taskRowLines, and the pane scrolls by
	// whole rows, so a taller row would put its own last line out of reach.
	rec := record(10, task.NeedsDecision)
	rec.Delegation.PR = 912
	rec.CI = &task.CIStatus{State: task.CIFailing, Failing: []task.CheckRun{{Name: "integration-tests"}, {Name: "lint"}}}
	rec.Reviews = []task.Review{{Round: 2, Findings: []task.Finding{{}, {}, {}}}}
	rec.Decisions = []task.Decision{{Question: task.Question{Issue: "Should retries apply to 4xx responses too?"}}}
	m := Model{Width: minWidth, Height: minHeight}
	m.SetTasks([]*task.Record{rec, record(11, task.Working)})
	for _, width := range []int{20, 32, 60, 200} {
		for i, r := range taskPane(m, width).rows {
			if len(r.lines) > taskRowLines {
				t.Errorf("task %d at %d columns draws %d lines; taskRowLines is %d: %q", i, width, len(r.lines), taskRowLines, r.lines)
			}
		}
	}
}

func TestFrameScrollsTheLog(t *testing.T) {
	m := model()
	for i := range 100 {
		m.Log = append(m.Log, "line "+string(rune('a'+i%26))+strings.Repeat("", i))
	}
	m.Log[99] = "the newest line"
	m.Log[0] = "the oldest line"
	if !strings.Contains(screen(m), "the newest line") {
		t.Error("an unscrolled log does not show its tail")
	}
	m.LogOffset = 99
	if !strings.Contains(screen(m), "the oldest line") {
		t.Error("a scrolled log does not reach its start")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
