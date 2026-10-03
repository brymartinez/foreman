package logs

import (
	"fmt"
	"strings"
	"testing"
)

func numbered(from, to int) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		fmt.Fprintf(&b, "2026-09-29T10:00:00Z line %d\n", i)
	}
	return b.String()
}

func TestLinesReadsEveryCompleteLineInTheRange(t *testing.T) {
	log := numbered(1, 3)
	got, next, partial, err := Lines(strings.NewReader(log), 0, int64(len(log)), false)
	if err != nil || next != int64(len(log)) || partial != 0 {
		t.Fatalf("Lines = %q, next %d, partial %d, %v", got, next, partial, err)
	}
	if len(got) != 3 || !strings.HasSuffix(got[2], "line 3") {
		t.Fatalf("Lines = %q; want lines 1 to 3", got)
	}
}

func TestLinesReturnsAnUnfinishedLastLineButStopsBeforeIt(t *testing.T) {
	done := numbered(1, 2)
	log := done + "2026-09-29T10:00:00Z writing"
	got, next, partial, err := Lines(strings.NewReader(log), 0, int64(len(log)), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2] != "2026-09-29T10:00:00Z writing" || partial != 1 {
		t.Fatalf("Lines = %q, partial %d; want the unfinished line last and counted", got, partial)
	}
	if next != int64(len(done)) {
		t.Fatalf("next = %d; want %d, where the unfinished line starts", next, len(done))
	}
}

func TestLinesCleansUnlessRaw(t *testing.T) {
	log := strings.Join([]string{
		`T1 {"type":"user"}`,
		`T2 {"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"description":"ls"}},{"type":"text","text":"hi"}]}}`,
		`T3 foreman: done`,
	}, "\n") + "\n"
	clean, _, _, err := Lines(strings.NewReader(log), 0, int64(len(log)), false)
	if err != nil {
		t.Fatal(err)
	}
	// The tool result vanishes and the assistant event expands to two lines.
	want := []string{"T2 claude Bash ls", "T2 claude message", "T3 foreman: done"}
	if strings.Join(clean, "|") != strings.Join(want, "|") {
		t.Fatalf("clean = %q; want %q", clean, want)
	}
	raw, _, _, err := Lines(strings.NewReader(log), 0, int64(len(log)), true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(raw, "\n")+"\n" != log {
		t.Fatalf("raw = %q; want the file's lines unchanged", raw)
	}
}

func TestBackStartsAtTheFirstWholeLineInTheChunk(t *testing.T) {
	log := numbered(1, 100)
	end := int64(len(log))
	start, err := Back(strings.NewReader(log), end, 200)
	if err != nil {
		t.Fatal(err)
	}
	if end-start > 200 || start == 0 {
		t.Fatalf("Back = %d; want a start within 200 bytes of %d", start, end)
	}
	if start > 0 && log[start-1] != '\n' {
		t.Fatalf("Back = %d starts mid-line: %q", start, log[start:start+20])
	}
	got, _, _, _ := Lines(strings.NewReader(log), start, end, false)
	if len(got) == 0 || !strings.HasSuffix(got[len(got)-1], "line 100") {
		t.Fatalf("lines from Back = %q; want them to end at line 100", got)
	}
}

func TestBackReachesTheFileStart(t *testing.T) {
	log := numbered(1, 3)
	if start, err := Back(strings.NewReader(log), int64(len(log)), 1<<20); err != nil || start != 0 {
		t.Fatalf("Back = %d, %v; want 0 for a chunk larger than the file", start, err)
	}
	if start, err := Back(strings.NewReader(log), 0, 100); err != nil || start != 0 {
		t.Fatalf("Back from 0 = %d, %v; want 0", start, err)
	}
}

func TestBackTakesALineLongerThanTheChunkWhole(t *testing.T) {
	head := numbered(1, 1)
	long := "2026-09-29T10:00:00Z " + strings.Repeat("x", 500) + "\n"
	log := head + long
	start, err := Back(strings.NewReader(log), int64(len(log)), 100)
	if err != nil {
		t.Fatal(err)
	}
	if start != int64(len(head)) {
		t.Fatalf("Back = %d; want %d, the start of the long line", start, len(head))
	}
}

func TestPagingBackReadsEachLineOnce(t *testing.T) {
	log := numbered(1, 50)
	r := strings.NewReader(log)
	end := int64(len(log))
	var all []string
	for end > 0 {
		start, err := Back(r, end, 128)
		if err != nil {
			t.Fatal(err)
		}
		lines, _, _, err := Lines(r, start, end, false)
		if err != nil {
			t.Fatal(err)
		}
		all = append(lines, all...)
		end = start
	}
	if strings.Join(all, "\n")+"\n" != log {
		t.Fatalf("paged lines differ from the file:\n%s", strings.Join(all, "\n"))
	}
}
