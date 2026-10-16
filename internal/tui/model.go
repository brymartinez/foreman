package tui

import (
	"bytes"
	"fmt"
	"math"
	"slices"
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
	ProposingBranch
	TypingBranch
	ConfirmingCancel
	ConfirmingReset
	AnsweringDecision
	// Routing waits for App.Route; ConfirmingRoute shows its choice.
	Routing
	ConfirmingRoute
)

type ActionKind int

const (
	ActionNone ActionKind = iota
	ActionQuit
	ActionRefresh
	ActionStart
	ActionCancel
	ActionReset
	ActionResume
	ActionAuto
	ActionRespond
	ActionToggleView
	ActionRoute
	ActionPropose
	// ActionReadLog rereads the open log from its tail; ActionLoadOlder reads the
	// lines before the oldest loaded one.
	ActionReadLog
	ActionLoadOlder
	// ActionLoadHistory reads the PR history for the current repository view.
	ActionLoadHistory
)

// Action is what a keystroke asks the driver to do. The model itself does no I/O.
type Action struct {
	Kind   ActionKind
	Start  app.StartRequest
	Cancel *task.Record
	// Reset is the FAILED task whose local run the developer confirmed discarding.
	Reset *task.Record
	// Resume is the REVIEWED task whose PR feedback the agent should answer.
	Resume task.Key
	// Respond is the NEEDS_DECISION task to record Answer for, as typed.
	Respond task.Key
	Answer  string
	// Route asks which profile should start an issue. RouteSeq tells its result
	// apart from one the developer has since backed out of.
	Route       app.RouteRequest
	RouteSeq    int
	ProposalSeq int
}

// Model is everything the dashboard draws and everything a keystroke changes.
type Model struct {
	Width, Height int
	Inbox         []app.InboxEntry
	Tasks         []*task.Record
	// Log is the loaded part of the selected task's runner log, oldest line first.
	Log    []string
	LogErr string
	// LogPath is the file Log was read from. Empty means the next read starts over.
	LogPath   string
	LogOpen   bool // Enter on a delegated task reveals its log below the lists.
	LogPaused bool
	// LogRaw shows the log's lines as written instead of cleaned. It lasts for this
	// session and applies to every task's log.
	LogRaw bool
	// LogAtStart means Log reaches back to the first line of the file.
	LogAtStart bool

	// HistoryOpen shows the PR history in place of the dashboard. HistoryLoading is
	// set while App.History runs; HistoryErr is why the last load failed.
	HistoryOpen     bool
	History         []app.HistoryRow
	HistoryWarnings []error
	HistoryErr      string
	HistoryLoading  bool
	HistoryCursor   int
	// HistoryTop is the first row in view.
	HistoryTop int
	// HistoryLogFocus sends the movement keys to the log instead of the rows.
	HistoryLogFocus bool

	// HelpOpen shows the keys and Version in place of the panes.
	HelpOpen bool
	Version  string
	// TaskFile is the path of a task's file, which the reset confirmation names.
	TaskFile func(task.Key) string

	Focus       Pane
	InboxCursor int
	TaskCursor  int
	// LogOffset is how many lines the log is scrolled back from its tail. Zero follows.
	LogOffset int
	// ShowOpen swaps the open pane's log for the selected task's recorded state.
	ShowOpen bool
	// ShowTop is the first wrapped show line in view. Show is read from the top, unlike
	// the log, which follows its tail.
	ShowTop int
	// DecisionTop is the first wrapped question line in view while a decision is asked.
	DecisionTop int

	Mode  Mode
	Input string // the context file or the decision answer being typed
	// Message is the one line under the panes: the last result, or an error.
	Message     string
	Refreshing  bool
	RefreshedAt time.Time
	Warnings    []error
	IssueSource task.Source // empty selects both issue sources

	// AutoOn starts eligible INBOX issues with AutoAgent while fewer than AutoMax
	// runs are active.
	AutoOn    bool
	AutoAgent task.Agent
	AutoMax   int

	// AllRepos shows the inbox from every repository instead of the configured
	// lists. It lasts for this session only. Auto mode waits while it is on, so
	// looking at every repository never starts work outside the configured lists.
	AllRepos bool

	// RouteOn means routing.yaml is in use: auto mode routes each new issue, and
	// the agent choice offers routing for an issue never delegated.
	RouteOn bool

	pending      app.StartRequest  // the delegation being collected
	pendingNew   bool              // the pending issue was never delegated
	routing      bool              // the pending delegation asks for a route
	routeSeq     int               // the route request the footer waits for
	route        app.Decision      // the route on show while ConfirmingRoute
	routeErr     string            // why the route on show failed
	proposalSeq  int               // the branch proposal this prompt waits for
	waiting      map[task.Key]wait // auto starts held back by routing
	cancelTarget task.Key          // the task named when cancellation was requested
	resetTarget  task.Key          // the task named when a reset was requested
	// resetRun is the run the confirmation shows, copied when x was pressed. A refresh
	// can replace the task's run meanwhile; y resets only this one.
	resetRun    *task.Record
	manualTask  bool              // cursor movement stops following the latest delegation
	tried       map[task.Key]bool // issues started this session, by hand or automatically
	starting    map[task.Key]bool // starts and resumes in flight, manual or automatic
	tasksStale  bool              // the last task read failed
	githubFresh bool              // the latest GitHub refresh succeeded and is not being replaced
	answering   bool              // an answer is being recorded
	question    int               // the index in Decisions of the question the prompt shows
	logStart    int64             // the byte offset of the first loaded log line
	logEnd      int64             // where the next read of new log lines starts
	logPartial  int               // trailing Log lines from an unfinished last line, read again
	logShort    int               // the part of a scroll step that waits for older lines
	logFloor    int64             // the first byte of the log the pane may show
	historySeq  int               // the history load whose result the view waits for
	logCeil     int64             // the byte the log the pane may show ends at; 0 is the file end
	hidden      []*task.Record    // delegated tasks from the source IssueSource leaves out
}

