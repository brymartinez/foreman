package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

// Pane is the dashboard pane that receives movement keys.
type Pane int

const (
	InboxPane Pane = iota
	TaskPane
	LogPane
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
)

// Action is what a keystroke asks the driver to do. The model itself does no I/O.
type Action struct {
	Kind   ActionKind
	Start  app.StartRequest
	Cancel *task.Record
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

	Focus       Pane
	InboxCursor int
	TaskCursor  int
	// LogOffset is how many lines the log is scrolled back from its tail. Zero follows.
	LogOffset int

	Mode  Mode
	Input string // the context file being typed
	// Message is the one line under the panes: the last result, or an error.
	Message     string
	Refreshing  bool
	RefreshedAt time.Time
	Warnings    []error

	pending      app.StartRequest // the delegation being collected
	cancelTarget task.Key         // the task named when cancellation was requested
	manualTask   bool             // cursor movement stops following the latest delegation
}

// Selected is the task the log pane follows, or nil when nothing is delegated.
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
	case k.Kind == KeyTab:
		m.Focus = (m.Focus + 1) % 3
	case k.Kind == KeyUp || (k.Kind == KeyRune && k.Rune == 'k'):
		m.move(-1)
	case k.Kind == KeyDown || (k.Kind == KeyRune && k.Rune == 'j'):
		m.move(1)
	case m.Focus == LogPane && (k.Kind == KeyRune && k.Rune == 'u'):
		m.scrollLog(m.logPage())
	case m.Focus == LogPane && (k.Kind == KeyRune && k.Rune == 'd'):
		m.scrollLog(-m.logPage())
	case m.Focus == LogPane && k.Kind == KeyCtrlU:
		m.scrollLog(max(m.logPage()/2, 1))
	case m.Focus == LogPane && k.Kind == KeyCtrlD:
		m.scrollLog(-max(m.logPage()/2, 1))
	case m.Focus == LogPane && k.Kind == KeyRune && k.Rune == 'g':
		m.scrollLog(len(m.Log))
	case m.Focus == LogPane && k.Kind == KeyRune && k.Rune == 'G':
		m.LogOffset = 0
	case k.Kind == KeyPageUp:
		m.scrollLog(m.logPage())
	case k.Kind == KeyPageDown:
		m.scrollLog(-m.logPage())
	case k.Kind == KeyEnd:
		m.LogOffset = 0
	case k.Kind == KeyRune && k.Rune == 'c':
		return m.startCancel()
	case k.Kind == KeyEnter:
		return m.enter()
	}
	return Action{}
}

func (m *Model) move(by int) {
	if m.Focus == LogPane {
		m.scrollLog(-by)
		return
	}
	if m.Focus == InboxPane {
		m.InboxCursor = clamp(m.InboxCursor+by, len(m.Inbox))
		return
	}
	before := m.TaskCursor
	m.TaskCursor = clamp(m.TaskCursor+by, len(m.Tasks))
	if m.TaskCursor != before {
		m.manualTask = true
		// A new task brings a new log, so show its tail rather than keep the old offset.
		m.LogOffset = 0
	}
}

func (m *Model) scrollLog(by int) {
	m.LogOffset += by
	if limit := len(m.Log) - m.logPage(); m.LogOffset > limit {
		m.LogOffset = limit
	}
	if m.LogOffset < 0 {
		m.LogOffset = 0
	}
}

func (m *Model) logPage() int {
	lay, ok := layoutFor(m.Width, m.Height)
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
		m.pending = app.StartRequest{Repo: e.Item.Key.Repo, Issue: e.Item.Key.Number}
		m.Mode, m.Input = ChoosingAgent, ""
		return Action{}
	}
	rec := m.Selected()
	switch {
	case rec == nil:
		m.Message = "No delegated work yet. Pick an issue on the left."
	case rec.State == task.NeedsDecision:
		// The command comes first, so a narrow terminal truncates the explanation
		// rather than the part the developer has to type.
		m.Message = fmt.Sprintf("foreman respond %s#%d · answers the waiting question", rec.Repo, rec.Number)
	default:
		m.Message = fmt.Sprintf("%s#%d · %s", rec.Repo, rec.Number, rec.Delegation.RunnerLog)
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
