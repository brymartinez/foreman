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
	parts := strings.SplitN(line, " ", 2)
	if len(parts) != 2 {
		return line
	}
	var event struct {
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		ThreadID  string `json:"thread_id"`
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
	if json.Unmarshal([]byte(parts[1]), &event) != nil || event.Type == "" {
		return line
	}
	switch event.Type {
	case "system":
		if event.Subtype == "init" {
			return parts[0] + " claude session started " + event.SessionID
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
				out = append(out, strings.TrimSpace(parts[0]+" claude "+c.Name+" "+detail))
			case "text":
				out = append(out, parts[0]+" claude message")
			}
		}
		return strings.Join(out, "\n")
	case "user", "result", "rate_limit_event", "stream_event":
		// Tool results stay in the raw log; the adapter logs the result's summary.
		return ""
	case "thread.started":
		return parts[0] + " codex  thread started " + event.ThreadID
	case "turn.completed":
		return ""
	case "turn.failed":
		return parts[0] + " codex  turn failed"
	default:
		return parts[0] + " codex  " + strings.TrimPrefix(event.Type, "item.")
	}
}
