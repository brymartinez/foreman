package tui

import "unicode"

// textWidth is the columns a string takes on the terminal. Frame's arithmetic counts
// columns, not runes: a Japanese issue title is twice as wide as its rune count, and
// counting runes let a pane's text run past its column and wrap into the pane beside
// it.
func textWidth(s string) int {
	n := 0
	for _, r := range s {
		n += runeWidth(r)
	}
	return n
}

// runeWidth is the columns one rune takes: none for a mark that draws on the rune
// before it, two in the East Asian wide and fullwidth ranges, one otherwise.
func runeWidth(r rune) int {
	switch {
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		return 0
	case unicode.Is(wideRunes, r):
		return 2
	}
	return 1
}

// wideRunes are the blocks a terminal draws in two columns. The standard library has
// no width table, and a dashboard is not worth a dependency for one, so this lists the
// East Asian wide and fullwidth blocks an issue title or a runner log can hold. The
// ambiguous-width runes foreman draws itself, such as · ✓ ▸ and …, are one column in a
// terminal that is not set to a CJK locale, so they are left out.
var wideRunes = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x1100, Hi: 0x115f, Stride: 1}, // Hangul Jamo
		{Lo: 0x2e80, Hi: 0x303e, Stride: 1}, // CJK radicals, Kangxi, CJK symbols
		{Lo: 0x3041, Hi: 0x33ff, Stride: 1}, // kana, compatibility jamo, CJK compatibility
		{Lo: 0x3400, Hi: 0x4dbf, Stride: 1}, // CJK extension A
		{Lo: 0x4e00, Hi: 0x9fff, Stride: 1}, // CJK unified ideographs
		{Lo: 0xa000, Hi: 0xa4cf, Stride: 1}, // Yi
		{Lo: 0xac00, Hi: 0xd7a3, Stride: 1}, // Hangul syllables
		{Lo: 0xf900, Hi: 0xfaff, Stride: 1}, // CJK compatibility ideographs
		{Lo: 0xfe30, Hi: 0xfe6f, Stride: 1}, // CJK compatibility forms
		{Lo: 0xff00, Hi: 0xff60, Stride: 1}, // fullwidth forms
		{Lo: 0xffe0, Hi: 0xffe6, Stride: 1}, // fullwidth signs
	},
	R32: []unicode.Range32{
		{Lo: 0x1f300, Hi: 0x1f64f, Stride: 1}, // pictographs and emoticons
		{Lo: 0x1f680, Hi: 0x1f6ff, Stride: 1}, // transport symbols
		{Lo: 0x1f900, Hi: 0x1f9ff, Stride: 1}, // supplemental pictographs
		{Lo: 0x20000, Hi: 0x3fffd, Stride: 1}, // CJK extension B and later
	},
}
