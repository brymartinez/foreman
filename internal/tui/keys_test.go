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
		"\x1b[F":  {Kind: KeyEnd},
		"\x1bOF":  {Kind: KeyEnd},
		"\r":      {Kind: KeyEnter},
		"\n":      {Kind: KeyEnter},
		"\t":      {Kind: KeyTab},
		"\x7f":    {Kind: KeyBackspace},
		"\b":      {Kind: KeyBackspace},
		"\x03":    {Kind: KeyCtrlC},
		"\x1b":    {Kind: KeyEscape},
		"q":       {Kind: KeyRune, Rune: 'q'},
		"1":       {Kind: KeyRune, Rune: '1'},
	} {
		got, n, ok := DecodeKey([]byte(in))
		if !ok || got != want || n != len(in) {
			t.Errorf("DecodeKey(%q) = %+v, %d, %v; want %+v, %d", in, got, n, ok, want, len(in))
		}
	}
}

func TestDecodeKeyWaitsForTheRestOfAnEscapeSequence(t *testing.T) {
	for _, in := range []string{"\x1b[", "\x1b[5", "\x1bO"} {
		if got, n, ok := DecodeKey([]byte(in)); ok || n != 0 {
			t.Errorf("DecodeKey(%q) = %+v, %d, %v; want it to wait for more", in, got, n, ok)
		}
	}
	if _, n, ok := DecodeKey(nil); ok || n != 0 {
		t.Errorf("DecodeKey(nil) consumed %d bytes", n)
	}
}

func TestDecodeKeyConsumesOneKeyAtATime(t *testing.T) {
	buf := []byte("\x1b[Bq")
	got, n, ok := DecodeKey(buf)
	if !ok || got.Kind != KeyDown || n != 3 {
		t.Fatalf("first key = %+v, %d, %v", got, n, ok)
	}
	got, n, ok = DecodeKey(buf[n:])
	if !ok || got.Kind != KeyRune || got.Rune != 'q' || n != 1 {
		t.Fatalf("second key = %+v, %d, %v", got, n, ok)
	}
}

func TestDecodeKeyDropsAnUnknownEscapeSequence(t *testing.T) {
	got, n, ok := DecodeKey([]byte("\x1b[200~x"))
	if !ok || got.Kind != KeyUnknown || n != 6 {
		t.Fatalf("DecodeKey = %+v, %d, %v; want the whole sequence dropped", got, n, ok)
	}
}

func TestDecodeKeyReadsAMultiByteRune(t *testing.T) {
	got, n, ok := DecodeKey([]byte("é"))
	if !ok || got.Kind != KeyRune || got.Rune != 'é' || n != 2 {
		t.Fatalf("DecodeKey = %+v, %d, %v", got, n, ok)
	}
}
