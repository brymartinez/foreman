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

func TestCleanSummarizesClaudeEvents(t *testing.T) {
	log := strings.Join([]string{
		`T1 {"type":"system","subtype":"init","session_id":"s1"}`,
		`T2 {"type":"system","subtype":"thinking_tokens"}`,
		`T3 {"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls","description":"List files"}}]}}`,
		`T4 {"type":"user","message":{"content":[{"type":"tool_result","content":"a\nb"}]}}`,
		`T5 {"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}`,
		`T6 {"type":"result","subtype":"success"}`,
		`T7 claude: subtype=success cost=$0.10 permission_denials=0 turns=2`,
	}, "\n") + "\n"
	var clean bytes.Buffer
	if err := Show(context.Background(), &clean, strings.NewReader(log), false, false, 0); err != nil {
		t.Fatal(err)
	}
	want := "T1 claude session started s1\nT3 claude Bash List files\nT5 claude message\nT7 claude: subtype=success cost=$0.10 permission_denials=0 turns=2\n"
	if clean.String() != want {
		t.Errorf("clean log =\n%s\nwant\n%s", clean.String(), want)
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

// The events are taken from a real `codex exec --json` run (0.156.1).
func TestCleanSummarizesCodexEvents(t *testing.T) {
	log := strings.Join([]string{
		`T1 {"type":"thread.started","thread_id":"t1"}`,
		`T2 {"type":"turn.started"}`,
		`T3 {"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"I’ll read the file.\nThen reply."}}`,
		`T4 {"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"/bin/zsh -lc 'cat a.txt'","aggregated_output":"","exit_code":null,"status":"in_progress"}}`,
		`T5 {"type":"item.updated","item":{"id":"item_1","type":"command_execution","command":"/bin/zsh -lc 'cat a.txt'","status":"in_progress"}}`,
		`T6 {"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"/bin/zsh -lc 'cat a.txt'","aggregated_output":"hello\nworld\n","exit_code":0,"status":"completed"}}`,
		`T7 {"type":"item.completed","item":{"id":"item_2","type":"command_execution","command":"go test ./...","aggregated_output":"FAIL\n","exit_code":1,"status":"completed"}}`,
		`T8 {"type":"item.completed","item":{"id":"item_3","type":"reasoning","text":"thinking"}}`,
		`T9 {"type":"item.completed","item":{"id":"item_4","type":"file_change","changes":[{"path":"retry.go","kind":"update"},{"path":"retry_test.go","kind":"add"}],"status":"completed"}}`,
		`T10 {"type":"item.completed","item":{"id":"item_5","type":"web_search","query":"go retry backoff"}}`,
		`T11 {"type":"item.completed","item":{"id":"item_6","type":"mcp_tool_call","server":"github","tool":"list_prs","status":"completed"}}`,
		`T12 {"type":"item.completed","item":{"id":"item_7","type":"error","message":"the sandbox denied the write"}}`,
		`T13 {"type":"error","message":"stream disconnected"}`,
		`T14 {"type":"turn.failed","error":{"message":"usage limit reached"}}`,
		`T15 {"type":"turn.completed","usage":{"input_tokens":46553,"cached_input_tokens":35712,"output_tokens":82,"reasoning_output_tokens":7}}`,
	}, "\n") + "\n"
	var clean bytes.Buffer
	if err := Show(context.Background(), &clean, strings.NewReader(log), false, false, 0); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"T1 codex  thread started t1",
		"T3 codex  message: I’ll read the file.",
		"T4 codex  bash /bin/zsh -lc 'cat a.txt'",
		"T6 codex  bash exited 0: hello",
		"T7 codex  bash exited 1: FAIL",
		"T9 codex  edit update retry.go, add retry_test.go",
		"T10 codex  search go retry backoff",
		"T11 codex  mcp github list_prs completed",
		"T12 codex  error the sandbox denied the write",
		"T13 codex  error stream disconnected",
		"T14 codex  turn failed: usage limit reached",
		"T15 codex  turn completed input=46553 cached=35712 output=82 reasoning=7 (USD unavailable)",
	}, "\n") + "\n"
	if clean.String() != want {
		t.Errorf("clean log =\n%s\nwant\n%s", clean.String(), want)
	}
	var rawOut bytes.Buffer
	if err := Show(context.Background(), &rawOut, strings.NewReader(log), true, false, 0); err != nil {
		t.Fatal(err)
	}
	if rawOut.String() != log {
		t.Errorf("raw changed the log:\n%s", rawOut.String())
	}
}

func TestCleanCutsALongCodexLine(t *testing.T) {
	long := strings.Repeat("x", 400)
	var clean bytes.Buffer
	line := `T1 {"type":"item.completed","item":{"id":"i","type":"agent_message","text":"` + long + `"}}`
	if err := Show(context.Background(), &clean, strings.NewReader(line+"\n"), false, false, 0); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(clean.String())
	if len([]rune(got)) > 180 || !strings.HasSuffix(got, "…") {
		t.Errorf("line not cut: %d runes, %q", len([]rune(got)), got)
	}
}
