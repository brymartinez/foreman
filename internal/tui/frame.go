package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/brymartinez/foreman/internal/render"
	"github.com/brymartinez/foreman/internal/task"
)

// focusMark flags the pane the arrow keys move in.
const focusMark = "▸ "

const (
	reverseOn = "\x1b[7m"
	dim       = "\x1b[2m"
	cyan      = "\x1b[36m"
	green     = "\x1b[32m"
	yellow    = "\x1b[33m"
	red       = "\x1b[31m"
	// fgDefault and dimOff end one color or one attribute. A full reset would also
	// end the reverse video a selected line is drawn with, and the rest of that line
	// would lose its highlight.
	fgDefault  = "\x1b[39m"
	dimOff     = "\x1b[22m"
	attrsReset = "\x1b[0m"
)

// Frame draws the whole screen: exactly m.Height lines, each at most m.Width visible
// columns. It is pure, so the layout is tested without a terminal. Text is truncated
// before any escape sequence is added, so the width arithmetic never counts escapes.
func Frame(m Model) []string {
	lay, ok := layoutFor(m.Width, m.Height, m.LogOpen, m.Mode == AnsweringDecision)
	if !ok {
		return tooSmall(m.Width, m.Height)
	}
	if m.HistoryOpen && !m.HelpOpen {
		return historyFrame(m)
	}
	lines := make([]string, 0, m.Height)
	lines = append(lines, colored(pad(header(m), m.Width), cyan))
	if m.HelpOpen {
		return append(lines, helpLines(m, m.Height-1)...)
	}

	right := m.Width - lay.leftWidth - 1
	left := paneLines(inboxPane(m), lay.leftWidth, lay.topHeight, m.Focus == InboxPane, m.InboxCursor)
	// The pane spends its first row on its heading, so a task has the rest to fill.
	rightLines := paneLines(taskPane(m, right, lay.topHeight-1), right, lay.topHeight, m.Focus == TaskPane, m.TaskCursor)
	for i := range lay.topHeight {
		lines = append(lines, left[i]+colored("│", cyan)+rightLines[i])
	}
	if m.LogOpen {
		lines = append(lines, colored(strings.Repeat("─", m.Width), cyan))
		lines = append(lines, logPane(m, lay.logHeight)...)
	}
	lines = append(lines, messageLine(m))
	lines = append(lines, dimmed(pad(truncate(footer(m), m.Width), m.Width)))
	return lines
}

// messageLine is the last result or error, under the panes.
func messageLine(m Model) string {
	message := pad(truncate(m.Message, m.Width), m.Width)
	if strings.HasPrefix(m.Message, "!") {
		return colored(message, red)
	} else if m.Message != "" {
		return colored(message, green)
	}
	return message
}

func tooSmall(width, height int) []string {
	lines := make([]string, max(height, 1))
	msg := fmt.Sprintf("foreman needs at least %dx%d; this terminal is %dx%d", minWidth, minHeight, width, height)
	lines[len(lines)/2] = truncate(msg, max(width, 1))
	return lines
}

func header(m Model) string {
	parts := []string{fmt.Sprintf("foreman · %d delegated · %d in the inbox", len(m.Tasks), len(m.Inbox))}
	if m.AutoOn && m.AllRepos {
		parts = append(parts, "auto paused")
	} else if m.AutoOn {
		parts = append(parts, fmt.Sprintf("auto on %d/%d", m.active(), m.AutoMax))
	} else {
		parts = append(parts, "auto off")
	}
	switch {
	case m.Refreshing:
		parts = append(parts, "refreshing…")
	case !m.RefreshedAt.IsZero():
		parts = append(parts, "refreshed "+m.RefreshedAt.Format(time.TimeOnly))
	}
	if n := len(m.Warnings); n > 0 {
		parts = append(parts, fmt.Sprintf("! %d %s", n, plural(n, "warning", "warnings")))
	}
	return strings.Join(parts, " · ")
}

// row is one list entry: its lines, drawn together and selectable as a unit. The
// text itself keeps the terminal's foreground, so it reads on a light theme and a
// dark one alike; accent is the one word drawn in color, the task's state.
type row struct {
	lines  []string
	accent string
	// accentLine is where the accent sits: the identity line, or the line the agent
	// and the state wrap onto in a narrow pane.
	accentLine int
	color      string
}

