package tui

import (
	"strings"
	"testing"
)

func TestQuestionMarkOpensHelpAndAnyKeyClosesItWithoutActing(t *testing.T) {
	for name, k := range map[string]Key{
		"q":   rune_('q'),
		"esc": {Kind: KeyEscape},
		"?":   rune_('?'),
		"j":   rune_('j'),
	} {
		t.Run(name, func(t *testing.T) {
			m := model()
			press(&m, rune_('?'))
			if !m.HelpOpen {
				t.Fatal("? did not open help")
			}
			cursor := m.InboxCursor
			if got := press(&m, k); got.Kind != ActionNone {
				t.Fatalf("%s while help was open returned %+v; want nothing", name, got)
			}
			if m.HelpOpen || m.InboxCursor != cursor {
				t.Fatalf("%s: help open %t, cursor %d; want help closed and the cursor kept", name, m.HelpOpen, m.InboxCursor)
			}
		})
	}
}

func TestCtrlCQuitsWhileHelpIsOpen(t *testing.T) {
	m := model()
	press(&m, rune_('?'))
	if got := press(&m, Key{Kind: KeyCtrlC}); got.Kind != ActionQuit {
		t.Fatalf("ctrl-c with help open returned %+v; want quit", got)
	}
}

func TestHelpShowsTheKeysAndVersionWithinTheTerminal(t *testing.T) {
	for _, size := range [][2]int{{100, 30}, {60, 12}} {
		m := model()
		m.Width, m.Height, m.Version = size[0], size[1], "v1.2.3"
		press(&m, rune_('?'))
		got := Frame(m)
		if len(got) != m.Height {
			t.Fatalf("%dx%d: %d lines; want %d", size[0], size[1], len(got), m.Height)
		}
		for i, l := range got {
			if w := textWidth(stripEscapes(l)); w > m.Width {
				t.Errorf("%dx%d: line %d is %d columns: %q", size[0], size[1], i, w, l)
			}
		}
		text := stripEscapes(strings.Join(got, "\n"))
		for _, want := range []string{"foreman v1.2.3", "R ", "raw", "? "} {
			if !strings.Contains(text, want) {
				t.Errorf("%dx%d: help lacks %q:\n%s", size[0], size[1], want, text)
			}
		}
	}
}

func TestHelpListsEveryKeyOnATallTerminal(t *testing.T) {
	m := model()
	m.Width, m.Height = 100, 40
	press(&m, rune_('?'))
	text := stripEscapes(strings.Join(Frame(m), "\n"))
	for _, k := range helpKeys {
		if !strings.Contains(text, k.keys) {
			t.Errorf("help lacks %q", k.keys)
		}
	}
}

func TestFooterHintsNameTheHelpKey(t *testing.T) {
	for _, width := range []int{60, 100, 160} {
		for _, m := range []Model{
			{Width: width, Height: 30},
			{Width: width, Height: 30, LogOpen: true},
			{Width: width, Height: 30, LogOpen: true, Focus: TaskPane},
		} {
			if got := footer(m); !strings.Contains(got, "? help") || textWidth(got) > width {
				t.Errorf("width %d open %t focus %v: hints %q; want ? help within the width", width, m.LogOpen, m.Focus, got)
			}
		}
	}
}

// stripEscapes drops the frame's color and attribute escapes so the text can be measured.
func stripEscapes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
