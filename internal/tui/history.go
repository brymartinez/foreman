package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

// logSource is the log the lower pane shows: a file, and the bytes of it the pane may
// show. ceil 0 reads to the end of the file.
type logSource struct {
	path        string
	floor, ceil int64
}

// logSource is the selected history row's part of its task log while the history is
// open, and otherwise the selected task's whole runner log.
func (m *Model) logSource() (logSource, bool) {
	if m.HistoryOpen {
		r := m.selectedHistory()
		if r == nil || r.Log.Path == "" {
			return logSource{}, false
		}
		return logSource{path: r.Log.Path, floor: r.Log.Start, ceil: r.Log.End}, true
	}
	rec := m.Selected()
	if !m.LogOpen || rec == nil || rec.Delegation == nil || rec.Delegation.RunnerLog == "" {
		return logSource{}, false
	}
	return logSource{path: rec.Delegation.RunnerLog}, true
}

func (m *Model) selectedHistory() *app.HistoryRow {
	if m.HistoryCursor < 0 || m.HistoryCursor >= len(m.History) {
		return nil
	}
	return &m.History[m.HistoryCursor]
}

// openHistory replaces the dashboard with the PR history. The dashboard's lower pane
// closes, so the log pane belongs to the history until Esc.
func (m *Model) openHistory() Action {
	m.HistoryOpen, m.HistoryLogFocus = true, false
	m.LogOpen, m.ShowOpen = false, false
	m.HistoryLoading, m.Message = true, ""
	m.historySeq++
	return Action{Kind: ActionLoadHistory}
}

// SetHistory shows a finished history load and keeps the cursor on the same PR. A
// failed load keeps the rows already shown.
func (m *Model) SetHistory(rows []app.HistoryRow, warnings []error, err error) {
	m.HistoryLoading = false
	if err != nil {
		m.HistoryErr = err.Error()
		m.Message = "! PR history: " + err.Error()
		return
	}
	cursor := m.HistoryCursor
	if cur := m.selectedHistory(); cur != nil {
		for i, r := range rows {
			if r.Key.Repo == cur.Key.Repo && r.PR == cur.PR {
				cursor = i
			}
		}
	}
	m.History, m.HistoryWarnings, m.HistoryErr = rows, warnings, ""
	m.HistoryCursor = clamp(cursor, len(rows))
	m.HistoryTop = historyTop(m.HistoryTop, m.HistoryCursor, len(rows), m.historyPage())
}

func (m *Model) historyKey(k Key) Action {
	switch {
	case k.Kind == KeyRune && k.Rune == 'q':
		return Action{Kind: ActionQuit}
	case k.Kind == KeyRune && k.Rune == '?':
		m.HelpOpen = true
	case k.Kind == KeyEscape && m.HistoryLogFocus:
		m.HistoryLogFocus = false
	case k.Kind == KeyEscape:
		m.HistoryOpen = false
	case k.Kind == KeyRune && k.Rune == 'r':
		m.HistoryLoading = true
		m.historySeq++
		return Action{Kind: ActionLoadHistory}
	case k.Kind == KeyEnter && m.selectedHistory() != nil:
		m.HistoryLogFocus = true
	case k.Kind == KeyTab:
		m.HistoryLogFocus = !m.HistoryLogFocus
	case k.Kind == KeyRune && k.Rune == 'R':
		m.LogRaw = !m.LogRaw
		m.LogPath, m.LogOffset = "", 0
		return Action{Kind: ActionReadLog}
	case m.HistoryLogFocus:
		return m.historyLogKey(k)
	default:
		page := m.historyPage()
		switch {
		case k.Kind == KeyDown || (k.Kind == KeyRune && k.Rune == 'j'):
			m.moveHistory(1)
		case k.Kind == KeyUp || (k.Kind == KeyRune && k.Rune == 'k'):
			m.moveHistory(-1)
		case k.Kind == KeyCtrlF || k.Kind == KeyPageDown:
			m.moveHistory(page)
		case k.Kind == KeyCtrlB || k.Kind == KeyPageUp:
			m.moveHistory(-page)
		case k.Kind == KeyCtrlD:
			m.moveHistory(max(page/2, 1))
		case k.Kind == KeyCtrlU:
			m.moveHistory(-max(page/2, 1))
		}
	}
	return Action{}
}