// Selected is the highlighted delegated task, or nil when nothing is delegated.
func (m *Model) Selected() *task.Record {
	if m.TaskCursor < 0 || m.TaskCursor >= len(m.Tasks) {
		return nil
	}
	return m.Tasks[m.TaskCursor]
}

// splitBySource keeps the tasks whose issue comes from source, or every task when
// source is empty, and returns the others apart.
func splitBySource(recs []*task.Record, source task.Source) (kept, others []*task.Record) {
	if source == "" {
		return recs, nil
	}
	for _, rec := range recs {
		if rec.IssueSource() == source {
			kept = append(kept, rec)
		} else {
			others = append(others, rec)
		}
	}
	return kept, others
}

func (m *Model) selectedEntry() *app.InboxEntry {
	if m.InboxCursor < 0 || m.InboxCursor >= len(m.Inbox) {
		return nil
	}
	return &m.Inbox[m.InboxCursor]
}

// SetTasks follows the latest start by default and preserves a manually picked task
// across refreshes. The list is sorted by attention, so state changes can reorder it.
// Tasks lists only IssueSource's tasks. The rest stay in hidden, because their runs
// still count toward auto.max_concurrent.
func (m *Model) SetTasks(recs []*task.Record) {
	var selected task.Key
	if cur := m.Selected(); cur != nil {
		selected = cur.Key
	}
	recs, m.hidden = splitBySource(recs, m.IssueSource)
	m.Tasks, m.tasksStale = recs, false
	if !m.manualTask {
		m.TaskCursor = newestTask(recs)
	} else {
		m.TaskCursor = clamp(indexOfTask(recs, selected, m.TaskCursor), len(recs))
	}
	cur := m.Selected()
	if cur == nil || cur.Key != selected {
		// The task the cursor was on is gone, so the pane is about to show another
		// task's log. Follow its tail rather than keep the old task's scroll offset.
		m.LogOffset = 0
		m.LogOpen, m.ShowOpen = false, false
	}
	switch {
	case m.Mode == AnsweringDecision && !asks(cur):
		// The question was answered elsewhere, such as by another dashboard, or the task
		// is gone. Its log takes the pane back.
		m.Mode, m.Input = Browsing, ""
	case m.Mode == AnsweringDecision && !m.LogOpen:
		// The cursor moved to another task, which closed the pane.
		m.Mode, m.Input = Browsing, ""
	case m.Mode == AnsweringDecision && pendingIndex(cur) != m.question:
		// The shown question was answered while the prompt was open. Text typed for it
		// must not be recorded as the answer to the next one.
		if !m.answering {
			m.Message = fmt.Sprintf("%s#%d: that question was answered elsewhere; this is the next one", cur.Repo, cur.Number)
		}
		m.Input, m.DecisionTop, m.question = "", 0, pendingIndex(cur)
	case m.Mode == Browsing && m.LogOpen && !m.answering && asks(cur):
		m.openDecision()
	}
	if !m.logShown() {
		m.LogPaused = false
	}
}

