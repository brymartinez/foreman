package tui

import (
	"strings"
	"testing"
)

func TestTextWidthCountsColumnsNotRunes(t *testing.T) {
	for _, tc := range []struct {
		s    string
		want int
	}{
		{"foreman", 7},
		{"配車の再試行", 12}, // CJK, two columns each
		{"ｱｲｳ", 3},     // halfwidth kana
		{"ＡＢ", 4},      // fullwidth latin
		{"한글", 4},      // Hangul syllables
		{"🚀 ship", 7},  // an emoji is two columns
		{"éclair", 6}, // a combining acute draws on the e
		{"a/b#10 · ✓ ▸ …", 14},
	} {
		if got := textWidth(tc.s); got != tc.want {
			t.Errorf("textWidth(%q) = %d; want %d", tc.s, got, tc.want)
		}
	}
}

func TestTruncateNeverExceedsItsWidth(t *testing.T) {
	for _, tc := range []struct {
		s     string
		width int
		want  string
	}{
		{"配車配車", 5, "配車…"},
		{"配車配車", 4, "配…"}, // the second 配 would straddle the edge, so it is dropped
		{"配車", 4, "配車"},
		{"foreman", 4, "for…"},
		{"foreman", 1, "…"},
		{"foreman", 0, ""},
	} {
		got := truncate(tc.s, tc.width)
		if got != tc.want {
			t.Errorf("truncate(%q, %d) = %q; want %q", tc.s, tc.width, got, tc.want)
		}
		if n := textWidth(got); n > tc.width {
			t.Errorf("truncate(%q, %d) is %d columns", tc.s, tc.width, n)
		}
	}
}

func TestPadFillsToTheColumnCount(t *testing.T) {
	if got := pad("配車", 6); got != "配車  " {
		t.Errorf("pad = %q", got)
	}
	if got := pad("配車配車", 4); got != "配車配車" {
		t.Errorf("pad widened a line that was already full: %q", got)
	}
}

func TestWrapLine(t *testing.T) {
	for _, tc := range []struct {
		s     string
		width int
		want  []string
	}{
		{"foreman", 3, []string{"for", "ema", "n"}},
		{"foreman", 7, []string{"foreman"}},
		{"  indented line", 6, []string{"  inde", "nted l", "ine"}},
		{"a配車", 2, []string{"a", "配", "車"}}, // 配 would straddle the edge, so it moves down
		{"ée", 1, []string{"é", "e"}},       // the combining acute stays on its e
		{"", 10, []string{""}},
		{"abc", 1, []string{"a", "b", "c"}},
		{"配", 1, []string{"配"}}, // a rune wider than the line still gets a line
		{"abc", 0, []string{"abc"}},
	} {
		got := wrapLine(tc.s, tc.width)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("wrapLine(%q, %d) = %q; want %q", tc.s, tc.width, got, tc.want)
		}
		if strings.Join(got, "") != tc.s {
			t.Errorf("wrapLine(%q, %d) lost text: %q", tc.s, tc.width, got)
		}
	}
}