// historyLogKey scrolls the detail's log with the dashboard log's keys, plus j/k for
// one line.
func (m *Model) historyLogKey(k Key) Action {
	page := m.logPage()
	switch {
	case k.Kind == KeyUp || (k.Kind == KeyRune && k.Rune == 'k'):
		return m.scrollLog(1)
	case k.Kind == KeyDown || (k.Kind == KeyRune && k.Rune == 'j'):
		return m.scrollLog(-1)
	case k.Kind == KeyPageUp || (k.Kind == KeyRune && k.Rune == 'u'):
		return m.scrollLog(page)
	case k.Kind == KeyPageDown || (k.Kind == KeyRune && k.Rune == 'd'):
		return m.scrollLog(-page)
	case k.Kind == KeyCtrlU:
		return m.scrollLog(max(page/2, 1))
	case k.Kind == KeyCtrlD:
		return m.scrollLog(-max(page/2, 1))
	case k.Kind == KeyRune && k.Rune == 'g':
		return m.scrollTop()
	case k.Kind == KeyEnd || (k.Kind == KeyRune && k.Rune == 'G'):
		m.scrollEnd()
	}
	return Action{}
}

// moveHistory moves the cursor by rows. A page step moves the view with it, so the
// rows under the cursor change the way they do in a pager.
func (m *Model) moveHistory(by int) {
	n := len(m.History)
	if n == 0 {
		return
	}
	page := m.historyPage()
	if by > 1 || by < -1 {
		m.HistoryTop += by
	}
	m.HistoryCursor = clamp(m.HistoryCursor+by, n)
	m.HistoryTop = historyTop(m.HistoryTop, m.HistoryCursor, n, page)
}

// historyTop is the first row in view: top, moved as little as keeps the cursor in a
// view of page rows, and never past the last full page.
func historyTop(top, cursor, n, page int) int {
	top = min(top, cursor)
	top = max(top, cursor-page+1)
	return max(min(top, n-page), 0)
}

// historyPage is how many rows the list shows.
func (m *Model) historyPage() int {
	lay, ok := layoutFor(m.Width, m.Height, true, false)
	if !ok {
		return 1
	}
	return max(lay.topHeight-1, 1)
}

// historyLogPage matches the dashboard's open log height.
func (m *Model) historyLogPage() int {
	lay, ok := layoutFor(m.Width, m.Height, true, false)
	if !ok {
		return 1
	}
	return max(lay.logHeight-1, 1)
}

// historyFrame draws the PR history in place of the dashboard.
func historyFrame(m Model) []string {
	lay, _ := layoutFor(m.Width, m.Height, true, false)
	leftWidth := m.Width * 3 / 5
	rightWidth := m.Width - leftWidth - 1
	lines := []string{colored(pad(truncate(historyHeader(m), m.Width), m.Width), cyan)}
	left := historyList(m, leftWidth, lay.topHeight)
	right := historyTiming(m, rightWidth, lay.topHeight)
	for i := range lay.topHeight {
		lines = append(lines, left[i]+colored("│", cyan)+right[i])
	}
	lines = append(lines, colored(strings.Repeat("─", m.Width), cyan))
	lines = append(lines, historyLogPane(m, lay.logHeight)...)
	return append(lines, messageLine(m), dimmed(pad(truncate(footer(m), m.Width), m.Width)))
}

