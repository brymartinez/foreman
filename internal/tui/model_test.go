package tui

import (
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

func entry(repo task.Repo, n int, title string) app.InboxEntry {
	return app.InboxEntry{Item: app.InboxItem{Key: task.Key{Repo: repo, Number: n}, Kind: task.Issue, Title: title}, Changes: []string{"assigned to you"}}
}

func record(n int, state task.State) *task.Record {
	return &task.Record{
		Key: task.Key{Repo: "a/b", Number: n}, Title: "Task", State: state,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Reviewer: task.Codex, RunnerPID: 4242, RunnerLog: "/logs/b.log"},
	}
}

func model() Model {
	m := Model{Width: 100, Height: 30}
	m.SetInbox([]app.InboxEntry{entry("a/b", 1, "First"), entry("a/b", 2, "Second")})
	m.SetTasks([]*task.Record{record(10, task.Working), record(11, task.Reviewing), record(12, task.Failed)})
	return m
}

func press(m *Model, keys ...Key) Action {
	var last Action
	for _, k := range keys {
		last = m.Key(k)
	}
	return last
}

func rune_(r rune) Key { return Key{Kind: KeyRune, Rune: r} }

func TestArrowsMoveAndClamp(t *testing.T) {
	m := model()
	m.Focus = TaskPane
	press(&m, Key{Kind: KeyDown}, Key{Kind: KeyDown})
	if m.TaskCursor != 2 {
		t.Fatalf("task cursor = %d; want 2", m.TaskCursor)
	}
	press(&m, Key{Kind: KeyDown}, Key{Kind: KeyDown})
	if m.TaskCursor != 2 {
		t.Errorf("task cursor ran past the end: %d", m.TaskCursor)
	}
	press(&m, rune_('k'), rune_('k'), rune_('k'), rune_('k'))
	if m.TaskCursor != 0 {
		t.Errorf("task cursor ran past the start: %d", m.TaskCursor)
	}
	m.Focus = InboxPane
	press(&m, rune_('j'), rune_('j'))
	if m.InboxCursor != 1 || m.TaskCursor != 0 {
		t.Errorf("inbox cursor = %d, task cursor = %d; only the focused pane moves", m.InboxCursor, m.TaskCursor)
	}
}

func TestTabMovesFocus(t *testing.T) {
	m := model()
	if m.Focus != InboxPane {
		t.Fatalf("focus starts at %v", m.Focus)
	}
	press(&m, Key{Kind: KeyTab})
	if m.Focus != TaskPane {
		t.Errorf("tab left focus at %v", m.Focus)
	}
	press(&m, Key{Kind: KeyTab})
	if m.Focus != InboxPane {
		t.Errorf("tab did not cycle back: %v", m.Focus)
	}
}

func TestDelegateAsksAgentThenContext(t *testing.T) {
	m := model()
	press(&m, Key{Kind: KeyDown}, Key{Kind: KeyEnter})
	if m.Mode != ChoosingAgent {
		t.Fatalf("mode after enter = %v", m.Mode)
	}
	press(&m, rune_('2'))
	if m.Mode != TypingContext {
		t.Fatalf("mode after picking an agent = %v", m.Mode)
	}
	press(&m, rune_('c'), rune_('t'), rune_('x'), rune_('y'), Key{Kind: KeyBackspace})
	if m.Input != "ctx" {
		t.Fatalf("input = %q", m.Input)
	}
	got := press(&m, Key{Kind: KeyEnter})
	want := app.StartRequest{Repo: "a/b", Issue: 2, Agent: task.Codex, ContextFile: "ctx"}
	if got.Kind != ActionStart || got.Start != want {
		t.Fatalf("action = %+v; want start %+v", got, want)
	}
	if m.Mode != Browsing {
		t.Errorf("mode after starting = %v", m.Mode)
	}
}

func TestDelegateWithoutAContextFile(t *testing.T) {
	m := model()
	got := press(&m, Key{Kind: KeyEnter}, rune_('1'), Key{Kind: KeyEnter})
	want := app.StartRequest{Repo: "a/b", Issue: 1, Agent: task.Claude}
	if got.Kind != ActionStart || got.Start != want {
		t.Fatalf("action = %+v; want start %+v", got, want)
	}
}