// logShown reports a task log or PR history log is on screen, the views Space
// can pause.
func (m *Model) logShown() bool {
	return !m.HelpOpen && m.Mode == Browsing &&
		(m.HistoryOpen || (m.LogOpen && !m.ShowOpen))
}

// pendingIndex is the index in rec.Decisions of the oldest unanswered question, or -1.
func pendingIndex(rec *task.Record) int {
	return slices.IndexFunc(rec.Decisions, func(d task.Decision) bool { return d.Answer == "" })
}

// asks reports whether rec has a question the developer can answer now.
func asks(rec *task.Record) bool {
	return rec != nil && rec.State == task.NeedsDecision && rec.Pending() != nil
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
	slices.SortStableFunc(entries, func(a, b app.InboxEntry) int {
		if a.Item.Key.Source == task.Linear && b.Item.Key.Source != task.Linear {
			return 1
		}
		if a.Item.Key.Source != task.Linear && b.Item.Key.Source == task.Linear {
			return -1
		}
		return 0
	})
	m.Inbox = entries
	m.InboxCursor = clamp(indexOfEntry(entries, selected, m.InboxCursor), len(entries))
}

func (m *Model) beginRefresh() {
	m.Refreshing = true
	m.githubFresh = false
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
	if k.Kind == KeyRune && k.Rune == ' ' && m.logShown() {
		m.LogPaused = !m.LogPaused
		return Action{}
	}
	m.LogPaused = false
	if k.Kind == KeyCtrlC {
		return Action{Kind: ActionQuit}
	}
	if m.HelpOpen {
		m.HelpOpen = false
		return Action{}
	}
	switch m.Mode {
	case ChoosingAgent:
		return m.chooseAgent(k)
	case TypingContext:
		return m.typeContext(k)
	case ProposingBranch:
		if k.Kind == KeyEscape {
			delete(m.starting, requestKey(m.pending))
			m.Mode, m.Input = Browsing, ""
		}
		return Action{}
	case TypingBranch:
		return m.typeBranch(k)
	case ConfirmingCancel:
		return m.confirmCancel(k)
	case ConfirmingReset:
		return m.confirmReset(k)
	case AnsweringDecision:
		return m.answerDecision(k)
	case Routing:
		if k.Kind == KeyEscape {
			m.Mode = ChoosingAgent
		}
		return Action{}
	case ConfirmingRoute:
		return m.confirmRoute(k)
	}
	if m.HistoryOpen {
		return m.historyKey(k)
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
	case k.Kind == KeyRune && k.Rune == '?':
		m.HelpOpen = true
	case k.Kind == KeyRune && k.Rune == 'p':
		return m.openHistory()
	case m.LogOpen && !m.ShowOpen && k.Kind == KeyRune && k.Rune == 'R':
		m.LogRaw = !m.LogRaw
		// Cleaned and raw lines do not line up, so the new view starts at the tail.
		m.LogPath, m.LogOffset = "", 0
		return Action{Kind: ActionReadLog}
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
		return m.scrollLog(m.logPage())
	case m.LogOpen && m.Focus == TaskPane && (k.Kind == KeyRune && k.Rune == 'd'):
		return m.scrollLog(-m.logPage())
	case m.LogOpen && m.Focus == TaskPane && k.Kind == KeyCtrlU:
		return m.scrollLog(max(m.logPage()/2, 1))
	case m.LogOpen && m.Focus == TaskPane && k.Kind == KeyCtrlD:
		return m.scrollLog(-max(m.logPage()/2, 1))
	case m.LogOpen && m.Focus == TaskPane && k.Kind == KeyRune && k.Rune == 'g':
		return m.scrollTop()
	case m.LogOpen && m.Focus == TaskPane && k.Kind == KeyRune && k.Rune == 'G':
		m.scrollEnd()
	case m.LogOpen && k.Kind == KeyPageUp:
		return m.scrollLog(m.logPage())
	case m.LogOpen && k.Kind == KeyPageDown:
		return m.scrollLog(-m.logPage())
	case m.LogOpen && k.Kind == KeyEnd:
		m.scrollEnd()
	case k.Kind == KeyRune && k.Rune == 'c':
		return m.startCancel()
	case k.Kind == KeyRune && k.Rune == 'x':
		return m.startReset()
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

// showLines is the selected task's render.Show text, wrapped to the pane width. It is
// rendered on each call from the record in memory, so it is never older than the last
// SetTasks.
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
// or toward the top of the show text. A step past the oldest loaded line stops there
// and asks for older lines when the file has them; LoadedOlder takes the rest of the
// step once they arrive.
func (m *Model) scrollLog(by int) Action {
	if m.ShowOpen {
		// A refresh or a resize can shrink the text under a stored ShowTop, so the step
		// starts from the page the frame draws, not from the stale position.
		limit := m.showLimit()
		m.ShowTop = min(max(min(m.ShowTop, limit)-by, 0), limit)
		return Action{}
	}
	want := m.LogOffset + by
	limit := max(len(m.Log)-m.logPage(), 0)
	m.LogOffset, m.logShort = min(max(want, 0), limit), 0
	if want > limit && !m.LogAtStart && m.LogPath != "" {
		m.logShort = want - limit
		return Action{Kind: ActionLoadOlder}
	}
	return Action{}
}

// toFileStart is a scroll step long enough to reach the first line of any log.
const toFileStart = math.MaxInt / 4

func (m *Model) scrollTop() Action {
	if m.ShowOpen {
		m.ShowTop = 0
		return Action{}
	}
	return m.scrollLog(toFileStart)
}

// LoadedOlder puts lines, read from the file at start, before the loaded ones. The
// offset counts back from the tail, so the view holds still, and then it takes the
// rest of the step that asked for them.
func (m *Model) LoadedOlder(lines []string, start int64) Action {
	m.Log = append(lines, m.Log...)
	m.logStart, m.LogAtStart = start, start <= m.logFloor
	return m.scrollLog(m.logShort)
}

// clearLog forgets the loaded log, so the next read starts at the tail of whatever
// log is selected then.
func (m *Model) clearLog() {
	m.Log, m.LogErr, m.LogPath, m.LogAtStart, m.LogOffset = nil, "", "", false, 0
	m.logStart, m.logEnd, m.logPartial, m.logShort, m.logFloor, m.logCeil = 0, 0, 0, 0, 0, 0
}

func (m *Model) scrollEnd() {
	if m.ShowOpen {
		m.ShowTop = m.showLimit()
		return
	}
	m.LogOffset = 0
}

func (m *Model) logPage() int {
	if m.HistoryOpen {
		return m.historyLogPage()
	}
	lay, ok := layoutFor(m.Width, m.Height, true, false)
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
			// ask: it goes straight back to work on the comments.
			m.manualTask = false
			m.Message = fmt.Sprintf("Resuming %s#%d to answer its PR feedback…", e.Item.Key.Repo, e.Item.Key.Number)
			m.starting = mark(m.starting, e.Item.Key)
			return Action{Kind: ActionResume, Resume: e.Item.Key}
		}
		m.pending = app.StartRequest{Repo: e.Item.Key.Repo, Issue: e.Item.Key.Number}
		if e.Item.Key.Source == task.Linear {
			m.pending.Key = e.Item.Key
		}
		m.pendingNew = !e.Delegated
		m.Mode, m.Input = ChoosingAgent, ""
		return Action{}
	}
	rec := m.Selected()
	switch {
	case rec == nil:
		m.Message = "No delegated work yet. Pick an issue on the left."
	case asks(rec):
		m.openDecision()
		m.Message = ""
	default:
		m.LogOpen = true
		m.LogOffset = 0
		m.Message = ""
	}
	return Action{}
}