// cue is one line's accent, carried beside the line while the pane is laid out.
type cue struct{ word, color string }

// pane is a titled list of rows, with the text shown when it has none.
type pane struct {
	title string
	rows  []row
	empty string
}

func inboxPane(m Model) pane {
	p := pane{title: "INBOX · configured repos", empty: "Nothing to delegate. Press r to refresh."}
	if m.AllRepos {
		p.title = "INBOX · all repos"
	}
	for _, e := range m.Inbox {
		k := e.Item.Key
		detail := strings.Join(e.Changes, ", ")
		if w, ok := m.waiting[k]; ok {
			detail = render.JoinFacts("waiting: "+w.reason, detail)
		}
		r := row{lines: []string{fmt.Sprintf("%s#%d  %s", k.Repo, k.Number, e.Item.Title)}}
		if e.Delegated {
			// INBOX only ever offers delegated work the developer can move, so its state
			// says which key is waiting: start again, or answer the PR.
			detail = render.JoinFacts(string(e.State), detail)
			r.accent, r.accentLine, r.color = string(e.State), 1, taskColor(e.State)
		}
		r.lines = append(r.lines, "  "+detail)
		p.rows = append(p.rows, r)
	}
	return p
}

// markerWidth is the "> " or "  " every pane line carries before its text.
const markerWidth = 2

const (
	// taskRowLines is the pane body a task needs at its narrowest: the identity, the
	// agent and state wrapped under it, the title, one line of PR status, and a
	// pending question. layoutFor keeps the pane at least this tall, so every task
	// fits the pane it is drawn in; the pane scrolls by whole rows, not within one.
	taskRowLines = 5
	// prRowLines caps the PR status, which is the whole row at the narrowest pane.
	// Without the cap one task could fill a tall pane on its own.
	prRowLines = 3
)

func taskPane(m Model, width, body int) pane {
	p := pane{title: "DELEGATED WORK", empty: "No delegated work yet."}
	for _, rec := range m.Tasks {
		rows := render.TaskRows(rec)
		lines := []string{rows[0]}
		stateLine := 0
		// status pads the identity to a fixed column so its rows line up. That padding
		// does not fit a narrow pane, and truncating it would drop the state, so wrap
		// the agent and the state onto their own line instead.
		if textWidth(rows[0])+markerWidth > width {
			identity, agent, state := render.TaskHead(rec)
			lines, stateLine = []string{identity, "  " + agent + " · " + state}, 1
		}
		lines = append(lines, "  "+rows[1]) // the title
		if body > taskRowLines && rec.Delegation.Branch != "" {
			lines = append(lines, "  branch "+rec.Delegation.Branch)
		}
		q := pendingQuestion(rec)
		if len(rows) > 2 {
			left := body - len(lines)
			if q != "" {
				left--
			}
			for _, part := range prRows(rec, width-markerWidth-2, min(max(left, 1), prRowLines)) {
				lines = append(lines, "  "+part)
			}
		}
		if q != "" {
			lines = append(lines, "  "+q)
		}
		p.rows = append(p.rows, row{lines: lines, accent: string(rec.State), accentLine: stateLine, color: taskColor(rec.State)})
	}
	return p
}

// prRows is the PR status wrapped into at most n lines. R3 asks for the PR with its CI
// and review badges, and at 60 columns a task with a waiting question has one line for
// them, so the reviewer goes first and then the failing checks' names, rather than a
// truncation cutting the badges off the end.
func prRows(rec *task.Record, width, n int) []string {
	s := render.TaskPR(rec)
	shortest := render.JoinFacts(s.PR, s.CIState, s.ReviewState)
	for _, row := range []string{
		render.JoinFacts(s.PR, s.Reviewer, s.CI, s.Review),
		render.JoinFacts(s.PR, s.CI, s.Review),
		shortest,
	} {
		if parts := wrap(row, width, n+1); len(parts) <= n {
			return parts
		}
	}
	// Even the states do not fit, so the last line is truncated.
	return wrap(shortest, width, n)
}

// wrap keeps every part of a PR's status row visible in the narrow task pane. It
// returns at most n lines, leaving the rest on the last one for truncate to cut.
func wrap(s string, width, n int) []string {
	remaining := printable(s)
	var lines []string
	for textWidth(remaining) > width && len(lines) < n-1 {
		cut := breakAt(remaining, width)
		lines = append(lines, remaining[:cut])
		remaining = strings.TrimLeft(remaining[cut:], " ")
	}
	return append(lines, remaining)
}

