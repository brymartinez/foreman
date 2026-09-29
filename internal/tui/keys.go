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
	KeyPageUp
	KeyPageDown
	KeyEnd
	KeyEnter
	KeyTab
	KeyBackspace
	KeyEscape
	KeyCtrlC
)

// Key is one keystroke. Rune is set only for KeyRune.
type Key struct {
	Kind KeyKind
	Rune rune
}

// DecodeKey reads the first key in p. It returns the key, the bytes it consumed, and
// whether p held a whole key: a partial escape sequence consumes nothing, so the
// caller can read more and try again.
func DecodeKey(p []byte) (Key, int, bool) {
	if len(p) == 0 {
		return Key{}, 0, false
	}
	switch p[0] {
	case 0x1b:
		return decodeEscape(p)
	case '\r', '\n':
		return Key{Kind: KeyEnter}, 1, true
	case '\t':
		return Key{Kind: KeyTab}, 1, true
	case 0x7f, '\b':
		return Key{Kind: KeyBackspace}, 1, true
	case 0x03:
		return Key{Kind: KeyCtrlC}, 1, true
	}
	if p[0] < 0x20 {
		return Key{Kind: KeyUnknown}, 1, true
	}
	r, n := utf8.DecodeRune(p)
	if r == utf8.RuneError && n <= 1 {
		// Either an invalid byte or a rune split across reads; drop the byte.
		return Key{Kind: KeyUnknown}, 1, true
	}
	return Key{Kind: KeyRune, Rune: r}, n, true
}

// decodeEscape handles CSI (ESC [) and SS3 (ESC O) sequences. A lone ESC is the
// escape key, which is how the dashboard abandons a prompt.
func decodeEscape(p []byte) (Key, int, bool) {
	if len(p) == 1 {
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
	return Key{}, 0, false
}

func escapeKey(params string, final byte) Key {
	switch final {
	case 'A':
		return Key{Kind: KeyUp}
	case 'B':
		return Key{Kind: KeyDown}
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
