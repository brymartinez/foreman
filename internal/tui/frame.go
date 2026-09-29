package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/render"
	"github.com/brymartinez/foreman/internal/task"
)

// focusMark flags the pane the arrow keys move in.
const focusMark = "▸ "

const (
	reverseOn  = "\x1b[7m"
	dim        = "\x1b[2m"
	attrsReset = "\x1b[0m"
)

// Frame draws the whole screen: exactly m.Height lines, each at most m.Width visible
// columns. It is pure, so the layout is tested without a terminal. Text is truncated
// before any escape sequence is added, so the width arithmetic never counts escapes.
func Frame(m Model) []string {
	lay, ok := layoutFor(m.Width, m.Height)
	if !ok {
		return tooSmall(m.Width, m.Height)
	}
	lines := make([]string, 0, m.Height)
	lines = append(lines, dimmed(pad(header(m), m.Width)))

	right := m.Width - lay.leftWidth - 1
	left := paneLines(inboxPane(m), lay.leftWidth, lay.topHeight, m.Focus == InboxPane, m.InboxCursor)
	rightLines := paneLines(taskPane(m, right), right, lay.topHeight, m.Focus == TaskPane, m.TaskCursor)
	for i := range lay.topHeight {
		lines = append(lines, left[i]+" "+rightLines[i])
	}

	lines = append(lines, dimmed(strings.Repeat("─", m.Width)))
	lines = append(lines, logPane(m, lay.logHeight)...)
	lines = append(lines, pad(truncate(m.Message, m.Width), m.Width))
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
type row struct{ lines []string }

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

func taskPane(m Model, width int) pane {
	p := pane{title: "DELEGATED WORK", empty: "No delegated work yet."}
	for _, rec := range m.Tasks {
		rows := render.TaskRows(rec)
		lines := []string{rows[0]}
		// status pads the identity to a fixed column so its rows line up. That padding
		// does not fit a narrow pane, and truncating it would drop the state, so wrap
		// the agent and the state onto their own line instead.
		if len([]rune(rows[0]))+markerWidth > width {
			identity, agent, state := render.TaskHead(rec)
			lines = []string{identity, "  " + agent + " · " + state}
		}
		for _, r := range rows[1:] {
			lines = append(lines, "  "+r)
		}
		if q := pendingQuestion(rec); q != "" {
			lines = append(lines, "  "+q)
		}
		p.rows = append(p.rows, row{lines: lines})
	}
	return p
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
	out := []string{dimmed(pad(truncate(title, width), width))}
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
			lines = append(lines, pad(truncate(marker+l, width), width))
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
		title += fmt.Sprintf(" · scrolled back %d (End to follow)", m.LogOffset)
	}
	out := []string{dimmed(pad(truncate(title, m.Width), m.Width))}
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
		rec := m.Selected()
		if rec == nil {
			return "Nothing to cancel. [y/n]"
		}
		return fmt.Sprintf("Cancel %s#%d and stop its agent? [y/n]", rec.Repo, rec.Number)
	}
	if len([]rune(hintsLong)) > m.Width {
		return hintsShort
	}
	return hintsLong
}

const (
	hintsLong  = "↑↓ move · tab pane · enter delegate · c cancel · r refresh · pgup/pgdn log · q quit"
	hintsShort = "↑↓ tab enter · c cancel · r refresh · q quit"
)

func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(strings.ReplaceAll(s, "\t", "    "))
	if len(r) <= width {
		return string(r)
	}
	if width == 1 {
		return "…"
	}
	return string(r[:width-1]) + "…"
}

func pad(s string, width int) string {
	if n := width - len([]rune(s)); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func dimmed(s string) string { return dim + s + attrsReset }

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
