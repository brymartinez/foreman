package logs

import (
	"bufio"
	"encoding/json"
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

func Clean(w io.Writer, r io.Reader, raw bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if raw {
			fmt.Fprintln(w, line)
			continue
		}
		if cleaned := cleanLine(line); cleaned != "" {
			fmt.Fprintln(w, cleaned)
		}
	}
	return sc.Err()
}

func cleanLine(line string) string {
	parts := strings.SplitN(line, " ", 2)
	if len(parts) != 2 {
		return line
	}
	var event struct {
		Type     string `json:"type"`
		ThreadID string `json:"thread_id"`
	}
	if json.Unmarshal([]byte(parts[1]), &event) == nil && event.Type != "" {
		switch event.Type {
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
	return line
}
