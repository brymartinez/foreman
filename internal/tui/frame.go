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
	reverseOn  = "\x1b[7m"
	dim        = "\x1b[2m"
	cyan       = "\x1b[36m"
	green      = "\x1b[32m"
	yellow     = "\x1b[33m"
	red        = "\x1b[31m"
	attrsReset = "\x1b[0m"
)

// Frame draws the whole screen: exactly m.Height lines, each at most m.Width visible
// columns. It is pure, so the layout is tested without a terminal. Text is truncated
// before any escape sequence is added, so the width arithmetic never counts escapes.
func Frame(m Model) []string {
	lay, ok := layoutFor(m.Width, m.Height, m.LogOpen)
	if !ok {
		return tooSmall(m.Width, m.Height)
	}
	lines := make([]string, 0, m.Height)
	lines = append(lines, colored(pad(header(m), m.Width), cyan))

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
	message := pad(truncate(m.Message, m.Width), m.Width)
	if strings.HasPrefix(m.Message, "!") {
		message = colored(message, red)
	} else if m.Message != "" {
		message = colored(message, green)
	}
	lines = append(lines, message)
	lines = append(lines, dimmed(pad(truncate(footer(m), m.Width), m.Width)))
	return lines
}

func tooSmall(width, height int) []string {
	lines := make([]string, max(height, 1))
	msg := fmt.Sprintf("foreman needs at least %dx%d; this terminal is %dx%d", minWidth, minHeight, width, height)
	lines[len(lines)/2] = truncate(msg, max(width, 1))
	return lines
}

func header(m Model) string {
	parts := []string{fmt.Sprintf("foreman · %d delegated · %d in the inbox", len(m.Tasks), len(m.Inbox))}
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

// row is one list entry: its lines, drawn together and selectable as a unit.
type row struct {
	lines []string
	color string
}

// pane is a titled list of rows, with the text shown when it has none.
type pane struct {
	title string
	rows  []row
	empty string
}

func inboxPane(m Model) pane {
	p := pane{title: "INBOX", empty: "Nothing to delegate. Press r to refresh."}
	for _, e := range m.Inbox {
		k := e.Item.Key
		p.rows = append(p.rows, row{lines: []string{
			fmt.Sprintf("%s#%d  %s", k.Repo, k.Number, e.Item.Title),
			"  " + strings.Join(e.Changes, ", "),
		}})
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
		// status pads the identity to a fixed column so its rows line up. That padding
		// does not fit a narrow pane, and truncating it would drop the state, so wrap
		// the agent and the state onto their own line instead.
		if textWidth(rows[0])+markerWidth > width {
			identity, agent, state := render.TaskHead(rec)
			lines = []string{identity, "  " + agent + " · " + state}
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
		p.rows = append(p.rows, row{lines: lines, color: taskColor(rec.State)})
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

	var lines []string
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
			line := pad(truncate(marker+l, width), width)
			if r.color != "" {
				line = colored(line, r.color)
			}
			lines = append(lines, line)
		}
		if i == cursor {
			selectedTo = len(lines)
		}
		if i < len(p.rows)-1 {
			lines = append(lines, strings.Repeat(" ", width))
		}
	}
	if len(p.rows) == 0 {
		lines = append(lines, pad(truncate("  "+p.empty, width), width))
	}

	start := 0
	if selectedTo > body {
		start = min(selectedFrom, len(lines)-body)
	}
	if start < 0 {
		start = 0
	}
	for i := start; i < len(lines) && len(out) < height; i++ {
		l := lines[i]
		if focused && i >= selectedFrom && i < selectedTo {
			l = reverseOn + l + attrsReset
		}
		out = append(out, l)
	}
	for len(out) < height {
		out = append(out, strings.Repeat(" ", width))
	}
	return out
}

// logPane renders the log heading and the visible slice of the selected task's log.
func logPane(m Model, height int) []string {
	title, body := "LOG · no task selected", []string{}
	switch {
	case m.Selected() == nil:
		body = []string{"  Delegate an issue, or press tab and pick a task to follow its log."}
	default:
		rec := m.Selected()
		title = fmt.Sprintf("LOG · %s#%d · %s", rec.Repo, rec.Number, rec.Delegation.RunnerLog)
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

func footer(m Model) string {
	switch m.Mode {
	case ChoosingAgent:
		return fmt.Sprintf("Delegate %s#%d with:  [1] Claude   [2] Codex   esc cancel", m.pending.Repo, m.pending.Issue)
	case TypingContext:
		return "Context file (enter to skip): " + m.Input + "█"
	case ConfirmingCancel:
		if m.cancelTarget == (task.Key{}) {
			return "Nothing to cancel. [y/n]"
		}
		return fmt.Sprintf("Cancel %s#%d and stop its agent? [y/n]", m.cancelTarget.Repo, m.cancelTarget.Number)
	}
	if m.LogOpen && m.Focus == TaskPane {
		if textWidth(hintsLogLong) > m.Width {
			return hintsLogShort
		}
		return hintsLogLong
	}
	if textWidth(hintsLong) > m.Width {
		return hintsShort
	}
	return hintsLong
}

const (
	hintsLong     = "j/k move · h/l pane · enter select · c cancel · r refresh · q quit"
	hintsShort    = "j/k move · h/l pane · enter · c cancel · q quit"
	hintsLogLong  = "j/k task · u/d log page · ctrl-u/d half page · g/G ends · esc close · h/l pane"
	hintsLogShort = "j/k task · u/d log · g/G ends · esc close · h/l pane"
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

func dimmed(s string) string { return dim + s + attrsReset }

func colored(s, color string) string { return color + s + attrsReset }

func taskColor(state task.State) string {
	switch state {
	case task.NeedsDecision, task.ReadyForHuman:
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