func historyHeader(m Model) string {
	view := "configured repos"
	if m.AllRepos {
		view = "all repos"
	}
	parts := []string{fmt.Sprintf("foreman · PR history · %d %s · %s", len(m.History), plural(len(m.History), "PR", "PRs"), view)}
	if m.HistoryLoading {
		parts = append(parts, "loading…")
	}
	if n := len(m.HistoryWarnings); n > 0 {
		parts = append(parts, fmt.Sprintf("! %d %s", n, plural(n, "PR status unread", "PR statuses unread")))
	}
	return strings.Join(parts, " · ")
}

const (
	statusWidth = len(app.StatusUnknown)
	totalWidth  = len("unavailable")
)

// historyList renders the title and one line per PR: repository, number, title,
// status, and total, with the title taking the width the others leave.
func historyList(m Model, width, height int) []string {
	title := "PR HISTORY · merged and closed, newest first"
	focused := !m.HistoryLogFocus
	if focused {
		title = focusMark + title
	}
	out := []string{colored(pad(truncate(title, width), width), cyan)}
	rows := height - 1
	if len(m.History) == 0 {
		empty := "No merged or closed Foreman PRs."
		switch {
		case m.HistoryLoading:
			empty = "Loading PR history…"
		case m.HistoryErr != "":
			empty = "! " + m.HistoryErr
		}
		out = append(out, pad(truncate("  "+empty, width), width))
	}
	repoWidth, prWidth := 0, 0
	for _, r := range m.History {
		repoWidth = max(repoWidth, min(textWidth(string(r.Key.Repo)), 30))
		prWidth = max(prWidth, len(fmt.Sprintf("#%d", r.PR)))
	}
	// The repository gives up columns before the title drops below 8, so the status
	// always fits. A list too narrow for the total leaves it to the timing pane.
	const minTitle = 8
	fixed := 2 + 2 + prWidth + 2 + 2 + statusWidth
	showTotal := width-fixed-2-totalWidth >= minTitle+1
	if showTotal {
		fixed += 2 + totalWidth
	}
	repoWidth = max(min(repoWidth, width-fixed-minTitle), 1)
	titleWidth := max(width-fixed-repoWidth, minTitle)
	top := historyTop(m.HistoryTop, m.HistoryCursor, len(m.History), rows)
	for i := top; i < len(m.History) && len(out) < height; i++ {
		r := m.History[i]
		marker := "  "
		if i == m.HistoryCursor {
			marker = "> "
		}
		line := marker + pad(truncate(string(r.Key.Repo), repoWidth), repoWidth) + "  " + pad(fmt.Sprintf("#%d", r.PR), prWidth) + "  " +
			pad(truncate(r.Title, titleWidth), titleWidth) + "  " + pad(r.Status, statusWidth)
		if showTotal {
			line += "  " + totalText(r)
		}
		line = pad(truncate(line, width), width)
		if i == m.HistoryCursor && focused {
			out = append(out, reverseOn+line+attrsReset)
			continue
		}
		out = append(out, highlight(line, r.Status, statusColor(r.Status)))
	}
	for len(out) < height {
		out = append(out, strings.Repeat(" ", width))
	}
	return out
}

func statusColor(status string) string {
	switch status {
	case app.StatusMerged:
		return green
	case app.StatusClosed:
		return yellow
	}
	return red
}

// totalText is the row's total column: the measured total, or why there is none.
func totalText(r app.HistoryRow) string {
	switch r.Timing {
	case app.TimingMeasured:
		return duration(r.Total)
	case app.TimingNoHandoff:
		return "no handoff"
	}
	return "unavailable"
}

// duration is 1h02m, 4m10s, or 45s.
func duration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

var phaseNames = map[task.Phase]string{
	task.PhasePlanning:          "planning",
	task.PhaseImplementation:    "implementing",
	task.PhaseSelfReview:        "self-review",
	task.PhaseAdversarialReview: "adversarial review",
	task.PhaseOpeningPR:         "opening PR",
	task.PhaseCI:                "CI wait",
	task.PhasePaused:            "paused",
}

