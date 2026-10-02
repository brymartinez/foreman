package tui

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestLogFollowsMostRecentDelegationUntilTaskIsPicked(t *testing.T) {
	older := record(10, task.NeedsDecision)
	newer := record(11, task.Working)
	older.Delegation.StartedAt = time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	newer.Delegation.StartedAt = older.Delegation.StartedAt.Add(time.Hour)
	m := Model{Width: 100, Height: 30}
	m.SetTasks([]*task.Record{older, newer}) // attention order puts the older task first
	if m.Selected().Key != newer.Key {
		t.Fatalf("log follows %v; want the newest delegation %v", m.Selected().Key, newer.Key)
	}
	// A new delegation takes over while the user has not picked a task.
	latest := record(12, task.Working)
	latest.Delegation.StartedAt = newer.Delegation.StartedAt.Add(time.Hour)
	m.SetTasks([]*task.Record{older, newer, latest})
	if m.Selected().Key != latest.Key {
		t.Fatalf("log did not follow the new delegation: %v", m.Selected().Key)
	}
	m.Focus = TaskPane
	m.Key(Key{Kind: KeyUp})
	m.SetTasks([]*task.Record{latest, older, newer})
	if m.Selected().Key != newer.Key {
		t.Fatalf("manual task selection was lost: %v", m.Selected().Key)
	}
}

func TestCancelTargetsTheLogTaskFromTheInbox(t *testing.T) {
	m := model() // inbox starts focused
	if got := press(&m, rune_('c'), rune_('y')); got.Kind != ActionCancel || got.Cancel.Key != m.Selected().Key {
		t.Fatalf("cancel targeted %+v; want the task shown in the log", got)
	}
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
		t.Errorf("tab did not cycle back to the inbox: %v", m.Focus)
	}
	press(&m, rune_('l'))
	if m.Focus != TaskPane {
		t.Errorf("l did not focus delegated work: %v", m.Focus)
	}
	press(&m, rune_('h'))
	if m.Focus != InboxPane {
		t.Errorf("h did not focus the inbox: %v", m.Focus)
	}
}

func TestArrowKeysSelectPanes(t *testing.T) {
	arrows, letters := model(), model()
	for i, k := range []Key{{Kind: KeyRight}, {Kind: KeyLeft}, {Kind: KeyRight}} {
		press(&arrows, k)
		press(&letters, rune_(map[KeyKind]rune{KeyRight: 'l', KeyLeft: 'h'}[k.Kind]))
		if arrows.Focus != letters.Focus {
			t.Fatalf("press %d: arrow left focus at %v, the letter at %v", i, arrows.Focus, letters.Focus)
		}
	}
	if arrows.Focus != TaskPane {
		t.Errorf("right did not focus delegated work: %v", arrows.Focus)
	}
}

func TestEnterOpensSelectedLogAndKeysScrollIt(t *testing.T) {
	m := model()
	m.Log = make([]string, 200)
	for i := range m.Log {
		m.Log[i] = strconv.Itoa(i)
	}
	press(&m, rune_('l'), Key{Kind: KeyEnter})
	if !m.LogOpen || m.Focus != TaskPane {
		t.Fatalf("enter did not open the selected task's log: %+v", m)
	}
	press(&m, rune_('u'))
	if m.LogOffset != m.logPage() {
		t.Fatalf("u scrolled %d lines; want one page", m.LogOffset)
	}
	press(&m, Key{Kind: KeyCtrlU})
	if m.LogOffset != m.logPage()+max(m.logPage()/2, 1) {
		t.Fatalf("ctrl-u did not scroll half a page: %d", m.LogOffset)
	}
	press(&m, Key{Kind: KeyCtrlD}, rune_('d'))
	if m.LogOffset != 0 {
		t.Fatalf("downward page keys did not reach the tail: %d", m.LogOffset)
	}
	press(&m, rune_('g'))
	if m.LogOffset != len(m.Log)-m.logPage() {
		t.Fatalf("g did not reach the oldest log line: %d", m.LogOffset)
	}
	press(&m, rune_('G'))
	if m.LogOffset != 0 {
		t.Fatalf("G did not follow the tail: %d", m.LogOffset)
	}
	if m.TaskCursor != 0 || m.InboxCursor != 0 {
		t.Fatalf("log navigation moved a list cursor: task=%d inbox=%d", m.TaskCursor, m.InboxCursor)
	}
	press(&m, rune_('j'))
	if m.TaskCursor != 1 || m.LogOpen {
		t.Fatalf("moving to another task did not close the old log: task=%d open=%t", m.TaskCursor, m.LogOpen)
	}
	press(&m, Key{Kind: KeyEscape})
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
	m.LogOpen = true
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
	m.LogOpen = true
	m.Log = make([]string, 200)
	press(&m, Key{Kind: KeyPageUp})
	press(&m, Key{Kind: KeyDown})
	if m.LogOffset != 0 {
		t.Errorf("offset = %d; a new selection shows its own tail", m.LogOffset)
	}
}