func TestEscapeAbandonsDelegation(t *testing.T) {
	for _, keys := range [][]Key{
		{Key{Kind: KeyEnter}, {Kind: KeyEscape}},
		{Key{Kind: KeyEnter}, rune_('1'), {Kind: KeyEscape}},
	} {
		m := model()
		if got := press(&m, keys...); got.Kind != ActionNone {
			t.Errorf("escape produced %+v", got)
		}
		if m.Mode != Browsing || m.Input != "" {
			t.Errorf("escape left mode %v input %q", m.Mode, m.Input)
		}
	}
}

func TestEnterOnNeedsDecisionPointsAtRespond(t *testing.T) {
	m := model()
	m.SetTasks([]*task.Record{record(10, task.NeedsDecision)})
	m.Focus = TaskPane
	if got := press(&m, Key{Kind: KeyEnter}); got.Kind != ActionNone {
		t.Fatalf("action = %+v", got)
	}
	// The command leads, so truncating the message to a narrow terminal cannot eat
	// the part the developer has to type.
	if !strings.HasPrefix(m.Message, "foreman respond a/b#10") {
		t.Errorf("message = %q", m.Message)
	}
	m.Width, m.Height = minWidth, 30
	if !strings.Contains(screen(m), "foreman respond a/b#10") {
		t.Errorf("a %d-column frame clipped the command:\n%s", minWidth, screen(m))
	}
}

func TestCancelConfirmsBeforeStopping(t *testing.T) {
	m := model()
	m.Focus = TaskPane
	press(&m, rune_('c'))
	if m.Mode != ConfirmingCancel {
		t.Fatalf("mode = %v", m.Mode)
	}
	if got := press(&m, rune_('n')); got.Kind != ActionNone || m.Mode != Browsing {
		t.Fatalf("n gave %+v in mode %v", got, m.Mode)
	}
	got := press(&m, rune_('c'), rune_('y'))
	if got.Kind != ActionCancel || got.Cancel == nil || got.Cancel.Number != 10 {
		t.Fatalf("action = %+v", got)
	}
	if m.Mode != Browsing {
		t.Errorf("mode after cancelling = %v", m.Mode)
	}
}

func TestCancelRefusesATaskWithNoRunner(t *testing.T) {
	m := model()
	rec := record(10, task.ReadyForHuman)
	rec.Delegation.RunnerPID = 0
	m.SetTasks([]*task.Record{rec})
	m.Focus = TaskPane
	if got := press(&m, rune_('c')); got.Kind != ActionNone || m.Mode != Browsing {
		t.Fatalf("action = %+v mode = %v", got, m.Mode)
	}
	if !strings.Contains(m.Message, "no runner") {
		t.Errorf("message = %q", m.Message)
	}
}

func TestCancelConfirmationKeepsItsOriginalTask(t *testing.T) {
	m := model()
	m.Focus = TaskPane
	press(&m, rune_('c')) // confirm a/b#10
	m.SetTasks([]*task.Record{record(11, task.Working)})
	if got := footer(m); !strings.Contains(got, "a/b#10") || strings.Contains(got, "a/b#11") {
		t.Fatalf("confirmation changed target after refresh: %q", got)
	}
	if got := press(&m, rune_('y')); got.Kind == ActionCancel {
		t.Fatalf("cancel switched to another task: %+v", got.Cancel.Key)
	}
	if !strings.Contains(m.Message, "a/b#10") {
		t.Errorf("message = %q; want the original task", m.Message)
	}
}

func TestCancelStopsTheTaskItAskedAboutAfterAReorder(t *testing.T) {
	m := model()
	m.Focus = TaskPane
	press(&m, Key{Kind: KeyDown}) // a/b#11
	press(&m, rune_('c'))
	// The gate reordered the list while the prompt was up.
	m.SetTasks([]*task.Record{record(12, task.Failed), record(11, task.Working), record(10, task.Working)})
	got := press(&m, rune_('y'))
	if got.Kind != ActionCancel || got.Cancel == nil || got.Cancel.Number != 11 {
		t.Fatalf("action = %+v; want the task the prompt named", got)
	}
}