// openDecision puts the selected task's oldest unanswered question in the lower pane.
// The question is read from the record on each frame, so the prompt always shows the
// next one. The cursor stays on the task, so a newer delegation cannot take the
// prompt away while the developer types.
func (m *Model) openDecision() {
	m.Mode, m.Input = AnsweringDecision, ""
	m.LogOpen, m.ShowOpen = true, false
	m.DecisionTop = 0
	m.question = pendingIndex(m.Selected())
	m.manualTask = true
}

// answerDecision handles a key while the prompt is open. Every rune is text, so an
// answer can start with q or j.
func (m *Model) answerDecision(k Key) Action {
	switch k.Kind {
	case KeyEscape:
		// Answers already recorded are saved, so reopening asks the next question.
		m.Mode, m.Input = Browsing, ""
		m.LogOpen = false
	case KeyBackspace:
		if r := []rune(m.Input); len(r) > 0 {
			m.Input = string(r[:len(r)-1])
		}
	case KeyRune:
		m.Input += string(k.Rune)
	case KeyUp:
		m.scrollDecision(-1)
	case KeyDown:
		m.scrollDecision(1)
	case KeyPageUp:
		m.scrollDecision(-m.decisionPage())
	case KeyPageDown:
		m.scrollDecision(m.decisionPage())
	case KeyEnter:
		rec := m.Selected()
		if m.answering || rec == nil {
			return Action{}
		}
		m.answering = true
		// The last answer relaunches the runner, so the slot is held as for a resume.
		m.starting = mark(m.starting, rec.Key)
		m.Message = fmt.Sprintf("Recording the answer for %s#%d…", rec.Repo, rec.Number)
		return Action{Kind: ActionRespond, Respond: rec.Key, Answer: m.Input}
	}
	return Action{}
}

