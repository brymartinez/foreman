package logs

import (
	"fmt"
	"strings"
	"testing"
)

func TestTailKeepsTheLastLines(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&b, "2026-09-29T10:00:00Z line %d\n", i)
	}
	got, err := Tail(strings.NewReader(b.String()), 5, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"line 16", "line 17", "line 18", "line 19", "line 20"}
	if len(got) != len(want) {
		t.Fatalf("Tail returned %d lines: %v", len(got), got)
	}
	for i, w := range want {
		if !strings.HasSuffix(got[i], w) {
			t.Errorf("line %d = %q; want it to end in %q", i, got[i], w)
		}
	}
}

func TestTailCleansAgentEvents(t *testing.T) {
	raw := strings.Join([]string{
		`2026-09-29T10:00:00Z {"type":"system","subtype":"init","session_id":"sid-7"}`,
		`2026-09-29T10:00:01Z {"type":"user"}`,
		`2026-09-29T10:00:02Z foreman: runner 42 started`,
	}, "\n") + "\n"
	got, err := Tail(strings.NewReader(raw), 10, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-09-29T10:00:00Z claude session started sid-7", "2026-09-29T10:00:02Z foreman: runner 42 started"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Tail = %q; want %q", got, want)
	}
}

func TestTailRawKeepsEveryLine(t *testing.T) {
	raw := `2026-09-29T10:00:00Z {"type":"user"}` + "\n" + `2026-09-29T10:00:01Z {"type":"result"}` + "\n"
	got, err := Tail(strings.NewReader(raw), 10, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("raw Tail dropped lines: %q", got)
	}
}

func TestTailSkipsAPartialFirstLine(t *testing.T) {
	var b strings.Builder
	for b.Len() < tailBytes+4096 {
		fmt.Fprintf(&b, "2026-09-29T10:00:00Z %s\n", strings.Repeat("x", 120))
	}
	b.WriteString("2026-09-29T10:00:00Z last\n")
	got, err := Tail(strings.NewReader(b.String()), 1000, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("Tail returned nothing")
	}
	// Every kept line starts at a real line boundary, so none is a truncated tail.
	for i, line := range got {
		if !strings.HasPrefix(line, "2026-09-29T10:00:00Z ") {
			t.Fatalf("line %d starts mid-line: %q", i, line)
		}
	}
	if !strings.HasSuffix(got[len(got)-1], "last") {
		t.Errorf("last line = %q", got[len(got)-1])
	}
}

func TestTailOfAnEmptyLog(t *testing.T) {
	got, err := Tail(strings.NewReader(""), 5, false)
	if err != nil || len(got) != 0 {
		t.Fatalf("Tail = %q, %v; want no lines", got, err)
	}
}
