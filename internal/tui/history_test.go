package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

func historyRows(n int) []app.HistoryRow {
	rows := make([]app.HistoryRow, n)
	for i := range rows {
		rows[i] = app.HistoryRow{Key: task.Key{Repo: "a/b", Number: 100 + i}, PR: 1000 - i, Title: fmt.Sprintf("Change %d", i),
			Status: app.StatusMerged, Timing: app.TimingMeasured, Total: 62 * time.Minute}
	}
	return rows
}

func openHistory(t *testing.T, rows []app.HistoryRow, height int) Model {
	t.Helper()
	m := Model{Width: 100, Height: height}
	if a := m.Key(Key{Kind: KeyRune, Rune: 'p'}); a.Kind != ActionLoadHistory || !m.HistoryOpen || !m.HistoryLoading {
		t.Fatalf("p = %+v, open %v loading %v; want the history open and loading", a, m.HistoryOpen, m.HistoryLoading)
	}
	m.SetHistory(rows, nil, nil)
	return m
}

func TestPOpensHistoryAndEscReturnsToTheDashboard(t *testing.T) {
	m := openHistory(t, historyRows(3), 30)
	if frame := strings.Join(Frame(m), "\n"); !strings.Contains(frame, "PR HISTORY") || strings.Contains(frame, "INBOX") {
		t.Fatalf("history frame shows the dashboard:\n%s", frame)
	}
	m.Key(Key{Kind: KeyEscape})
	if m.HistoryOpen {
		t.Fatal("esc left the history open")
	}
	if frame := strings.Join(Frame(m), "\n"); !strings.Contains(frame, "INBOX") {
		t.Fatalf("esc did not bring the dashboard back:\n%s", frame)
	}
}

func TestQQuitsFromHistory(t *testing.T) {
	m := openHistory(t, historyRows(3), 30)
	if a := m.Key(Key{Kind: KeyRune, Rune: 'q'}); a.Kind != ActionQuit {
		t.Fatalf("q = %+v, want quit", a)
	}
}

func TestHistoryRowsShowRepoPRTitleStatusAndTotal(t *testing.T) {
	rows := historyRows(3)
	rows[1].Status, rows[1].Timing = app.StatusClosed, app.TimingNoHandoff
	rows[2].Status, rows[2].Timing = app.StatusUnknown, app.TimingUnavailable
	m := openHistory(t, rows, 30)
	frame := Frame(m)
	if len(frame) != 30 {
		t.Fatalf("frame has %d lines, want 30", len(frame))
	}
	text := strings.Join(frame, "\n")
	for _, want := range []string{"a/b", "#1000", "Change 0", "merged", "1h02m", "closed", "no handoff", "unknown", "unavailable"} {
		if !strings.Contains(text, want) {
			t.Errorf("frame is missing %q:\n%s", want, text)
		}
	}
}

func TestHistoryPagingMovesByRowPageAndHalfPageAndStopsAtTheEnds(t *testing.T) {
	m := openHistory(t, historyRows(100), 20)
	page := m.historyPage()
	if page < 5 {
		t.Fatalf("page = %d rows; the list should fill the terminal", page)
	}
	steps := []struct {
		key  Key
		want int
	}{
		{Key{Kind: KeyRune, Rune: 'k'}, 0},
		{Key{Kind: KeyRune, Rune: 'j'}, 1},
		{Key{Kind: KeyCtrlF}, 1 + page},
		{Key{Kind: KeyCtrlD}, 1 + page + page/2},
		{Key{Kind: KeyCtrlU}, 1 + page},
		{Key{Kind: KeyCtrlB}, 1},
		{Key{Kind: KeyCtrlB}, 0},
	}
	for _, s := range steps {
		m.Key(s.key)
		if m.HistoryCursor != s.want {
			t.Fatalf("after %+v cursor = %d, want %d", s.key, m.HistoryCursor, s.want)
		}
	}
	for range 50 {
		m.Key(Key{Kind: KeyCtrlF})
	}
	if m.HistoryCursor != 99 {
		t.Fatalf("cursor = %d, want it stopped on the last row", m.HistoryCursor)
	}
	if !strings.Contains(strings.Join(Frame(m), "\n"), "#901") {
		t.Fatal("the last row is not in view")
	}
}

func TestHistorySelectionStaysVisibleAfterAResize(t *testing.T) {
	m := openHistory(t, historyRows(100), 60)
	for range 40 {
		m.Key(Key{Kind: KeyRune, Rune: 'j'})
	}
	m.Height = 14
	if !strings.Contains(strings.Join(Frame(m), "\n"), "Change 40") {
		t.Fatal("the selected row left the view after the terminal shrank")
	}
}

