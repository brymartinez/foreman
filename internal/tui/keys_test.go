package tui

import "testing"

func TestDecodeKey(t *testing.T) {
	for in, want := range map[string]Key{
		"\x1b[A":  {Kind: KeyUp},
		"\x1b[B":  {Kind: KeyDown},
		"\x1bOA":  {Kind: KeyUp},
		"\x1bOB":  {Kind: KeyDown},
		"\x1b[5~": {Kind: KeyPageUp},
		"\x1b[6~": {Kind: KeyPageDown},
		"\x1b[C":  {Kind: KeyRight},
		"\x1b[D":  {Kind: KeyLeft},
		"\x1bOC":  {Kind: KeyRight},
		"\x1bOD":  {Kind: KeyLeft},
		"\x1b[F":  {Kind: KeyEnd},
		"\x1bOF":  {Kind: KeyEnd},
		"\r":      {Kind: KeyEnter},
		"\n":      {Kind: KeyEnter},
		"\t":      {Kind: KeyTab},
		"\x7f":    {Kind: KeyBackspace},
		"\b":      {Kind: KeyBackspace},
		"\x03":    {Kind: KeyCtrlC},
		"\x04":    {Kind: KeyCtrlD},
		"\x15":    {Kind: KeyCtrlU},
		"q":       {Kind: KeyRune, Rune: 'q'},
		"1":       {Kind: KeyRune, Rune: '1'},
	} {
		// A whole key decodes the same either way; only a cut-off one reads final.
		for _, final := range []bool{false, true} {
			got, n, ok := DecodeKey([]byte(in), final)
			if !ok || got != want || n != len(in) {
				t.Errorf("DecodeKey(%q, %v) = %+v, %d, %v; want %+v, %d", in, final, got, n, ok, want, len(in))
			}
		}
	}
}

func TestDecodeKeyWaitsForTheRestOfAKey(t *testing.T) {
	// A terminal read can end anywhere, including after ESC: the rest of an arrow key
	// or of a multi-byte rune may still be on its way.
	for _, in := range []string{"\x1b", "\x1b[", "\x1b[5", "\x1bO", "\xc3"} {
		if got, n, ok := DecodeKey([]byte(in), false); ok || n != 0 {
			t.Errorf("DecodeKey(%q, false) = %+v, %d, %v; want it to wait for more", in, got, n, ok)
		}
	}
	if _, n, ok := DecodeKey(nil, false); ok || n != 0 {
		t.Errorf("DecodeKey(nil) consumed %d bytes", n)
	}
}

func TestDecodeKeyResolvesACutOffKeyWhenNothingFollows(t *testing.T) {
	// Nothing followed, so the ESC was the escape key and the lone byte was junk.
	if got, n, ok := DecodeKey([]byte("\x1b"), true); !ok || got.Kind != KeyEscape || n != 1 {
		t.Errorf("lone ESC = %+v, %d, %v; want the escape key", got, n, ok)
	}
	if got, n, ok := DecodeKey([]byte("\x1b["), true); !ok || got.Kind != KeyEscape || n != 1 {
		t.Errorf("cut-off sequence = %+v, %d, %v; want the escape key", got, n, ok)
	}
	if got, n, ok := DecodeKey([]byte("\xc3"), true); !ok || got.Kind != KeyUnknown || n != 1 {
		t.Errorf("half a rune = %+v, %d, %v; want the byte dropped", got, n, ok)
	}
}

func TestDecodeKeyConsumesOneKeyAtATime(t *testing.T) {
	buf := []byte("\x1b[Bq")
	got, n, ok := DecodeKey(buf, false)
	if !ok || got.Kind != KeyDown || n != 3 {
		t.Fatalf("first key = %+v, %d, %v", got, n, ok)
	}
	got, n, ok = DecodeKey(buf[n:], false)
	if !ok || got.Kind != KeyRune || got.Rune != 'q' || n != 1 {
		t.Fatalf("second key = %+v, %d, %v", got, n, ok)
	}
}

func TestDecodeKeyDropsAnUnknownEscapeSequence(t *testing.T) {
	got, n, ok := DecodeKey([]byte("\x1b[200~x"), false)
	if !ok || got.Kind != KeyUnknown || n != 6 {
		t.Fatalf("DecodeKey = %+v, %d, %v; want the whole sequence dropped", got, n, ok)
	}
}

func TestDecodeKeyReadsAMultiByteRune(t *testing.T) {
	got, n, ok := DecodeKey([]byte("é"), false)
	if !ok || got.Kind != KeyRune || got.Rune != 'é' || n != 2 {
		t.Fatalf("DecodeKey = %+v, %d, %v", got, n, ok)
	}
}
