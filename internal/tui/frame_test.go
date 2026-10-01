package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/render"
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
	m.Focus = TaskPane
	m.Key(Key{Kind: KeyEnter})
	got := screen(m)
	for _, want := range []string{"INBOX", "DELEGATED WORK", "LOG", "a/b#1", "First", "a/b#10", "claude Bash go test"} {
		if !strings.Contains(got, want) {
			t.Errorf("frame is missing %q:\n%s", want, got)
		}
	}
}

func TestFrameHidesLogUntilTaskEnterAndShowsBranch(t *testing.T) {
	m := model()
	m.Tasks[0].Delegation.Branch = "feat/10-task"
	m.Log = []string{"secret log line"}
	if got := screen(m); strings.Contains(got, "LOG ·") || strings.Contains(got, "secret log line") {
		t.Fatalf("log visible before task enter:\n%s", got)
	}
	if got := screen(m); !strings.Contains(got, "branch feat/10-task") {
		t.Fatalf("delegated task has no branch:\n%s", got)
	}
	m.Focus = TaskPane
	m.Key(Key{Kind: KeyEnter})
	if got := screen(m); !strings.Contains(got, "secret log line") {
		t.Fatalf("enter did not reveal the selected log:\n%s", got)
	}
	m.Key(Key{Kind: KeyEscape})
	if got := screen(m); strings.Contains(got, "LOG ·") {
		t.Fatalf("escape did not close the log:\n%s", got)
	}
}

func TestFrameColorsAndDividesThePanes(t *testing.T) {
	m := model()
	lines := Frame(m)
	if !strings.Contains(lines[0], cyan) || !strings.Contains(lines[1], colored("│", cyan)) || !strings.Contains(lines[2], colored("WORKING", green)) {
		t.Fatalf("frame lacks color or a pane divider:\n%s", strings.Join(lines[:3], "\n"))
	}
	m.Message = "! GitHub failed"
	if !strings.Contains(Frame(m)[m.Height-2], red) {
		t.Fatal("an error message has no error color")
	}
}

func TestRowTextKeepsTheTerminalForeground(t *testing.T) {
	m := model()
	m.Tasks[0].Delegation.Branch = "feat/10-task"
	// The second task is not under the cursor, so its row is drawn without selection.
	row := taskRow(t, Frame(m), "a/b#11")
	if !strings.Contains(row, colored("REVIEWING", green)) {
		t.Fatalf("the state is not the row's status cue: %q", row)
	}
	if rest := strings.ReplaceAll(row, colored("REVIEWING", green), "REVIEWING"); escapes.MatchString(rest) {
		t.Errorf("row text carries color beyond the state: %q", rest)
	}
	// In a narrow pane the state wraps onto its own line, and the cue follows it.
	m.Width = minWidth
	if row := taskRow(t, Frame(m), "Claude · REVIEWING"); !strings.Contains(row, colored("REVIEWING", green)) {
		t.Errorf("the wrapped state lost its color: %q", row)
	}
}

func TestSelectedRowIsReadableOnAnyTheme(t *testing.T) {
	m := model()
	m.Focus = TaskPane
	row := taskRow(t, Frame(m), "a/b#10") // the cursor starts on the newest task
	if !strings.HasPrefix(row, reverseOn) || !strings.HasSuffix(row, attrsReset) {
		t.Fatalf("the selected row is not drawn in reverse video: %q", row)
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(row, reverseOn), attrsReset)
	if escapes.MatchString(inner) {
		// A foreground color under reverse video paints the text in the terminal's
		// background color, which is what light themes cannot read.
		t.Errorf("the selected row holds color inside its reverse video: %q", inner)
	}
}

// taskRow is the frame line that holds identity, from the task pane.
func taskRow(t *testing.T, lines []string, identity string) string {
	t.Helper()
	for _, l := range lines {
		_, right, found := strings.Cut(l, colored("│", cyan))
		if found && strings.Contains(escapes.ReplaceAllString(right, ""), identity) {
			return right
		}
	}
	t.Fatalf("no task row for %s in:\n%s", identity, strings.Join(plain(lines), "\n"))
	return ""
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
	m.Key(Key{Kind: KeyEnter})
	logFocused := Frame(m)
	lay, _ := layoutFor(m.Width, m.Height, true)
	logHeading := logFocused[lay.topHeight+2]
	if strings.Contains(logHeading, focusMark) || strings.Count(strings.Join(logFocused, ""), focusMark) != 1 {
		t.Errorf("log is not a focusable pane: %q", logHeading)
	}
	m.Width = minWidth
	if got := plain(Frame(m))[m.Height-1]; !strings.Contains(got, "j/k task") || !strings.Contains(got, "u/d log") {
		t.Errorf("the narrow log footer hides its scroll keys: %q", got)
	}
}

