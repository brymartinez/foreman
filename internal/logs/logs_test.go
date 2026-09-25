package logs

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriterTimestampsLinesAndCleanSummarizesJSON(t *testing.T) {
	var raw bytes.Buffer
	w := &Writer{W: &raw}
	_, _ = w.Write([]byte("foreman: starting\n2026 detail\n"))
	lines := strings.Split(strings.TrimSpace(raw.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], " foreman: starting") || !strings.Contains(lines[1], " 2026 detail") {
		t.Fatalf("raw log = %q", raw.String())
	}
	var clean bytes.Buffer
	if err := Clean(&clean, strings.NewReader(lines[0]+"\n2026-09-25T18:00:00Z {\"type\":\"thread.started\",\"thread_id\":\"t1\"}\n"), false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(clean.String(), "codex  thread started t1") {
		t.Fatalf("clean log = %q", clean.String())
	}
}
