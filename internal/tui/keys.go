// Package tui is foreman's dashboard: the inbox, delegated work, and the selected
// task's log in one terminal view. Model and Frame are pure; only Run touches the
// terminal.
package tui

import "unicode/utf8"

type KeyKind int

const (
	KeyUnknown KeyKind = iota
	KeyRune
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyPageUp
	KeyPageDown
	KeyEnd
	KeyEnter
	KeyTab
	KeyBackspace
	KeyEscape
	KeyCtrlC
	KeyCtrlD
	KeyCtrlU
)

// Key is one keystroke. Rune is set only for KeyRune.
type Key struct {
	Kind KeyKind
	Rune rune
}

// DecodeKey reads the first key in p. It returns the key, the bytes it consumed, and
// whether p held a whole key: bytes that could still grow into one, a half-read escape
// sequence or a rune split across reads, consume nothing so the caller can read more
// and try again. A terminal read does not preserve key boundaries, so that happens.
//
// final says no more bytes are coming, either because stdin ended or because the
// caller waited and none did. It resolves the ambiguity the other way: a lone ESC is
// then the escape key rather than the start of an arrow key.
func DecodeKey(p []byte, final bool) (Key, int, bool) {
	if len(p) == 0 {
		return Key{}, 0, false
	}
	switch p[0] {
	case 0x1b:
		return decodeEscape(p, final)
	case '\r', '\n':
		return Key{Kind: KeyEnter}, 1, true
	case '\t':
		return Key{Kind: KeyTab}, 1, true
	case 0x7f, '\b':
		return Key{Kind: KeyBackspace}, 1, true
	case 0x03:
		return Key{Kind: KeyCtrlC}, 1, true
	case 0x04:
		return Key{Kind: KeyCtrlD}, 1, true
	case 0x15:
		return Key{Kind: KeyCtrlU}, 1, true
	}
	if p[0] < 0x20 {
		return Key{Kind: KeyUnknown}, 1, true
	}
	if !final && !utf8.FullRune(p) && len(p) < utf8.UTFMax {
		// A multi-byte rune split across reads; the rest is still coming.
		return Key{}, 0, false
	}
	r, n := utf8.DecodeRune(p)
	if r == utf8.RuneError && n <= 1 {
		// An invalid byte, or a rune whose rest never arrived; drop the byte.
		return Key{Kind: KeyUnknown}, 1, true
	}
	return Key{Kind: KeyRune, Rune: r}, n, true
}

// decodeEscape handles CSI (ESC [) and SS3 (ESC O) sequences. A lone ESC is the escape
// key, which is how the dashboard abandons a prompt, but only once nothing follows it:
// an arrow key starts with the same byte.
func decodeEscape(p []byte, final bool) (Key, int, bool) {
	if len(p) == 1 {
		if !final {
			return Key{}, 0, false
		}
		return Key{Kind: KeyEscape}, 1, true
	}
	if p[1] != '[' && p[1] != 'O' {
		return Key{Kind: KeyEscape}, 1, true
	}
	// A CSI sequence ends at its first byte in 0x40..0x7e after the parameters.
	for i := 2; i < len(p); i++ {
		if p[i] < 0x40 || p[i] > 0x7e {
			continue
		}
		return escapeKey(string(p[2:i]), p[i]), i + 1, true
	}
	if !final {
		return Key{}, 0, false
	}
	// The sequence was cut off. Give up the ESC so the rest decodes as plain runes.
	return Key{Kind: KeyEscape}, 1, true
}

func escapeKey(params string, final byte) Key {
	switch final {
	case 'A':
		return Key{Kind: KeyUp}
	case 'B':
		return Key{Kind: KeyDown}
	case 'C':
		return Key{Kind: KeyRight}
	case 'D':
		return Key{Kind: KeyLeft}
	case 'F':
		return Key{Kind: KeyEnd}
	case '~':
		switch params {
		case "5":
			return Key{Kind: KeyPageUp}
		case "6":
			return Key{Kind: KeyPageDown}
		case "4", "8":
			return Key{Kind: KeyEnd}
		}
	}
	return Key{Kind: KeyUnknown}
}