// breakAt is where to cut s so the line before the cut is at most width columns. It
// prefers the last space that fits, and cuts mid-word when none does.
func breakAt(s string, width int) int {
	n, lastSpace, limit := 0, 0, len(s)
	for i, r := range s {
		w := runeWidth(r)
		if n+w > width {
			limit = i
			break
		}
		if r == ' ' {
			lastSpace = i
		}
		n += w
	}
	if lastSpace > 0 {
		return lastSpace
	}
	return limit
}

func pendingQuestion(rec *task.Record) string {
	if rec.State != task.NeedsDecision {
		return ""
	}
	p := rec.Pending()
	if p == nil {
		return "? decision needed"
	}
	return "? " + p.Question.Issue
}

// paneLines renders one pane into exactly height lines of width columns. The rows
// scroll so the cursor's row is always visible.
func paneLines(p pane, width, height int, focused bool, cursor int) []string {
	title := p.title
	if focused {
		title = focusMark + title
	}
	out := []string{colored(pad(truncate(title, width), width), cyan)}
	body := height - 1

	// Every line is built plain, and color goes on at the end, so a selected row can
	// be drawn in reverse video alone.
	var lines []string
	var cues []cue
	selectedFrom, selectedTo := 0, 0
	for i, r := range p.rows {
		if i == cursor {
			selectedFrom = len(lines)
		}
		for j, l := range r.lines {
			marker := "  "
			if j == 0 && i == cursor {
				marker = "> "
			}
			lines = append(lines, pad(truncate(marker+l, width), width))
			c := cue{}
			if j == r.accentLine {
				c = cue{word: r.accent, color: r.color}
			}
			cues = append(cues, c)
		}
		if i == cursor {
			selectedTo = len(lines)
		}
		if i < len(p.rows)-1 {
			lines = append(lines, strings.Repeat(" ", width))
			cues = append(cues, cue{})
		}
	}
	if len(p.rows) == 0 {
		lines = append(lines, pad(truncate("  "+p.empty, width), width))
		cues = append(cues, cue{})
	}

	start := 0
	if selectedTo > body {
		start = min(selectedFrom, len(lines)-body)
	}
	if start < 0 {
		start = 0
	}
	for i := start; i < len(lines) && len(out) < height; i++ {
		if focused && i >= selectedFrom && i < selectedTo {
			// Reverse video and nothing else: a foreground color under it would draw
			// the text in the terminal's background color.
			out = append(out, reverseOn+lines[i]+attrsReset)
			continue
		}
		out = append(out, highlight(lines[i], cues[i].word, cues[i].color))
	}
	for len(out) < height {
		out = append(out, strings.Repeat(" ", width))
	}
	return out
}

// logPane renders the log heading and the visible slice of the selected task's log.
func logPane(m Model, height int) []string {
	if m.Mode == AnsweringDecision {
		return decisionPane(m, height)
	}
	if m.ShowOpen {
		return showPane(m, height)
	}
	title, body := "LOG · no task selected", []string{}
	switch {
	case m.Selected() == nil:
		body = []string{"  Delegate an issue, or press tab and pick a task to follow its log."}
	default:
		rec := m.Selected()
		title = fmt.Sprintf("LOG · %s#%d · %s", rec.Repo, rec.Number, rec.Delegation.RunnerLog)
		if m.LogRaw {
			title = fmt.Sprintf("LOG · raw · %s#%d · %s", rec.Repo, rec.Number, rec.Delegation.RunnerLog)
		}
		switch {
		case m.LogErr != "":
			body = []string{"  " + m.LogErr}
		case len(m.Log) == 0:
			body = []string{"  No log lines yet."}
		}
	}
	if m.LogOffset > 0 {
		title += fmt.Sprintf(" · scrolled back %d (G to follow)", m.LogOffset)
	}
	out := []string{colored(pad(truncate(title, m.Width), m.Width), cyan)}
	visible := height - 1
	if len(body) == 0 {
		end := max(len(m.Log)-m.LogOffset, 0)
		body = m.Log[max(end-visible, 0):end]
	}
	for _, l := range body {
		if len(out) >= height {
			break
		}
		out = append(out, pad(truncate(l, m.Width), m.Width))
	}
	for len(out) < height {
		out = append(out, strings.Repeat(" ", m.Width))
	}
	return out
}