func TestFrameEmptyPanes(t *testing.T) {
	m := Model{Width: 100, Height: 30}
	got := screen(m)
	for _, want := range []string{"Nothing to delegate", "No delegated work"} {
		if !strings.Contains(got, want) {
			t.Errorf("frame is missing %q:\n%s", want, got)
		}
	}
}

func TestFrameShowsALogReadError(t *testing.T) {
	m := model()
	m.LogOpen = true
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
		{Browsing, "enter select"},
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
	lay, _ := layoutFor(m.Width, m.Height, true)
	for i, l := range plain(paneLines(inboxPane(m), lay.leftWidth, lay.topHeight, true, 0)) {
		if n := textWidth(l); n != lay.leftWidth {
			t.Errorf("inbox line %d is %d columns; want %d: %q", i, n, lay.leftWidth, l)
		}
	}
}

// waiting is the task the reviewer's smallest case is about: a question to answer, a
// PR, failing CI, and a review round with findings.
func waiting() *task.Record {
	rec := record(10, task.NeedsDecision)
	rec.Delegation.PR = 912
	rec.CI = &task.CIStatus{State: task.CIFailing, Failing: []task.CheckRun{{Name: "test"}, {Name: "lint"}}}
	rec.Reviews = []task.Review{{Round: 2, Findings: []task.Finding{{}, {}, {}}}}
	rec.Decisions = []task.Decision{{Question: task.Question{Issue: "Retry 4xx?"}}}
	return rec
}

func TestFrameShowsTheCIAndReviewBadgesAtTheSmallestSupportedSize(t *testing.T) {
	// One pane line is left for the PR once the identity, the agent and state, the
	// title, and the question have taken theirs, so the row drops the reviewer and
	// the failing checks' names rather than truncating the badges away.
	m := Model{Width: minWidth, Height: minHeight, Focus: TaskPane}
	m.SetTasks([]*task.Record{waiting()})
	m.LogOpen = true
	got := screen(m)
	for _, want := range []string{"a/b#10", "NEEDS_DECISION", "PR #912", "CI ✗", "review ✗ 3", "Retry 4xx?"} {
		if !strings.Contains(got, want) {
			t.Errorf("a %dx%d frame is missing %q:\n%s", minWidth, minHeight, want, got)
		}
	}
}

func TestFrameShowsTheWholePRRowWhenThePaneIsTallEnough(t *testing.T) {
	// A taller pane has room for the parts the smallest layout drops, and then the
	// row says exactly what status says. wrap breaks it at spaces, so joining the
	// lines with a space puts it back together.
	rec := waiting()
	m := Model{Width: minWidth, Height: 30}
	m.SetTasks([]*task.Record{rec})
	lines := taskPane(m, 32, 7).rows[0].lines
	got := strings.Join(lines, " ")
	if want := render.TaskRows(rec)[2]; !strings.Contains(strings.Join(strings.Fields(got), " "), want) {
		t.Errorf("the tall pane's row is missing %q:\n%q", want, lines)
	}
	if !strings.Contains(got, "Retry 4xx?") {
		t.Errorf("the question is missing:\n%q", lines)
	}
}

func TestTaskPaneRowsFitThePaneTheyAreDrawnIn(t *testing.T) {
	// The pane scrolls by whole rows, so a row taller than the pane would put its own
	// last line out of reach. layoutFor never gives the pane fewer than taskRowLines.
	rec := waiting()
	rec.CI.Failing = []task.CheckRun{{Name: "integration-tests"}, {Name: "lint"}, {Name: "typecheck"}}
	rec.Decisions = []task.Decision{{Question: task.Question{Issue: "Should retries apply to 4xx responses too?"}}}
	m := Model{Width: minWidth, Height: minHeight}
	m.SetTasks([]*task.Record{rec, record(11, task.Working)})
	for _, width := range []int{20, 32, 60, 200} {
		for _, body := range []int{taskRowLines, 6, 10, 40} {
			for i, r := range taskPane(m, width, body).rows {
				if len(r.lines) > body {
					t.Errorf("task %d at %d columns draws %d lines in a pane of %d: %q", i, width, len(r.lines), body, r.lines)
				}
			}
		}
	}
}

func TestFrameScrollsTheLog(t *testing.T) {
	m := model()
	m.LogOpen = true
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