func TestSetHistoryKeepsTheSelectedPR(t *testing.T) {
	rows := historyRows(5)
	m := openHistory(t, rows, 30)
	m.Key(Key{Kind: KeyRune, Rune: 'j'})
	m.Key(Key{Kind: KeyRune, Rune: 'j'})
	m.SetHistory(append([]app.HistoryRow{{Key: task.Key{Repo: "c/d", Number: 1}, PR: 5}}, rows...), nil, nil)
	if m.HistoryCursor != 3 {
		t.Fatalf("cursor = %d, want it on the same PR after a reload", m.HistoryCursor)
	}
}

func TestHistoryShowsWhyItHasNoRows(t *testing.T) {
	m := openHistory(t, nil, 30)
	if !strings.Contains(strings.Join(Frame(m), "\n"), "No merged or closed Foreman PRs") {
		t.Fatal("an empty history says nothing")
	}
	m.SetHistory(nil, nil, errors.New("read tasks: boom"))
	if !strings.Contains(strings.Join(Frame(m), "\n"), "read tasks: boom") {
		t.Fatal("a failed load shows no error")
	}
}

func TestVInHistorySwitchesTheRepositoryViewAndReloads(t *testing.T) {
	m := openHistory(t, historyRows(3), 30)
	a := m.Key(Key{Kind: KeyRune, Rune: 'v'})
	if a.Kind != ActionToggleView || !m.AllRepos || len(m.History) != 0 || !m.HistoryLoading {
		t.Fatalf("v = %+v all %v rows %d loading %v", a, m.AllRepos, len(m.History), m.HistoryLoading)
	}
	apply(&m, result{history: historyRows(2), historyLoad: true, historySeq: m.historySeq - 1})
	if len(m.History) != 0 {
		t.Fatal("a load for the view just left filled the list")
	}
	apply(&m, result{history: historyRows(2), historyLoad: true, historySeq: m.historySeq, allRepos: true})
	if len(m.History) != 2 || m.HistoryLoading {
		t.Fatalf("rows %d loading %v after the current view's load", len(m.History), m.HistoryLoading)
	}
}

func TestRReloadsHistory(t *testing.T) {
	m := openHistory(t, historyRows(3), 30)
	if a := m.Key(Key{Kind: KeyRune, Rune: 'r'}); a.Kind != ActionLoadHistory {
		t.Fatalf("r = %+v", a)
	}
}

// historyLog writes lines to a task log and returns a row for the attempt that owns
// lines from..to, 1-based and inclusive.
func historyLog(t *testing.T, total, from, to int) (app.HistoryRow, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "a-b-1.log")
	text := lines(1, total)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	offset := func(n int) int64 { return int64(len(lines(1, n-1))) }
	r := app.HistoryRow{Key: task.Key{Repo: "a/b", Number: 1}, PR: 7, Status: app.StatusMerged, Timing: app.TimingMeasured, Total: time.Hour,
		Phases: map[task.Phase]time.Duration{task.PhasePlanning: 20 * time.Minute, task.PhaseCI: 40 * time.Minute},
		Log:    app.LogRange{Path: path, Start: offset(from)}}
	if to < total {
		r.Log.End = offset(to + 1)
	}
	return r, path
}