// helpKey is one row of the help: the keys, what they do, and a short form for a
// terminal too short to list every row.
type helpKey struct{ keys, does, short string }

var helpKeys = []helpKey{
	{"j/k, up/down", "move in the focused pane", "j/k move"},
	{"h/l, left/right, tab", "switch between inbox and delegated work", "h/l pane"},
	{"enter", "inbox: start or resume an issue · tasks: open the log or the question", "enter select"},
	{"esc", "close the lower pane", "esc close"},
	{"s", "switch the open pane between the log and the task's state", "s show/log"},
	{"R", "switch the log between cleaned and raw lines", "R raw log"},
	{"space", "pause or resume log redraws; navigation also resumes", "space pause/resume"},
	{"u/d, pgup/pgdn", "scroll the log a page; past the top loads older lines", "u/d page"},
	{"ctrl-u/ctrl-d", "scroll the log, or move the PR history, half a page", "ctrl-u/d half page"},
	{"g/G, end", "go to the log's first line or follow its tail", "g/G log start/end"},
	{"c", "cancel the selected task's run", "c cancel"},
	{"p", "open the PR history of merged and closed PRs; esc returns", "p PR history"},
	{"ctrl-f/ctrl-b", "PR history: move a full page", "ctrl-f/b page"},
	{"a", "turn auto mode on or off, kept for the next launch", "a auto"},
	{"v", "show the inbox for the configured repos or all repos", "v view"},
	{"r", "refresh from GitHub", "r refresh"},
	{"?", "show this help", "? help"},
	{"q, ctrl-c", "quit", "q quit"},
}

// helpLines fills the screen under the header with the keys, the version, and how to
// close the help. When the rows do not fit, the keys flow together in their short form.
func helpLines(m Model, height int) []string {
	out := []string{colored(pad(truncate("KEYS · foreman "+m.Version, m.Width), m.Width), cyan)}
	var body []string
	if len(helpKeys)+2 <= height {
		width := 0
		for _, k := range helpKeys {
			width = max(width, textWidth(k.keys))
		}
		for _, k := range helpKeys {
			body = append(body, "  "+pad(k.keys, width)+"  "+k.does)
		}
	} else {
		short := make([]string, len(helpKeys))
		for i, k := range helpKeys {
			short[i] = k.short
		}
		body = wrap(strings.Join(short, " · "), m.Width-2, height-2)
		for i := range body {
			body[i] = "  " + body[i]
		}
	}
	for _, l := range body {
		if len(out) >= height-1 {
			break
		}
		out = append(out, pad(truncate(l, m.Width), m.Width))
	}
	for len(out) < height-1 {
		out = append(out, strings.Repeat(" ", m.Width))
	}
	return append(out, dimmed(pad(truncate("press any key to close the help", m.Width), m.Width)))
}

// showPane renders the selected task's render.Show text from ShowTop down.
func showPane(m Model, height int) []string {
	title, body := "SHOW · no task selected", []string{"  Delegate an issue, or press tab and pick a task to see its state."}
	if rec := m.Selected(); rec != nil {
		title = fmt.Sprintf("SHOW · %s#%d · %s", rec.Repo, rec.Number, rec.State)
		lines := m.showLines()
		visible := height - 1
		// A resize or a shorter record can leave ShowTop past the end, so it is clamped
		// here rather than trusted.
		top := min(m.ShowTop, max(len(lines)-visible, 0))
		end := min(top+visible, len(lines))
		if len(lines) > visible {
			title += fmt.Sprintf(" · lines %d-%d of %d", top+1, end, len(lines))
		}
		body = lines[top:end]
	}
	out := []string{colored(pad(truncate(title, m.Width), m.Width), cyan)}
	for _, l := range body {
		if len(out) >= height {
			break
		}
		out = append(out, pad(truncate(l, m.Width), m.Width))
	}
	for len(out) < height {
		out = append(out, strings.Repeat(" ", m.Width))
	}
	return out
}