func TestEnterOnReviewedResumesInsteadOfAsking(t *testing.T) {
	m := Model{Width: 100, Height: 30, Inbox: []app.InboxEntry{{
		Item:      app.InboxItem{Key: task.Key{Repo: "a/b", Number: 7}, Kind: task.Issue, Title: "Add retries"},
		Delegated: true,
		State:     task.Reviewed,
	}}}
	got := m.Key(Key{Kind: KeyEnter})
	if got.Kind != ActionResume || got.Resume != (task.Key{Repo: "a/b", Number: 7}) {
		t.Fatalf("action = %+v", got)
	}
	if m.Mode != Browsing {
		t.Errorf("mode = %v; a resume asks for no agent", m.Mode)
	}
}

func TestEnterOnAnUndelegatedIssueStillAsksForAnAgent(t *testing.T) {
	m := Model{Width: 100, Height: 30, Inbox: []app.InboxEntry{
		{Item: app.InboxItem{Key: task.Key{Repo: "a/b", Number: 8}, Kind: task.Issue, Title: "New"}},
	}}
	if got := m.Key(Key{Kind: KeyEnter}); got.Kind != ActionNone || m.Mode != ChoosingAgent {
		t.Fatalf("action = %+v mode = %v", got, m.Mode)
	}
}

// longShow gives the selected task enough show text to scroll at any supported size.
func longShow(m *Model) {
	rec := m.Selected()
	rec.LastOutcome = &task.Outcome{Status: "failed", Summary: strings.Repeat("the outcome summary wraps ", 12)}
	rec.Reviews = []task.Review{{Round: 1, Reviewer: task.Codex, Findings: make([]task.Finding, 30)}}
	for i := range rec.Reviews[0].Findings {
		rec.Reviews[0].Findings[i] = task.Finding{Severity: "blocking", File: "internal/tui/model.go", Line: i + 1, Summary: "finding " + strconv.Itoa(i)}
	}
}

func TestShowToggle(t *testing.T) {
	m := model()
	longShow(&m)
	m.Log = make([]string, 200)
	press(&m, rune_('s'))
	if m.ShowOpen {
		t.Fatal("s opened the show view with the pane closed")
	}
	press(&m, rune_('l'), Key{Kind: KeyEnter})
	if m.ShowOpen {
		t.Fatal("the pane opened on the show view; it opens on the log")
	}
	press(&m, rune_('u'), rune_('s'))
	if !m.ShowOpen || m.ShowTop != 0 {
		t.Fatalf("s did not open show at its top: open=%t top=%d", m.ShowOpen, m.ShowTop)
	}
	press(&m, rune_('d'), rune_('s'))
	if m.ShowOpen || m.LogOffset != 0 {
		t.Fatalf("s did not return to the log tail: open=%t offset=%d", m.ShowOpen, m.LogOffset)
	}
	press(&m, rune_('h'), rune_('s'))
	if !m.ShowOpen || !m.LogOpen {
		t.Fatalf("s did not switch views with INBOX focused: show=%t log=%t", m.ShowOpen, m.LogOpen)
	}
	if m.InboxCursor != 0 || m.Mode != Browsing {
		t.Fatalf("s changed the inbox: cursor=%d mode=%v", m.InboxCursor, m.Mode)
	}
}

