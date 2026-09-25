package logs

import (
	"bytes"
	"context"
	"io"
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
	if err := Show(context.Background(), &clean, strings.NewReader(lines[0]+"\n2026-09-25T18:00:00Z {\"type\":\"thread.started\",\"thread_id\":\"t1\"}\n"), false, false, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(clean.String(), "codex  thread started t1") {
		t.Fatalf("clean log = %q", clean.String())
	}
}

func TestWriterFlushKeepsAnUnterminatedLastLine(t *testing.T) {
	var raw bytes.Buffer
	w := &Writer{W: &raw}
	_, _ = w.Write([]byte("partial"))
	if raw.Len() != 0 {
		t.Fatalf("wrote before the line ended: %q", raw.String())
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(raw.String(), " partial\n") {
		t.Fatalf("flushed log = %q", raw.String())
	}
}

// growingLog returns each chunk from one Read, then EOF, as a file being appended
// to does. It cancels the follow once every chunk is out.
type growingLog struct {
	chunks []string
	eof    bool
	done   func()
}

func (g *growingLog) Read(p []byte) (int, error) {
	if g.eof || len(g.chunks) == 0 {
		g.eof = false
		if len(g.chunks) == 0 {
			g.done()
		}
		return 0, io.EOF
	}
	n := copy(p, g.chunks[0])
	g.chunks, g.eof = g.chunks[1:], true
	return n, nil
}

func TestShowFollowWaitsForALineToFinish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	log := &growingLog{chunks: []string{"2026-09-25T18:00:00Z foreman: star", "ting claude\n2026-09-25T18:00:01Z done\n"}, done: cancel}
	var out bytes.Buffer
	if err := Show(ctx, &out, log, false, true, 0); err != nil {
		t.Fatal(err)
	}
	want := "2026-09-25T18:00:00Z foreman: starting claude\n2026-09-25T18:00:01Z done\n"
	if out.String() != want {
		t.Fatalf("followed log = %q, want %q", out.String(), want)
	}
}