// phaseParts are the detail's phase values: each measured phase, then the total. An
// attempt from before the stages shows its combined review time in place of the
// review stages and opening PR, which it has no separate time for.
func phaseParts(r app.HistoryRow) []string {
	return timingParts(r, false)
}

// timingParts is phaseParts, without the stages that took no time when compact.
func timingParts(r app.HistoryRow, compact bool) []string {
	if r.Timing == app.TimingUnavailable || r.Phases == nil {
		return []string{"phase times unavailable"}
	}
	var parts []string
	for _, p := range task.MeasuredPhases {
		if r.LegacyReview && (p == task.PhaseSelfReview || p == task.PhaseAdversarialReview || p == task.PhaseOpeningPR) {
			if p == task.PhaseSelfReview {
				parts = append(parts, "legacy review and fixes "+duration(r.Phases[task.PhaseReview]), "separate reviews unavailable")
			}
			continue
		}
		if compact && r.Phases[p] == 0 {
			continue
		}
		parts = append(parts, phaseNames[p]+" "+duration(r.Phases[p]))
	}
	if r.Timing == app.TimingNoHandoff {
		return append(parts, fmt.Sprintf("no handoff, %s to the close", duration(r.Total)))
	}
	return append(parts, "total "+duration(r.Total))
}

// phaseLines are the selected row's phase values, one per timing pane line.
func (m *Model) phaseLines() []string {
	r := m.selectedHistory()
	if r == nil {
		return nil
	}
	return phaseParts(*r)
}

// historyTiming renders the selected row's phase lines beside the list. When the pane
// is too short for every line, the heading goes first, then the stages that took no
// time, and then the last stages before the total, which always stays.
func historyTiming(m Model, width, height int) []string {
	var out []string
	r := m.selectedHistory()
	if r != nil {
		parts := m.phaseLines()
		if len(parts) > height {
			parts = timingParts(*r, true)
		}
		if len(parts) > height {
			parts = append(parts[:height-1], parts[len(parts)-1])
		}
		if height > len(parts) {
			out = append(out, colored(pad("TIMING", width), cyan))
		}
		for _, line := range parts {
			out = append(out, pad(truncate(line, width), width))
		}
	} else {
		out = append(out, colored(pad("TIMING", width), cyan))
	}
	for len(out) < height {
		out = append(out, strings.Repeat(" ", width))
	}
	return out[:height]
}

// historyLogPane renders the selected attempt's bounded log across the lower pane.
func historyLogPane(m Model, height int) []string {
	r := m.selectedHistory()
	if r == nil {
		out := []string{colored(pad("LOG · no PR selected", m.Width), cyan)}
		for len(out) < height {
			out = append(out, strings.Repeat(" ", m.Width))
		}
		return out
	}
	logTitle := fmt.Sprintf("LOG · PR #%d · %s#%d", r.PR, r.Key.Repo, r.Key.Number)
	if m.LogRaw {
		logTitle += " · raw"
	}
	if r.Log.Whole {
		logTitle += " · whole issue log, attempt boundaries unknown"
	}
	if m.LogOffset > 0 {
		logTitle += fmt.Sprintf(" · scrolled back %d (G to follow)", m.LogOffset)
	}
	if r.Log.Path == "" {
		logTitle += " · none recorded"
	} else {
		logTitle += " · " + r.Log.Path
	}
	if m.HistoryLogFocus {
		logTitle = focusMark + logTitle
	}
	out := []string{colored(pad(truncate(logTitle, m.Width), m.Width), cyan)}
	var body []string
	switch {
	case m.LogErr != "":
		body = []string{"  " + m.LogErr}
	case r.Log.Path != "" && len(m.Log) == 0:
		body = []string{"  No log lines."}
	default:
		end := max(len(m.Log)-m.LogOffset, 0)
		body = m.Log[max(end-m.historyLogPage(), 0):end]
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