func TestARefreshThatMovesTheCursorFollowsTheNewTail(t *testing.T) {
	m := model()
	m.Focus = TaskPane
	m.Log = make([]string, 200)
	press(&m, Key{Kind: KeyPageUp})
	m.SetTasks([]*task.Record{record(11, task.Reviewing), record(12, task.Failed)})
	if m.LogOffset != 0 {
		t.Errorf("offset = %d; the task the cursor was on is gone", m.LogOffset)
	}
}

func TestSelectionFollowsTheTaskAcrossARefresh(t *testing.T) {
	m := model()
	m.Focus = TaskPane
	press(&m, Key{Kind: KeyDown}) // a/b#11
	if m.Selected().Number != 11 {
		t.Fatalf("selected %d", m.Selected().Number)
	}
	// The gate moved #11 to the top of the list, and #10 finished.
	m.SetTasks([]*task.Record{record(11, task.ReadyForHuman), record(12, task.Failed)})
	if m.Selected() == nil || m.Selected().Number != 11 {
		t.Fatalf("selection lost across the refresh: %+v", m.Selected())
	}
	m.SetTasks([]*task.Record{record(12, task.Failed)})
	if m.Selected() == nil || m.Selected().Number != 12 {
		t.Fatalf("cursor not clamped after the task vanished: %+v", m.Selected())
	}
	m.SetTasks(nil)
	if m.Selected() != nil {
		t.Errorf("selection from an empty list: %+v", m.Selected())
	}
}

func TestSelectionFollowsTheInboxEntryAcrossARefresh(t *testing.T) {
	m := model()
	press(&m, Key{Kind: KeyDown}) // a/b#2
	m.SetInbox([]app.InboxEntry{entry("a/b", 3, "New"), entry("a/b", 2, "Second")})
	if m.InboxCursor != 1 {
		t.Errorf("inbox cursor = %d; want the same issue still selected", m.InboxCursor)
	}
}

func TestQuitKeys(t *testing.T) {
	for _, k := range []Key{rune_('q'), {Kind: KeyCtrlC}} {
		m := model()
		if got := press(&m, k); got.Kind != ActionQuit {
			t.Errorf("%+v gave %+v", k, got)
		}
	}
	// In the context prompt q is a character to type, but Ctrl+C still quits.
	m := model()
	if got := press(&m, Key{Kind: KeyEnter}, rune_('1'), rune_('q')); got.Kind != ActionNone || m.Input != "q" {
		t.Errorf("q in the context prompt gave %+v and input %q", got, m.Input)
	}
	m = model()
	if got := press(&m, Key{Kind: KeyEnter}, Key{Kind: KeyCtrlC}); got.Kind != ActionQuit {
		t.Errorf("ctrl+c in a prompt gave %+v", got)
	}
}

func TestRefreshKey(t *testing.T) {
	m := model()
	if got := press(&m, rune_('r')); got.Kind != ActionRefresh {
		t.Errorf("r gave %+v", got)
	}
}

func TestPageKeysScrollTheLogAndEndResumesFollowing(t *testing.T) {
	m := model()
	m.Log = make([]string, 200)
	press(&m, Key{Kind: KeyPageUp})
	if m.LogOffset == 0 {
		t.Fatal("page up did not scroll the log")
	}
	first := m.LogOffset
	press(&m, Key{Kind: KeyPageDown})
	if m.LogOffset != 0 {
		t.Errorf("page down left the offset at %d", m.LogOffset)
	}
	press(&m, Key{Kind: KeyPageUp}, Key{Kind: KeyEnd})
	if m.LogOffset != 0 {
		t.Errorf("end left the offset at %d (paged %d)", m.LogOffset, first)
	}
}

func TestMovingTheTaskCursorFollowsTheNewTail(t *testing.T) {
	m := model()
	m.Focus = TaskPane
	m.Log = make([]string, 200)
	press(&m, Key{Kind: KeyPageUp})
	press(&m, Key{Kind: KeyDown})
	if m.LogOffset != 0 {
		t.Errorf("offset = %d; a new selection shows its own tail", m.LogOffset)
	}
}
