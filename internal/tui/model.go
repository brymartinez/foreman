package tui

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/render"
	"github.com/brymartinez/foreman/internal/task"
)

// Pane is the dashboard pane that receives movement keys.
type Pane int

const (
	InboxPane Pane = iota
	TaskPane
)

// Mode is what the footer is collecting. Browsing is the normal view.
type Mode int

const (
	Browsing Mode = iota
	ChoosingAgent
	TypingContext
	ConfirmingCancel
)

type ActionKind int

const (
	ActionNone ActionKind = iota
	ActionQuit
	ActionRefresh
	ActionStart
	ActionCancel
	ActionResume
	ActionAuto
)

// Action is what a keystroke asks the driver to do. The model itself does no I/O.
type Action struct {
	Kind   ActionKind
	Start  app.StartRequest
	Cancel *task.Record
	// Resume is the REVIEWED task whose PR feedback the agent should answer.
	Resume task.Key
}

// Model is everything the dashboard draws and everything a keystroke changes.
type Model struct {
	Width, Height int
	Inbox         []app.InboxEntry
	Tasks         []*task.Record
	// Log is the selected task's runner log, oldest line first.
	Log     []string
	LogErr  string
	LogPath string
	LogOpen bool // Enter on a delegated task reveals its log below the lists.

	Focus       Pane
	InboxCursor int
	TaskCursor  int
	// LogOffset is how many lines the log is scrolled back from its tail. Zero follows.
	LogOffset int
	// ShowOpen swaps the open pane's log for the selected task's foreman show text.
	ShowOpen bool
	// ShowTop is the first wrapped show line in view. Show is read from the top, unlike
	// the log, which follows its tail.
	ShowTop int

	Mode  Mode
	Input string // the context file being typed
	// Message is the one line under the panes: the last result, or an error.
	Message     string
	Refreshing  bool
	RefreshedAt time.Time
	Warnings    []error

	// AutoOn starts eligible INBOX issues with AutoAgent while fewer than AutoMax
	// runs are active.
	AutoOn    bool
	AutoAgent task.Agent
	AutoMax   int

	pending      app.StartRequest  // the delegation being collected
	cancelTarget task.Key          // the task named when cancellation was requested
	manualTask   bool              // cursor movement stops following the latest delegation
	tried        map[task.Key]bool // issues auto mode has started this session
	starting     map[task.Key]bool // starts and resumes in flight, manual or automatic
}

// Selected is the highlighted delegated task, or nil when nothing is delegated.
func (m *Model) Selected() *task.Record {
	if m.TaskCursor < 0 || m.TaskCursor >= len(m.Tasks) {
		return nil
	}
	return m.Tasks[m.TaskCursor]
}

func (m *Model) selectedEntry() *app.InboxEntry {
	if m.InboxCursor < 0 || m.InboxCursor >= len(m.Inbox) {
		return nil
	}
	return &m.Inbox[m.InboxCursor]
}

// SetTasks follows the latest start by default and preserves a manually picked task
// across refreshes. The list is sorted by attention, so state changes can reorder it.
func (m *Model) SetTasks(recs []*task.Record) {
	var selected task.Key
	if cur := m.Selected(); cur != nil {
		selected = cur.Key
	}
	m.Tasks = recs
	if !m.manualTask {
		m.TaskCursor = newestTask(recs)
	} else {
		m.TaskCursor = clamp(indexOfTask(recs, selected, m.TaskCursor), len(recs))
	}
	if cur := m.Selected(); cur == nil || cur.Key != selected {
		// The task the cursor was on is gone, so the pane is about to show another
		// task's log. Follow its tail rather than keep the old task's scroll offset.
		m.LogOffset = 0
		m.LogOpen, m.ShowOpen = false, false
	}
}

func newestTask(recs []*task.Record) int {
	newest := 0
	for i := 1; i < len(recs); i++ {
		if recs[i].Delegation.StartedAt.After(recs[newest].Delegation.StartedAt) {
			newest = i
		}
	}
	return newest
}

// SetInbox replaces the inbox list and keeps the cursor on the same issue.
func (m *Model) SetInbox(entries []app.InboxEntry) {
	var selected task.Key
	if cur := m.selectedEntry(); cur != nil {
		selected = cur.Item.Key
	}
	m.Inbox = entries
	m.InboxCursor = clamp(indexOfEntry(entries, selected, m.InboxCursor), len(entries))
}

func indexOfTask(recs []*task.Record, want task.Key, fallback int) int {
	if want == (task.Key{}) {
		return fallback
	}
	for i, r := range recs {
		if r.Key == want {
			return i
		}
	}
	return fallback
}

func indexOfEntry(entries []app.InboxEntry, want task.Key, fallback int) int {
	if want == (task.Key{}) {
		return fallback
	}
	for i, e := range entries {
		if e.Item.Key == want {
			return i
		}
	}
	return fallback
}

