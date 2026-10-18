package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

// openLog writes log to a file, selects a task that runs on it, and opens its pane
// the way Run does.
func openLog(t *testing.T, log string) (Model, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runner.log")
	if err := os.WriteFile(path, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := record(10, task.Implementing)
	rec.Delegation.RunnerLog = path
	m := Model{Width: 100, Height: 30, Focus: TaskPane}
	readLocal(context.Background(), stubSource{recs: []*task.Record{rec}}, &m)
	m.Key(Key{Kind: KeyEnter})
	readLog(&m)
	return m, path
}

func smallChunks(t *testing.T, n int64) {
	before := logChunk
	logChunk = n
	t.Cleanup(func() { logChunk = before })
}

func lines(from, to int) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func TestRSwitchesTheOpenLogBetweenCleanedAndRawLines(t *testing.T) {
	log := `T1 {"type":"user"}` + "\n" + "T2 foreman: runner started\n"
	m, _ := openLog(t, log)
	if !slices.Equal(m.Log, []string{"T2 foreman: runner started"}) {
		t.Fatalf("cleaned log = %q", m.Log)
	}
	m.LogOffset = 1
	if got := press(&m, rune_('R')); got.Kind != ActionReadLog || !m.LogRaw || m.LogOffset != 0 {
		t.Fatalf("R returned %+v, raw %t, offset %d; want a reread at the tail in raw", got, m.LogRaw, m.LogOffset)
	}
	readLog(&m)
	if !slices.Equal(m.Log, []string{`T1 {"type":"user"}`, "T2 foreman: runner started"}) {
		t.Fatalf("raw log = %q", m.Log)
	}
	lay, _ := layoutFor(m.Width, m.Height, true, false)
	if title := logPane(m, lay.logHeight)[0]; !strings.Contains(title, "raw") {
		t.Errorf("raw log heading = %q; want it marked raw", title)
	}
	press(&m, rune_('R'))
	readLog(&m)
	if m.LogRaw || len(m.Log) != 1 {
		t.Errorf("second R: raw %t, log %q; want the cleaned lines back", m.LogRaw, m.Log)
	}
	if title := logPane(m, lay.logHeight)[0]; strings.Contains(title, "raw") {
		t.Errorf("cleaned log heading = %q", title)
	}
}

func TestRDoesNothingWithoutAnOpenLog(t *testing.T) {
	m := model()
	if got := press(&m, rune_('R')); got.Kind != ActionNone || m.LogRaw {
		t.Fatalf("R with no log open returned %+v, raw %t", got, m.LogRaw)
	}
}

func TestScrollingPastTheLoadedLinesLoadsOlderOnesAndHoldsTheView(t *testing.T) {
	smallChunks(t, 200)
	m, _ := openLog(t, lines(1, 100))
	if m.LogAtStart || len(m.Log) >= 100 {
		t.Fatalf("the first read loaded %d lines, at start %t; want only the tail chunk", len(m.Log), m.LogAtStart)
	}
	loaded := len(m.Log)
	var a Action
	for range 20 {
		if a = press(&m, Key{Kind: KeyPageUp}); a.Kind == ActionLoadOlder {
			break
		}
	}
	if a.Kind != ActionLoadOlder {
		t.Fatalf("page up at the oldest loaded line returned %+v; want a load", a)
	}
	loadOlder(&m)
	if len(m.Log) <= loaded {
		t.Fatalf("loading older lines kept %d lines", len(m.Log))
	}
	top := visibleLog(&m)
	// The page up that asked for the load moved the view onto the older lines.
	if slices.Contains(top, "line 100") {
		t.Fatalf("view after the load = %q; want it scrolled back", top)
	}
	if !slices.Equal(m.Log[len(m.Log)-loaded:], linesSlice(101-loaded, 100)) {
		t.Fatalf("older lines were not put before the loaded ones: %q", m.Log)
	}
}

func TestGLoadsBackToTheFirstLineOfTheFile(t *testing.T) {
	smallChunks(t, 128)
	m, _ := openLog(t, lines(1, 300))
	if got := press(&m, rune_('g')); got.Kind != ActionLoadOlder {
		t.Fatalf("g returned %+v; want a load", got)
	}
	loadOlder(&m)
	if !m.LogAtStart || !slices.Equal(m.Log, linesSlice(1, 300)) {
		t.Fatalf("after g: at start %t, %d lines; want every line of the file", m.LogAtStart, len(m.Log))
	}
	if got := visibleLog(&m); got[0] != "line 1" {
		t.Fatalf("view after g starts at %q; want line 1", got[0])
	}
	if got := press(&m, rune_('g')); got.Kind != ActionNone {
		t.Errorf("g at the file start returned %+v; want nothing to load", got)
	}
}

func TestNewLinesKeepLoadedOlderLinesAndTheView(t *testing.T) {
	smallChunks(t, 128)
	m, path := openLog(t, lines(1, 300))
	press(&m, rune_('g'))
	loadOlder(&m)
	top := visibleLog(&m)
	appendTo(t, path, lines(301, 320))
	readLog(&m)
	if !slices.Equal(m.Log, linesSlice(1, 320)) {
		t.Fatalf("after new lines the log holds %d lines from %q; want 1 to 320", len(m.Log), m.Log[0])
	}
	if got := visibleLog(&m); !slices.Equal(got, top) {
		t.Errorf("the view moved:\ngot  %q\nwant %q", got, top)
	}
}

func TestAnUnfinishedLastLineIsReadAgainOnceComplete(t *testing.T) {
	m, path := openLog(t, "line 1\nline 2 sta")
	if !slices.Equal(m.Log, []string{"line 1", "line 2 sta"}) {
		t.Fatalf("log = %q", m.Log)
	}
	appendTo(t, path, "rted\nline 3\n")
	readLog(&m)
	if !slices.Equal(m.Log, []string{"line 1", "line 2 started", "line 3"}) {
		t.Fatalf("log = %q; want the finished line once", m.Log)
	}
}

func TestALogRewrittenFromTheStartIsReadAgain(t *testing.T) {
	m, path := openLog(t, lines(1, 50))
	if err := os.WriteFile(path, []byte("fresh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	readLog(&m)
	if !slices.Equal(m.Log, []string{"fresh"}) {
		t.Fatalf("log = %q; want the rewritten file", m.Log)
	}
}

func TestALogShorterThanAPageAsksForNoLoad(t *testing.T) {
	m, _ := openLog(t, lines(1, 3))
	if got := press(&m, Key{Kind: KeyPageUp}); got.Kind != ActionNone || m.LogOffset != 0 {
		t.Fatalf("page up on a short log returned %+v, offset %d", got, m.LogOffset)
	}
	if got := press(&m, rune_('g')); got.Kind != ActionNone {
		t.Fatalf("g on a short log returned %+v", got)
	}
}

func TestAFailedOlderReadShowsTheErrorAndStops(t *testing.T) {
	smallChunks(t, 128)
	m, path := openLog(t, lines(1, 300))
	loaded := slices.Clone(m.Log)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	press(&m, rune_('g'))
	loadOlder(&m)
	if !strings.HasPrefix(m.Message, "! ") || !slices.Equal(m.Log, loaded) {
		t.Fatalf("message %q, log kept %t; want the error and the loaded lines", m.Message, slices.Equal(m.Log, loaded))
	}
}

func linesSlice(from, to int) []string {
	return strings.Split(strings.TrimSuffix(lines(from, to), "\n"), "\n")
}