// Answered applies a recorded answer. The record replaces the task's entry at once, so
// the prompt shows the next question without waiting for the next task read. After the
// last answer the pane goes back to the task's log, following its tail.
func (m *Model) Answered(rec *task.Record) {
	m.answering = false
	for i, cur := range m.Tasks {
		if cur.Key == rec.Key {
			m.Tasks[i] = rec
		}
	}
	if cur := m.Selected(); cur == nil || cur.Key != rec.Key {
		return
	}
	if asks(rec) {
		if m.Mode == AnsweringDecision {
			m.Input, m.DecisionTop, m.question = "", 0, pendingIndex(rec)
		}
		return
	}
	if m.Mode == AnsweringDecision || (m.Mode == Browsing && m.LogOpen) {
		m.Mode, m.Input = Browsing, ""
		m.LogOpen, m.ShowOpen, m.LogOffset = true, false, 0
	}
}

// AnswerFailed keeps the prompt and the typed text, so the developer can fix the
// answer and press Enter again.
func (m *Model) AnswerFailed() { m.answering = false }

// decisionLines is the selected task's pending question as render.Blocked prints it,
// wrapped to the pane width.
func (m *Model) decisionLines() []string {
	rec := m.Selected()
	if rec == nil || rec.Pending() == nil {
		return nil
	}
	var b bytes.Buffer
	render.Blocked(&b, rec.Pending().Question)
	var out []string
	for _, l := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
		out = append(out, wrapLine(printable(l), m.Width)...)
	}
	return out
}

// decisionPage is how many question lines the prompt shows: the pane less its heading
// and the answer line. At the smallest height the heading is dropped first.
func (m *Model) decisionPage() int {
	lay, ok := layoutFor(m.Width, m.Height, true, true)
	if !ok {
		return 1
	}
	if lay.logHeight < 3 {
		return max(lay.logHeight-1, 1)
	}
	return lay.logHeight - 2
}

// decisionLimit is the last DecisionTop that still fills the pane.
func (m *Model) decisionLimit() int {
	return max(len(m.decisionLines())-m.decisionPage(), 0)
}

func (m *Model) scrollDecision(by int) {
	limit := m.decisionLimit()
	m.DecisionTop = min(max(min(m.DecisionTop, limit)+by, 0), limit)
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
		m.pending.Agent, m.routing, m.Mode, m.Input = task.Claude, false, TypingContext, ""
	case k.Kind == KeyRune && k.Rune == '2':
		m.pending.Agent, m.routing, m.Mode, m.Input = task.Codex, false, TypingContext, ""
	case k.Kind == KeyRune && k.Rune == '3' && m.offersRoute():
		m.pending.Agent, m.routing, m.Mode, m.Input = "", true, TypingContext, ""
	}
	return Action{}
}

// offersRoute reports whether the agent choice offers routing. A FAILED issue keeps
// the profile it ran with, so only an issue never delegated is routed.
func (m *Model) offersRoute() bool { return m.RouteOn && m.pendingNew }

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
	case k.Kind == KeyEnter && m.routing:
		m.pending.ContextFile = strings.TrimSpace(m.Input)
		m.Mode, m.Input = Routing, ""
		m.routeSeq++
		req := app.RouteRequest{Key: m.pending.Key, Repo: m.pending.Repo, Issue: m.pending.Issue, ContextFile: m.pending.ContextFile}
		return Action{Kind: ActionRoute, Route: req, RouteSeq: m.routeSeq}
	case k.Kind == KeyEnter:
		req := m.pending
		req.ContextFile = strings.TrimSpace(m.Input)
		return m.prepareBranch(req)
	}
	return Action{}
}

