package logs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Writer prefixes every complete line with the time it was written.
type Writer struct {
	W   io.Writer
	buf string
	mu  sync.Mutex
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf += string(p)
	for {
		i := strings.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		if err := w.line(w.buf[:i]); err != nil {
			return 0, err
		}
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// Flush writes a final line that has no trailing newline.
func (w *Writer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.buf
	w.buf = ""
	return w.line(s)
}

func (w *Writer) line(s string) error {
	if s == "" {
		return nil
	}
	_, err := fmt.Fprintf(w.W, "%s %s\n", time.Now().Format(time.RFC3339), s)
	return err
}

// Show prints the log at r, cleaned unless raw. With follow it keeps reading as the
// runner appends, until ctx ends, and holds back a line until it is complete.
func Show(ctx context.Context, w io.Writer, r io.Reader, raw, follow bool, poll time.Duration) error {
	br := bufio.NewReaderSize(r, 64*1024)
	var partial string
	for {
		chunk, err := br.ReadString('\n')
		partial += chunk
		if err == nil {
			printLine(w, strings.TrimSuffix(partial, "\n"), raw)
			partial = ""
			continue
		}
		if !errors.Is(err, io.EOF) {
			return err
		}
		if !follow {
			if partial != "" {
				printLine(w, partial, raw)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(poll):
		}
	}
}

// tailBytes bounds how much of a log Tail reads. A task that runs for hours writes
// far more than a pane can show, and Tail is called once a second.
const tailBytes = 128 << 10

// Tail returns the last n display lines of the log at r, cleaned unless raw. It reads
// only the final tailBytes, and drops the partial line that window starts in.
func Tail(r io.ReadSeeker, n int, raw bool) ([]string, error) {
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	start := max(size-tailBytes, 0)
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	text := string(data)
	if start > 0 {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		} else {
			text = ""
		}
	}
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if !raw {
			line = cleanLine(line)
		}
		for _, out := range strings.Split(line, "\n") {
			if out != "" {
				lines = append(lines, out)
			}
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

func printLine(w io.Writer, line string, raw bool) {
	if !raw {
		line = cleanLine(line)
	}
	if line != "" {
		fmt.Fprintln(w, line)
	}
}

func cleanLine(line string) string {
	at, payload, ok := strings.Cut(line, " ")
	if !ok {
		return line
	}
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal([]byte(payload), &head) != nil || head.Type == "" {
		return line
	}
	switch head.Type {
	case "system", "assistant", "user", "result", "rate_limit_event", "stream_event":
		return claudeLine(at, payload, head.Type)
	}
	return codexLine(at, payload, head.Type)
}

type claudeEvent struct {
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Message   struct {
		Content []struct {
			Type  string `json:"type"`
			Name  string `json:"name"`
			Input struct {
				Description string `json:"description"`
				FilePath    string `json:"file_path"`
			} `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

func claudeLine(at, payload, kind string) string {
	var event claudeEvent
	if json.Unmarshal([]byte(payload), &event) != nil {
		return ""
	}
	switch kind {
	case "system":
		if event.Subtype == "init" {
			return at + " claude session started " + event.SessionID
		}
		return ""
	case "assistant":
		var out []string
		for _, c := range event.Message.Content {
			switch c.Type {
			case "tool_use":
				detail := c.Input.Description
				if detail == "" {
					detail = c.Input.FilePath
				}
				out = append(out, strings.TrimSpace(at+" claude "+c.Name+" "+detail))
			case "text":
				out = append(out, at+" claude message")
			}
		}
		return strings.Join(out, "\n")
	}
	// Tool results stay in the raw log; the adapter logs the result's summary.
	return ""
}

// codexEvent covers every `codex exec --json` event the clean log reports. The item
// object carries the substance of a run, and its own type says what it holds (D125).
type codexEvent struct {
	ThreadID string `json:"thread_id"`
	Message  string `json:"message"`
	Error    *struct {
		Message string `json:"message"`
	} `json:"error"`
	Usage *struct {
		InputTokens           int64 `json:"input_tokens"`
		CachedInputTokens     int64 `json:"cached_input_tokens"`
		OutputTokens          int64 `json:"output_tokens"`
		ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	} `json:"usage"`
	Item *struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Message string `json:"message"`
		Command string `json:"command"`
		Output  string `json:"aggregated_output"`
		Exit    *int   `json:"exit_code"`
		Query   string `json:"query"`
		Server  string `json:"server"`
		Tool    string `json:"tool"`
		Status  string `json:"status"`
		Changes []struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
		} `json:"changes"`
	} `json:"item"`
}

func codexLine(at, payload, kind string) string {
	var event codexEvent
	if json.Unmarshal([]byte(payload), &event) != nil {
		return ""
	}
	say := func(text string) string {
		if text = strings.TrimSpace(text); text == "" {
			return ""
		}
		return at + " codex  " + text
	}
	switch kind {
	case "thread.started":
		return say("thread started " + event.ThreadID)
	case "turn.started":
		return ""
	case "turn.completed":
		if event.Usage == nil {
			return ""
		}
		// Codex reports tokens and no price, so the log says so rather than leave the
		// reader to wonder what a run cost.
		return say(fmt.Sprintf("turn completed input=%d cached=%d output=%d reasoning=%d (USD unavailable)",
			event.Usage.InputTokens, event.Usage.CachedInputTokens, event.Usage.OutputTokens, event.Usage.ReasoningOutputTokens))
	case "turn.failed":
		if event.Error != nil {
			return say("turn failed: " + event.Error.Message)
		}
		return say("turn failed")
	case "error":
		return say("error " + event.Message)
	case "item.started", "item.updated", "item.completed":
		if event.Item == nil {
			return ""
		}
		return say(codexItem(kind, event))
	}
	return say(strings.TrimPrefix(kind, "item."))
}

// codexItem is one item event as a line. A command is reported twice on purpose: once
// as it starts, so a long run shows what it is waiting on, and once with its result.
// Reasoning and the todo list are noise in a log that is read after the fact, so they
// stay in the raw events.
func codexItem(kind string, event codexEvent) string {
	it := event.Item
	switch it.Type {
	case "agent_message":
		if kind != "item.completed" {
			return ""
		}
		return "message: " + cut(firstLine(it.Text))
	case "command_execution":
		switch {
		case kind == "item.started":
			return "bash " + cut(firstLine(it.Command))
		case kind != "item.completed":
			return ""
		case it.Exit == nil:
			return "bash ended: " + cut(firstLine(it.Command))
		case strings.TrimSpace(it.Output) == "":
			return fmt.Sprintf("bash exited %d", *it.Exit)
		}
		return fmt.Sprintf("bash exited %d: %s", *it.Exit, cut(firstLine(it.Output)))
	case "file_change":
		if kind != "item.completed" {
			return ""
		}
		changes := make([]string, len(it.Changes))
		for i, c := range it.Changes {
			changes[i] = strings.TrimSpace(c.Kind + " " + c.Path)
		}
		return cut("edit " + strings.Join(changes, ", "))
	case "web_search":
		if kind != "item.completed" {
			return ""
		}
		return "search " + cut(it.Query)
	case "mcp_tool_call":
		if kind != "item.completed" {
			return ""
		}
		return cut(strings.TrimSpace("mcp " + it.Server + " " + it.Tool + " " + it.Status))
	case "error":
		return "error " + cut(firstLine(it.Message+it.Text))
	}
	return ""
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// maxDetail bounds one log line's detail. A command's output can run to megabytes, and
// the whole of it belongs in the raw log, not in a pane one line tall.
const maxDetail = 160

func cut(s string) string {
	if r := []rune(s); len(r) > maxDetail {
		return string(r[:maxDetail]) + "…"
	}
	return s
}