func TestShowResetsOnClose(t *testing.T) {
	m := model()
	press(&m, rune_('l'), Key{Kind: KeyEnter}, rune_('s'))
	press(&m, Key{Kind: KeyEnter})
	if !m.ShowOpen {
		t.Error("enter on an open pane left the show view")
	}
	press(&m, Key{Kind: KeyEscape}, Key{Kind: KeyEnter})
	if m.ShowOpen {
		t.Error("the pane reopened on show after esc")
	}
	press(&m, rune_('s'), rune_('j'), Key{Kind: KeyEnter})
	if m.TaskCursor != 1 || m.ShowOpen {
		t.Errorf("another task opened on show: task=%d show=%t", m.TaskCursor, m.ShowOpen)
	}
	press(&m, rune_('s'))
	m.SetTasks(m.Tasks[2:])
	if m.LogOpen || m.ShowOpen {
		t.Errorf("a refresh that changed the task left the pane open: log=%t show=%t", m.LogOpen, m.ShowOpen)
	}
}

func TestShowScrollKeys(t *testing.T) {
	m := model()
	m.Width, m.Height = minWidth, minHeight
	longShow(&m)
	press(&m, rune_('l'), Key{Kind: KeyEnter}, rune_('s'))
	page, limit := m.logPage(), m.showLimit()
	if limit < 3*page {
		t.Fatalf("the test text is too short to scroll: limit %d page %d", limit, page)
	}
	for _, step := range []struct {
		key  Key
		want int
	}{
		{rune_('d'), page},
		{Key{Kind: KeyCtrlD}, page + max(page/2, 1)},
		{Key{Kind: KeyPageDown}, 2*page + max(page/2, 1)},
		{rune_('u'), page + max(page/2, 1)},
		{Key{Kind: KeyCtrlU}, page},
		{Key{Kind: KeyPageUp}, 0},
		{Key{Kind: KeyPageUp}, 0},
		{rune_('G'), limit},
		{rune_('d'), limit},
		{rune_('g'), 0},
		{Key{Kind: KeyEnd}, limit},
	} {
		press(&m, step.key)
		if m.ShowTop != step.want {
			t.Fatalf("after %+v ShowTop = %d; want %d", step.key, m.ShowTop, step.want)
		}
	}
	if m.LogOffset != 0 {
		t.Errorf("scrolling show moved the log: %d", m.LogOffset)
	}
}

func TestShowReachesEveryLine(t *testing.T) {
	m := model()
	m.Width, m.Height = minWidth, minHeight
	longShow(&m)
	press(&m, rune_('l'), Key{Kind: KeyEnter}, rune_('s'))
	want := m.showLines()
	var seen []string
	for range len(want) + 1 {
		seen = append(seen, visibleShow(&m)...)
		press(&m, rune_('d'))
	}
	for _, w := range want {
		if strings.TrimSpace(w) != "" && !slices.Contains(seen, strings.TrimRight(w, " ")) {
			t.Errorf("paging never showed %q", w)
		}
	}
}

func TestShowScrollsUpAfterTheTextShrinks(t *testing.T) {
	m := model()
	m.Width, m.Height = minWidth, minHeight
	longShow(&m)
	press(&m, rune_('l'), Key{Kind: KeyEnter}, rune_('s'), rune_('G'))
	m.Selected().Reviews[0].Findings = m.Selected().Reviews[0].Findings[:2]
	limit := m.showLimit()
	press(&m, rune_('u'))
	if want := max(limit-m.logPage(), 0); m.ShowTop != want {
		t.Errorf("u after the text shrank left ShowTop at %d; want %d (limit %d)", m.ShowTop, want, limit)
	}
	press(&m, rune_('G'))
	m.Width = 200
	limit = m.showLimit()
	press(&m, Key{Kind: KeyPageUp})
	if want := max(limit-m.logPage(), 0); m.ShowTop != want {
		t.Errorf("page up after a resize left ShowTop at %d; want %d (limit %d)", m.ShowTop, want, limit)
	}
}

func TestBrowseViewKeyTogglesView(t *testing.T) {
	var m Model
	if a := press(&m, rune_('v')); a.Kind != ActionToggleView || !m.AllRepos {
		t.Fatalf("first v = %+v, all repos %t; want the all view and a refresh", a, m.AllRepos)
	}
	if a := press(&m, rune_('v')); a.Kind != ActionToggleView || m.AllRepos {
		t.Fatalf("second v = %+v, all repos %t; want the configured view and a refresh", a, m.AllRepos)
	}
}