func (m *Model) prepareBranch(req app.StartRequest) Action {
	if !m.pendingNew {
		m.Mode, m.Input = Browsing, ""
		return m.startPending(req)
	}
	m.pending = req
	m.Mode, m.Input = ProposingBranch, ""
	m.starting = mark(m.starting, requestKey(req))
	m.proposalSeq++
	return Action{Kind: ActionPropose, Start: req, ProposalSeq: m.proposalSeq}
}

func (m *Model) Proposed(seq int, p app.BranchProposal, err error) {
	if m.Mode != ProposingBranch || seq != m.proposalSeq {
		return
	}
	m.Mode, m.Input = TypingBranch, p.Name
	if err != nil {
		m.Message = "! branch proposal: " + err.Error() + "; enter a branch name"
	} else if p.Uncertain {
		m.Message = "! branch needs your choice: " + p.Reason
	} else {
		m.Message = "Proposed branch: " + p.Reason
	}
}

func (m *Model) typeBranch(k Key) Action {
	switch k.Kind {
	case KeyEscape:
		delete(m.starting, requestKey(m.pending))
		m.Mode, m.Input = Browsing, ""
	case KeyBackspace:
		if r := []rune(m.Input); len(r) > 0 {
			m.Input = string(r[:len(r)-1])
		}
	case KeyRune:
		m.Input += string(k.Rune)
	case KeyEnter:
		name := strings.TrimSpace(m.Input)
		if !task.ValidBranchName(name) {
			m.Message = "! enter a valid Git branch name"
			return Action{}
		}
		req := m.pending
		req.BranchName = name
		m.Mode, m.Input, m.manualTask = Browsing, "", false
		return m.startPending(req)
	}
	return Action{}
}

func (m *Model) startPending(req app.StartRequest) Action {
	k := requestKey(req)
	m.tried, m.starting = mark(m.tried, k), mark(m.starting, k)
	return Action{Kind: ActionStart, Start: req}
}

// Routed shows a route result, unless the developer has backed out of it.
func (m *Model) Routed(seq int, d app.Decision, err error) {
	if m.Mode != Routing || seq != m.routeSeq {
		return
	}
	m.Mode, m.route, m.routeErr = ConfirmingRoute, d, ""
	if err != nil {
		m.routeErr = err.Error()
	}
}

// confirmRoute starts the shown profile on Enter. Esc goes back to the agent choice,
// where 1 and 2 still override the route.
func (m *Model) confirmRoute(k Key) Action {
	switch {
	case k.Kind == KeyEscape:
		m.Mode = ChoosingAgent
	case k.Kind == KeyEnter && m.route.Profile != nil && m.routeErr == "":
		req := m.pending
		req.Agent, req.Profile, req.RouteReason = "", m.route.Profile, m.route.Reason
		return m.prepareBranch(req)
	}
	return Action{}
}

// startReset asks before discarding the selected FAILED task's local run. Whether its
// runner is still alive is App.Reset's check: the pid of a FAILED task stays recorded
// until a refresh sees the process gone.
func (m *Model) startReset() Action {
	rec := m.Selected()
	switch {
	case rec == nil:
		m.Message = "No delegated work to reset."
	case rec.State != task.Failed:
		m.Message = fmt.Sprintf("%s#%d is %s; only a FAILED task can be reset", rec.Repo, rec.Number, rec.State)
	default:
		run := *rec
		if rec.Delegation != nil {
			d := *rec.Delegation
			run.Delegation = &d
		}
		m.resetTarget, m.resetRun = rec.Key, &run
		m.Mode = ConfirmingReset
	}
	return Action{}
}

func (m *Model) confirmReset(k Key) Action {
	run, fits := m.resetRun, resetFits(*m)
	m.Mode, m.resetTarget, m.resetRun = Browsing, task.Key{}, nil
	if k.Kind != KeyRune || k.Rune != 'y' || run == nil {
		return Action{}
	}
	if !fits {
		m.Message = "! Enlarge the terminal to see everything reset deletes, then press x again"
		return Action{}
	}
	// App.Reset refuses when the task's run is no longer this one.
	m.Message = fmt.Sprintf("Resetting %s#%d…", run.Repo, run.Number)
	return Action{Kind: ActionReset, Reset: run}
}

// ResetDone shows a reset issue in INBOX as never started, so only a manual start
// picks it up, until the next refresh reads it from the task file.
func (m *Model) ResetDone(key task.Key) {
	for i := range m.Inbox {
		if m.Inbox[i].Item.Key == key {
			m.Inbox[i].Delegated, m.Inbox[i].State, m.Inbox[i].Reset = false, task.Discovered, true
		}
	}
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