// decisionPane renders the selected task's pending question from DecisionTop down, as
// render.Blocked prints it, with the answer line always last.
func decisionPane(m Model, height int) []string {
	rec := m.Selected()
	if rec == nil || rec.Pending() == nil {
		// SetTasks leaves the prompt once nothing is pending, so this lasts one frame.
		return showPane(m, height)
	}
	lines := m.decisionLines()
	visible := m.decisionPage()
	top := min(m.DecisionTop, max(len(lines)-visible, 0))
	end := min(top+visible, len(lines))
	title := fmt.Sprintf("DECISION · %s#%d", rec.Repo, rec.Number)
	if i, n := questionNumber(rec); n > 1 {
		title += fmt.Sprintf(" · question %d of %d", i, n)
	}
	if len(lines) > visible {
		title += fmt.Sprintf(" · lines %d-%d of %d", top+1, end, len(lines))
	}
	var out []string
	if height >= 3 {
		out = append(out, colored(pad(truncate(title, m.Width), m.Width), cyan))
	}
	for _, l := range lines[top:end] {
		out = append(out, pad(truncate(l, m.Width), m.Width))
	}
	for len(out) < height-1 {
		out = append(out, strings.Repeat(" ", m.Width))
	}
	return append(out, pad(lastColumns("> "+printable(m.Input)+"█", m.Width), m.Width))
}

// questionNumber is which question of the current batch is asked, and the batch size.
// Answers the runner has not delivered yet belong to the batch, so the count holds
// across Esc and a reopen.
func questionNumber(rec *task.Record) (int, int) {
	answered := 0
	for _, d := range rec.Decisions {
		if d.Answer != "" && d.DeliveredAt.IsZero() {
			answered++
		}
	}
	return answered + 1, answered + rec.PendingCount()
}

// lastColumns keeps the end of s, where the cursor is, when s is wider than width.
func lastColumns(s string, width int) string {
	r := []rune(s)
	for len(r) > 0 && textWidth(string(r)) > width {
		r = r[1:]
	}
	return string(r)
}

func footer(m Model) string {
	if m.LogPaused {
		return "PAUSED · space or navigation resumes"
	}
	switch m.Mode {
	case ChoosingAgent:
		if m.offersRoute() {
			return fmt.Sprintf("Delegate %s#%d with:  [1] Claude   [2] Codex   [3] Route   esc cancel", m.pending.Repo, m.pending.Issue)
		}
		return fmt.Sprintf("Delegate %s#%d with:  [1] Claude   [2] Codex   esc cancel", m.pending.Repo, m.pending.Issue)
	case Routing:
		return fmt.Sprintf("Choosing a profile for %s#%d…   esc back", m.pending.Repo, m.pending.Issue)
	case ConfirmingRoute:
		return routeHints(m)
	case TypingContext:
		return "Context file (enter to skip): " + m.Input + "█"
	case ConfirmingCancel:
		if m.cancelTarget == (task.Key{}) {
			return "Nothing to cancel. [y/n]"
		}
		return fmt.Sprintf("Cancel %s#%d and stop its agent? [y/n]", m.cancelTarget.Repo, m.cancelTarget.Number)
	case AnsweringDecision:
		return answerHints(m)
	}
	if m.HistoryOpen {
		return historyHints(m)
	}
	if m.LogOpen && m.Focus == TaskPane {
		if textWidth(hintsLogLong) > m.Width {
			return hintsLogShort
		}
		return hintsLogLong
	}
	if m.LogOpen {
		if textWidth(hintsOpenLong) > m.Width {
			return hintsOpenShort
		}
		return hintsOpenLong
	}
	if textWidth(hintsLong) > m.Width {
		return hintsShort
	}
	return hintsLong
}

// historyHints are the history's keys: the list's, or the detail log's when it has focus.
func historyHints(m Model) string {
	long, short := hintsHistoryLong, hintsHistoryShort
	switch {
	case m.HistoryLogFocus:
		long, short = hintsHistoryLogLong, hintsHistoryLogShort
	case m.HistoryDetail:
		long, short = hintsHistoryDetailLong, hintsHistoryDetailShort
	}
	if textWidth(long) > m.Width {
		return short
	}
	return long
}