func clamp(i, n int) int {
	if i >= n {
		i = n - 1
	}
	if i < 0 {
		return 0
	}
	return i
}

// Key applies one keystroke and returns what the driver must do.
func (m *Model) Key(k Key) Action {
	if k.Kind == KeyCtrlC {
		return Action{Kind: ActionQuit}
	}
	switch m.Mode {
	case ChoosingAgent:
		return m.chooseAgent(k)
	case TypingContext:
		return m.typeContext(k)
	case ConfirmingCancel:
		return m.confirmCancel(k)
	}
	return m.browse(k)
}

func (m *Model) browse(k Key) Action {
	switch {
	case k.Kind == KeyRune && k.Rune == 'q':
		return Action{Kind: ActionQuit}
	case k.Kind == KeyRune && k.Rune == 'r':
		return Action{Kind: ActionRefresh}
	case k.Kind == KeyRune && k.Rune == 'a':
		return m.toggleAuto()
	case k.Kind == KeyTab:
		m.Focus = (m.Focus + 1) % 2
	case k.Kind == KeyLeft || (k.Kind == KeyRune && k.Rune == 'h'):
		m.Focus = InboxPane
	case k.Kind == KeyRight || (k.Kind == KeyRune && k.Rune == 'l'):
		m.Focus = TaskPane
	case k.Kind == KeyEscape:
		m.LogOpen, m.ShowOpen = false, false
	case k.Kind == KeyUp || (k.Kind == KeyRune && k.Rune == 'k'):
		m.move(-1)
	case k.Kind == KeyDown || (k.Kind == KeyRune && k.Rune == 'j'):
		m.move(1)
	case m.LogOpen && k.Kind == KeyRune && k.Rune == 's':
		m.toggleShow()
	case m.LogOpen && m.Focus == TaskPane && (k.Kind == KeyRune && k.Rune == 'u'):
		m.scrollLog(m.logPage())
	case m.LogOpen && m.Focus == TaskPane && (k.Kind == KeyRune && k.Rune == 'd'):
		m.scrollLog(-m.logPage())
	case m.LogOpen && m.Focus == TaskPane && k.Kind == KeyCtrlU:
		m.scrollLog(max(m.logPage()/2, 1))
	case m.LogOpen && m.Focus == TaskPane && k.Kind == KeyCtrlD:
		m.scrollLog(-max(m.logPage()/2, 1))
	case m.LogOpen && m.Focus == TaskPane && k.Kind == KeyRune && k.Rune == 'g':
		m.scrollTop()
	case m.LogOpen && m.Focus == TaskPane && k.Kind == KeyRune && k.Rune == 'G':
		m.scrollEnd()
	case m.LogOpen && k.Kind == KeyPageUp:
		m.scrollLog(m.logPage())
	case m.LogOpen && k.Kind == KeyPageDown:
		m.scrollLog(-m.logPage())
	case m.LogOpen && k.Kind == KeyEnd:
		m.scrollEnd()
	case k.Kind == KeyRune && k.Rune == 'c':
		return m.startCancel()
	case k.Kind == KeyEnter:
		return m.enter()
	}
	return Action{}
}

func (m *Model) move(by int) {
	if m.Focus == InboxPane {
		m.InboxCursor = clamp(m.InboxCursor+by, len(m.Inbox))
		return
	}
	before := m.TaskCursor
	m.TaskCursor = clamp(m.TaskCursor+by, len(m.Tasks))
	if m.TaskCursor != before {
		m.manualTask = true
		m.LogOpen, m.ShowOpen = false, false
		// A new task brings a new log, so show its tail rather than keep the old offset.
		m.LogOffset = 0
	}
}

// toggleShow swaps the open pane between the log and the show text. Show opens at its
// top, where the state and last outcome are; the log comes back following its tail.
func (m *Model) toggleShow() {
	m.ShowOpen = !m.ShowOpen
	m.ShowTop, m.LogOffset = 0, 0
}

// showLines is the selected task's foreman show text, wrapped to the pane width. It is
// rendered on each call from the record in memory, so it is never older than the last
// SetTasks and always matches what foreman show prints.
func (m *Model) showLines() []string {
	rec := m.Selected()
	if rec == nil {
		return nil
	}
	var b bytes.Buffer
	render.Show(&b, rec)
	var out []string
	for _, l := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
		out = append(out, wrapLine(printable(l), m.Width)...)
	}
	return out
}

// showLimit is the last ShowTop that still fills the pane.
func (m *Model) showLimit() int {
	return max(len(m.showLines())-m.logPage(), 0)
}

// scrollLog moves the open view by lines; a positive count goes toward older log lines
// or toward the top of the show text.
func (m *Model) scrollLog(by int) {
	if m.ShowOpen {
		// A refresh or a resize can shrink the text under a stored ShowTop, so the step
		// starts from the page the frame draws, not from the stale position.
		limit := m.showLimit()
		m.ShowTop = min(max(min(m.ShowTop, limit)-by, 0), limit)
		return
	}
	m.LogOffset += by
	if limit := len(m.Log) - m.logPage(); m.LogOffset > limit {
		m.LogOffset = limit
	}
	if m.LogOffset < 0 {
		m.LogOffset = 0
	}
}