func TestEnterShowsTheAttemptsPhasesAndOnlyItsLogLines(t *testing.T) {
	row, _ := historyLog(t, 30, 11, 20)
	m := openHistory(t, []app.HistoryRow{row}, 40)
	m.Key(Key{Kind: KeyEnter})
	readLog(&m)
	if !m.HistoryDetail || len(m.Log) != 10 || m.Log[0] != "line 11" || m.Log[9] != "line 20" || !m.LogAtStart {
		t.Fatalf("detail %v log %q at start %v, want lines 11 to 20 only", m.HistoryDetail, m.Log, m.LogAtStart)
	}
	text := strings.Join(Frame(m), "\n")
	for _, want := range []string{"planning 20m00s", "CI wait 40m00s", "total 1h00m", "line 11", "line 20"} {
		if !strings.Contains(text, want) {
			t.Errorf("detail is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "line 10\n") || strings.Contains(text, "line 21") {
		t.Errorf("detail shows another attempt's lines:\n%s", text)
	}
}

func TestHistoryLogScrollsBackInChunksAndStopsAtTheAttemptStart(t *testing.T) {
	smallChunks(t, 40)
	row, _ := historyLog(t, 200, 51, 150)
	m := openHistory(t, []app.HistoryRow{row}, 20)
	m.Key(Key{Kind: KeyEnter})
	readLog(&m)
	if m.LogAtStart || m.Log[len(m.Log)-1] != "line 150" {
		t.Fatalf("first read = %q at start %v, want a tail chunk ending at line 150", m.Log, m.LogAtStart)
	}
	m.Key(Key{Kind: KeyTab})
	if a := m.Key(Key{Kind: KeyRune, Rune: 'g'}); a.Kind == ActionLoadOlder {
		loadOlder(&m)
	}
	if !m.LogAtStart || m.Log[0] != "line 51" || len(m.Log) != 100 {
		t.Fatalf("after g: first %q, %d lines, at start %v; want lines 51 to 150", m.Log[0], len(m.Log), m.LogAtStart)
	}
	m.Key(Key{Kind: KeyRune, Rune: 'G'})
	m.Key(Key{Kind: KeyRune, Rune: 'k'})
	if m.LogOffset != 1 {
		t.Fatalf("k in the log moved it %d lines, want 1", m.LogOffset)
	}
	m.Key(Key{Kind: KeyCtrlU})
	if m.LogOffset != 1+max(m.logPage()/2, 1) {
		t.Fatalf("ctrl-u in the log moved it to %d", m.LogOffset)
	}
}

func TestHistoryLabelsAWholeIssueLog(t *testing.T) {
	row, _ := historyLog(t, 5, 1, 5)
	row.Log.Whole, row.Timing, row.Phases = true, app.TimingUnavailable, nil
	m := openHistory(t, []app.HistoryRow{row}, 30)
	m.Key(Key{Kind: KeyEnter})
	readLog(&m)
	text := strings.Join(Frame(m), "\n")
	for _, want := range []string{"whole issue log, attempt boundaries unknown", "phase times unavailable"} {
		if !strings.Contains(text, want) {
			t.Errorf("detail is missing %q:\n%s", want, text)
		}
	}
}

func TestEscClosesTheDetailBeforeTheHistory(t *testing.T) {
	row, _ := historyLog(t, 5, 1, 5)
	m := openHistory(t, []app.HistoryRow{row}, 30)
	m.Key(Key{Kind: KeyEnter})
	m.Key(Key{Kind: KeyEscape})
	if m.HistoryDetail || !m.HistoryOpen {
		t.Fatalf("detail %v open %v, want the detail closed and the history still open", m.HistoryDetail, m.HistoryOpen)
	}
	m.Key(Key{Kind: KeyEscape})
	if m.HistoryOpen {
		t.Fatal("second esc left the history open")
	}
}

func TestOpeningHistoryClosesTheDashboardLogPane(t *testing.T) {
	m, _ := openLog(t, lines(1, 3))
	m.Key(Key{Kind: KeyRune, Rune: 'p'})
	readLog(&m)
	if m.LogOpen || len(m.Log) != 0 {
		t.Fatalf("log open %v with %d lines under the history", m.LogOpen, len(m.Log))
	}
}

func TestDecodeKeyReadsCtrlFAndCtrlB(t *testing.T) {
	for b, want := range map[byte]KeyKind{0x06: KeyCtrlF, 0x02: KeyCtrlB} {
		if k, n, ok := DecodeKey([]byte{b}, false); !ok || n != 1 || k.Kind != want {
			t.Errorf("0x%02x = %+v", b, k)
		}
	}
}

func TestHelpListsTheHistoryKey(t *testing.T) {
	m := Model{Width: 120, Height: 40, HelpOpen: true}
	if !slices.ContainsFunc(Frame(m), func(l string) bool { return strings.Contains(l, "PR history") }) {
		t.Fatal("help does not mention the PR history")
	}
}

func TestRunLoadsHistoryThroughTheSource(t *testing.T) {
	s := historySource{rows: historyRows(2)}
	r := loadHistory(context.Background(), s, true, 7)
	if !r.historyLoad || !r.allRepos || r.historySeq != 7 || len(r.history) != 2 {
		t.Fatalf("result = %+v", r)
	}
}

type historySource struct {
	stubSource
	rows []app.HistoryRow
}

func (s historySource) History(context.Context, bool) ([]app.HistoryRow, []error, error) {
	return s.rows, nil, nil
}

func TestAReloadThatEndsTheAttemptDropsTheNextAttemptsLines(t *testing.T) {
	row, _ := historyLog(t, 30, 11, 30)
	m := openHistory(t, []app.HistoryRow{row}, 40)
	m.Key(Key{Kind: KeyEnter})
	readLog(&m)
	if m.Log[len(m.Log)-1] != "line 30" {
		t.Fatalf("setup: log ends %q", m.Log[len(m.Log)-1])
	}
	ended, _ := historyLog(t, 30, 11, 20)
	row.Log.End = ended.Log.End
	settle(context.Background(), stubSource{}, &m, result{historyLoad: true, historySeq: m.historySeq, history: []app.HistoryRow{row}}, nil)
	if len(m.Log) != 10 || m.Log[9] != "line 20" {
		t.Fatalf("log = %q, want lines 11 to 20 once a restart ended the attempt", m.Log)
	}
}

func TestAnOlderHistoryLoadDoesNotReplaceANewerOne(t *testing.T) {
	m := openHistory(t, historyRows(3), 30)
	m.Key(Key{Kind: KeyRune, Rune: 'r'})
	older := m.historySeq
	m.Key(Key{Kind: KeyRune, Rune: 'r'})
	apply(&m, result{historyLoad: true, historySeq: m.historySeq, history: historyRows(5)})
	apply(&m, result{historyLoad: true, historySeq: older, history: historyRows(1)})
	if len(m.History) != 5 {
		t.Fatalf("rows = %d, want the newest load's 5", len(m.History))
	}
}

func TestThePhaseSummaryWrapsInAnEightyColumnTerminal(t *testing.T) {
	row, _ := historyLog(t, 30, 1, 30)
	row.Phases = map[task.Phase]time.Duration{task.PhasePlanning: 12 * time.Minute, task.PhaseImplementation: 34 * time.Minute,
		task.PhaseReview: 21 * time.Minute, task.PhaseCI: 15 * time.Minute, task.PhasePaused: 58 * time.Minute}
	row.Total = 140 * time.Minute
	m := openHistory(t, []app.HistoryRow{row}, 30)
	m.Width = 80
	m.Key(Key{Kind: KeyEnter})
	readLog(&m)
	frame := Frame(m)
	text := strings.Join(frame, "\n")
	for _, want := range []string{"paused 58m00s", "total 2h20m", "line 30"} {
		if !strings.Contains(text, want) {
			t.Errorf("detail is missing %q:\n%s", want, text)
		}
	}
	if len(frame) != 30 {
		t.Errorf("frame has %d lines, want 30", len(frame))
	}
	page := m.logPage()
	m.Key(Key{Kind: KeyTab})
	m.Key(Key{Kind: KeyRune, Rune: 'g'})
	if got := strings.Join(Frame(m), "\n"); !strings.Contains(got, fmt.Sprintf("line %d ", page)) || strings.Contains(got, fmt.Sprintf("line %d ", page+1)) {
		t.Errorf("the log shows more or fewer than its %d-line page:\n%s", page, got)
	}
}

func TestTheDetailShowsALogLineAtTheMinimumTerminalSize(t *testing.T) {
	row, _ := historyLog(t, 30, 1, 30)
	row.Phases = map[task.Phase]time.Duration{task.PhasePlanning: 12 * time.Minute, task.PhaseImplementation: 34 * time.Minute,
		task.PhaseReview: 21 * time.Minute, task.PhaseCI: 15 * time.Minute, task.PhasePaused: 58 * time.Minute}
	row.Total = 140 * time.Minute
	m := openHistory(t, []app.HistoryRow{row}, minHeight)
	m.Width = minWidth
	m.Key(Key{Kind: KeyEnter})
	readLog(&m)
	if n := len(m.phaseLines()); n != 3 {
		t.Fatalf("setup: %d phase lines, want 3", n)
	}
	frame := Frame(m)
	text := strings.Join(frame, "\n")
	for _, want := range []string{"#7", "total 2h20m", "line 30"} {
		if !strings.Contains(text, want) {
			t.Errorf("frame is missing %q:\n%s", want, text)
		}
	}
	if len(frame) != minHeight {
		t.Errorf("frame has %d lines, want %d", len(frame), minHeight)
	}
}

func TestALongRepositoryNameLeavesTheTotalInViewAtTheMinimumWidth(t *testing.T) {
	rows := historyRows(1)
	rows[0].Key.Repo, rows[0].PR = "longorganization/longrepository", 123
	m := openHistory(t, rows, minHeight)
	m.Width = minWidth
	line := Frame(m)[2]
	for _, want := range []string{"#123", "merged", "1h02m"} {
		if !strings.Contains(line, want) {
			t.Errorf("row %q is missing %q", line, want)
		}
	}
}