// routeHints show the routed profile, or why nothing can start.
func routeHints(m Model) string {
	ref := fmt.Sprintf("%s#%d", m.pending.Repo, m.pending.Issue)
	switch d := m.route; {
	case m.routeErr != "":
		return fmt.Sprintf("Route %s: ! %s   esc back", ref, printable(m.routeErr))
	case d.Profile == nil:
		return fmt.Sprintf("Route %s waits: %s   esc back", ref, printable(d.Wait))
	default:
		p := d.Profile
		return fmt.Sprintf("Route %s: %s (%s; plan %s, implement %s). %s   enter start   esc back", ref, p.Name, p.Agent,
			render.ModelText(p.PlanModel, p.PlanReasoningEffort), render.ModelText(p.Model, p.ReasoningEffort), printable(d.Reason))
	}
}

// answerHints say what Enter does on an empty answer, naming the recommended option.
func answerHints(m Model) string {
	long, short := hintsAnswerLong, hintsAnswerShort
	if rec := m.Selected(); rec != nil && rec.Pending() != nil {
		if o, ok := rec.Pending().Question.Recommended(); ok {
			label := printable(strings.TrimSpace(o.Label))
			long = fmt.Sprintf("type an option label or an answer · enter for %s · up/down scroll · esc close", label)
			short = fmt.Sprintf("label or answer · enter for %s · up/down · esc close", label)
		}
	}
	if textWidth(long) > m.Width {
		return short
	}
	return long
}

const (
	hintsAnswerLong  = "type an option label or an answer · enter to record · up/down scroll · esc close"
	hintsAnswerShort = "label or answer · enter · up/down · esc close"
	hintsLong        = "j/k move · h/l pane · enter select · c cancel · a auto · v view · r refresh · ? help · q quit"
	hintsShort       = "j/k move · h/l pane · enter · v view · ? help · q quit"
	hintsLogLong     = "j/k task · u/d log page · ctrl-u/d half page · g/G ends · s show/log · R raw · esc close · ? help"
	hintsLogShort    = "j/k task · u/d log · g/G ends · s show/log · ? help"
	// hintsOpen are for INBOX focus while the pane is open, where s still switches it.
	hintsOpenLong  = "j/k move · h/l pane · enter select · s show/log · R raw · esc close · c cancel · a auto · v view · ? help · q quit"
	hintsOpenShort = "j/k move · h/l pane · s show/log · esc close · ? help"

	hintsHistoryLong        = "j/k move · ctrl-f/b page · ctrl-d/u half page · enter details · v view · r reload · esc back · ? help · q quit"
	hintsHistoryShort       = "j/k move · ctrl-f/b page · enter details · esc back · ? help"
	hintsHistoryDetailLong  = "j/k move · ctrl-f/b page · ctrl-d/u half page · tab log · R raw · esc close · ? help"
	hintsHistoryDetailShort = "j/k move · tab log · esc close · ? help"
	hintsHistoryLogLong     = "j/k line · u/d page · ctrl-u/d half page · g/G ends · tab rows · R raw · esc close · ? help"
	hintsHistoryLogShort    = "j/k line · u/d page · g/G ends · tab rows · esc close"
)

// truncate cuts s to at most width columns, ending in an ellipsis when it cut. A wide
// rune that would straddle the last column is dropped, so the line never spills into
// the pane beside it.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = printable(s)
	if textWidth(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		w := runeWidth(r)
		if n+w > width-1 { // one column is kept for the ellipsis
			break
		}
		b.WriteRune(r)
		n += w
	}
	return b.String() + "…"
}

// printable removes terminal controls from task, issue, and runner text before it
// reaches the terminal. The frame adds its own styling only after truncation.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ReplaceAll(s, "\t", "    "))
}

func pad(s string, width int) string {
	if n := width - textWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func dimmed(s string) string { return dim + s + dimOff }

func colored(s, color string) string { return color + s + fgDefault }

// highlight draws word in color inside line, which is already truncated and padded.
// The escapes go in after all of the width arithmetic, so they are never counted as
// columns. A word a truncation cut is not there, and the line stays plain.
func highlight(line, word, color string) string {
	if word == "" || color == "" {
		return line
	}
	i := strings.LastIndex(line, word)
	if i < 0 {
		return line
	}
	return line[:i] + colored(word, color) + line[i+len(word):]
}

func taskColor(state task.State) string {
	switch state {
	case task.NeedsDecision, task.ReadyForHuman, task.Reviewed:
		return yellow
	case task.Failed:
		return red
	case task.Working, task.Reviewing:
		return green
	}
	return ""
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