func (m *Model) scrollTop() {
	if m.ShowOpen {
		m.ShowTop = 0
		return
	}
	m.scrollLog(len(m.Log))
}

func (m *Model) scrollEnd() {
	if m.ShowOpen {
		m.ShowTop = m.showLimit()
		return
	}
	m.LogOffset = 0
}

func (m *Model) logPage() int {
	lay, ok := layoutFor(m.Width, m.Height, true)
	if !ok {
		return 1
	}
	return max(lay.logHeight-1, 1)
}

func (m *Model) enter() Action {
	if m.Focus == InboxPane {
		e := m.selectedEntry()
		if e == nil {
			m.Message = "Nothing to delegate."
			return Action{}
		}
		if e.State == task.Reviewed {
			// The task keeps its agent, worktree, and session, so there is nothing to
			// ask: it goes straight back to work on the comments (D121).
			m.manualTask = false
			m.Message = fmt.Sprintf("Resuming %s#%d to answer its PR feedback…", e.Item.Key.Repo, e.Item.Key.Number)
			m.starting = mark(m.starting, e.Item.Key)
			return Action{Kind: ActionResume, Resume: e.Item.Key}
		}
		m.pending = app.StartRequest{Repo: e.Item.Key.Repo, Issue: e.Item.Key.Number}
		m.Mode, m.Input = ChoosingAgent, ""
		return Action{}
	}
	rec := m.Selected()
	switch {
	case rec == nil:
		m.Message = "No delegated work yet. Pick an issue on the left."
	default:
		m.LogOpen = true
		m.LogOffset = 0
		if rec.State == task.NeedsDecision {
			m.Message = fmt.Sprintf("foreman respond %s#%d · answers the waiting question", rec.Repo, rec.Number)
		} else {
			m.Message = ""
		}
	}
	return Action{}
}

func (m *Model) startCancel() Action {
	rec := m.Selected()
	switch {
	case rec == nil:
		m.Message = "No delegated work to cancel."
	case rec.Delegation == nil || rec.Delegation.RunnerPID == 0:
		m.Message = fmt.Sprintf("%s#%d is %s with no runner; nothing to cancel", rec.Repo, rec.Number, rec.State)
	default:
		m.cancelTarget = rec.Key
		m.Mode = ConfirmingCancel
	}
	return Action{}
}

func (m *Model) chooseAgent(k Key) Action {
	switch {
	case k.Kind == KeyEscape:
		m.Mode = Browsing
	case k.Kind == KeyRune && k.Rune == '1':
		m.pending.Agent, m.Mode, m.Input = task.Claude, TypingContext, ""
	case k.Kind == KeyRune && k.Rune == '2':
		m.pending.Agent, m.Mode, m.Input = task.Codex, TypingContext, ""
	}
	return Action{}
}

func (m *Model) typeContext(k Key) Action {
	switch {
	case k.Kind == KeyEscape:
		m.Mode, m.Input = Browsing, ""
	case k.Kind == KeyBackspace:
		if r := []rune(m.Input); len(r) > 0 {
			m.Input = string(r[:len(r)-1])
		}
	case k.Kind == KeyRune:
		m.Input += string(k.Rune)
	case k.Kind == KeyEnter:
		req := m.pending
		req.ContextFile = strings.TrimSpace(m.Input)
		m.Mode, m.Input = Browsing, ""
		m.manualTask = false
		m.Message = fmt.Sprintf("Starting %s#%d with %s…", req.Repo, req.Issue, req.Agent)
		m.starting = mark(m.starting, task.Key{Repo: req.Repo, Number: req.Issue})
		return Action{Kind: ActionStart, Start: req}
	}
	return Action{}
}

func (m *Model) confirmCancel(k Key) Action {
	if k.Kind == KeyRune && k.Rune == 'y' {
		key := m.cancelTarget
		m.Mode = Browsing
		m.cancelTarget = task.Key{}
		for _, rec := range m.Tasks {
			if rec.Key != key {
				continue
			}
			if (rec.State == task.Working || rec.State == task.Reviewing) && rec.Delegation != nil && rec.Delegation.RunnerPID != 0 {
				m.Message = fmt.Sprintf("Cancelling %s#%d…", rec.Repo, rec.Number)
				return Action{Kind: ActionCancel, Cancel: rec}
			}
			break
		}
		m.Message = fmt.Sprintf("%s#%d is no longer running; nothing to cancel", key.Repo, key.Number)
		return Action{}
	}
	if k.Kind == KeyEscape || k.Kind == KeyRune {
		m.Mode = Browsing
		m.cancelTarget = task.Key{}
	}
	return Action{}
}
