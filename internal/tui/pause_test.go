package tui

import (
	"strings"
	"testing"
)

func TestSpacePausesOnlyAnOpenLogAndResumesOnSecondSpace(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T) Model
	}{
		{"task log", func(t *testing.T) Model {
			m := model()
			m.Focus = TaskPane
			press(&m, Key{Kind: KeyEnter})
			return m
		}},
		{"PR history list", func(t *testing.T) Model { return openHistory(t, historyRows(1), 30) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.setup(t)
			press(&m, rune_(' '))
			if !strings.Contains(screen(m), "PAUSED") {
				t.Fatal("Space did not pause the open log and show its indicator")
			}
			press(&m, rune_(' '))
			if strings.Contains(screen(m), "PAUSED") {
				t.Fatal("second Space did not resume the log")
			}
		})
	}
	for _, tc := range []struct {
		name  string
		setup func(*testing.T) Model
	}{
		{"dashboard", func(*testing.T) Model { return model() }},
		{"task state", func(*testing.T) Model {
			m := model()
			m.Focus = TaskPane
			press(&m, Key{Kind: KeyEnter}, rune_('s'))
			return m
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.setup(t)
			press(&m, rune_(' '))
			if strings.Contains(screen(m), "PAUSED") {
				t.Fatal("Space paused without an open log")
			}
		})
	}
}

func TestNavigationResumesThePausedLogAndAppliesItsKey(t *testing.T) {
	m := model()
	m.Focus = TaskPane
	press(&m, Key{Kind: KeyEnter}, rune_(' '))
	m.Log = make([]string, 50)
	press(&m, rune_('u'))
	if strings.Contains(screen(m), "PAUSED") || m.LogOffset == 0 {
		t.Fatalf("u left pause %t and offset %d; want resumed and scrolled", strings.Contains(screen(m), "PAUSED"), m.LogOffset)
	}
	press(&m, rune_(' '), Key{Kind: KeyEscape})
	if strings.Contains(screen(m), "PAUSED") || m.LogOpen {
		t.Fatal("Escape did not resume and close the log")
	}

	h := openHistory(t, historyRows(2), 30)
	press(&h, rune_(' '), Key{Kind: KeyTab})
	if strings.Contains(screen(h), "PAUSED") || !h.HistoryLogFocus {
		t.Fatal("Tab did not resume and focus the history log")
	}
	press(&h, rune_(' '), Key{Kind: KeyEscape})
	if strings.Contains(screen(h), "PAUSED") || h.HistoryLogFocus || !h.HistoryOpen {
		t.Fatal("Escape did not resume and return to the history list")
	}
}

func TestHelpExplainsLogPauseAndResume(t *testing.T) {
	m := model()
	m.Width, m.Height = 100, 40
	press(&m, rune_('?'))
	got := screen(m)
	for _, want := range []string{"space", "pause", "resume", "navigation"} {
		if !strings.Contains(strings.ToLower(got), want) {
			t.Errorf("help lacks %q", want)
		}
	}
}

func TestAPausedLogWritesOneFrameThenNothingUntilItResumes(t *testing.T) {
	var b strings.Builder
	p := painter{out: &b}
	m := model()
	m.Focus = TaskPane
	press(&m, Key{Kind: KeyEnter})
	press(&m, rune_(' '))

	p.paint(m)
	if !strings.Contains(b.String(), "PAUSED") {
		t.Fatalf("the first paused frame lacks the indicator: %q", b.String())
	}
	b.Reset()
	m.Log = append(m.Log, "new line")
	p.resize()
	p.paint(m)
	if b.Len() != 0 {
		t.Fatalf("a paused log wrote to the terminal: %q", b.String())
	}

	press(&m, rune_(' '))
	p.paint(m)
	if !strings.HasPrefix(b.String(), clear) || !strings.Contains(b.String(), "new line") ||
		strings.Contains(b.String(), "PAUSED") {
		t.Fatalf("resuming did not clear for the held resize and draw the latest log: %q", b.String())
	}
	b.Reset()
	p.paint(m)
	if strings.Contains(b.String(), clear) {
		t.Fatal("the held resize cleared the screen twice")
	}
}

func TestAResizeClearsTheScreenAtOnceWhenNotPaused(t *testing.T) {
	var b strings.Builder
	p := painter{out: &b}
	p.paint(model())
	b.Reset()
	p.resize()
	if b.String() != clear {
		t.Fatalf("resize wrote %q; want a clear", b.String())
	}
}

func TestATaskRefreshKeepsAPausedHistoryLogPaused(t *testing.T) {
	h := openHistory(t, historyRows(1), 30)
	press(&h, Key{Kind: KeyEnter}, rune_(' '))
	h.SetTasks(nil)
	if !h.LogPaused {
		t.Fatal("a task refresh resumed the paused PR history log")
	}
}
