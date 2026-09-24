package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/brymartinez/foreman/internal/task"
)

// Codex drives `codex exec`. The workspace-write sandbox blocks network by default;
// the agent needs it to push and open the PR (D7), so it is enabled explicitly.
type Codex struct {
	Bin   string
	Model string
	Exec  Exec
}

func (Codex) Name() task.Agent { return task.Codex }

func (c Codex) Start(ctx context.Context, in StartInput, onSession func(task.Session)) (Turn, error) {
	return c.run(ctx, in, func(schema, last string) []string {
		return []string{
			"exec", "--json",
			"-C", in.Worktree,
			"-s", "workspace-write",
			"-c", "sandbox_workspace_write.network_access=true",
			"--output-schema", schema,
			"-o", last,
		}
	}, "", onSession)
}

// Resume continues a thread by id. `codex exec resume` has no -s or -C flag, so the
// sandbox is set through -c and the directory through the process cwd.
func (c Codex) Resume(ctx context.Context, s task.Session, in StartInput) (Turn, error) {
	return c.run(ctx, in, func(schema, last string) []string {
		return []string{
			"exec", "resume", "--json",
			"-c", `sandbox_mode="workspace-write"`,
			"-c", "sandbox_workspace_write.network_access=true",
			"--output-schema", schema,
			"-o", last,
		}
	}, s.ID, func(task.Session) {})
}

func (c Codex) run(ctx context.Context, in StartInput, baseArgs func(schema, last string) []string, resumeID string, onSession func(task.Session)) (Turn, error) {
	dir, err := os.MkdirTemp("", "foreman-codex-*")
	if err != nil {
		return Turn{}, err
	}
	defer os.RemoveAll(dir)
	schemaPath, lastPath := filepath.Join(dir, "outcome.schema.json"), filepath.Join(dir, "last.json")
	if err := os.WriteFile(schemaPath, OutcomeSchema, 0o600); err != nil {
		return Turn{}, err
	}
	args := baseArgs(schemaPath, lastPath)
	if c.Model != "" {
		args = append(args, "-m", c.Model)
	}
	if resumeID != "" {
		args = append(args, resumeID)
	}
	args = append(args, in.Prompt)

	events := &codexEvents{log: logOrDiscard(in.Log), onSession: func(id string) {
		onSession(task.Session{Agent: task.Codex, ID: id})
	}}
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() { events.consume(pr); close(done) }()
	runErr := c.Exec(ctx, Command{Name: c.Bin, Args: args, Dir: in.Worktree, Stdout: pw, Stderr: logOrDiscard(in.Log)})
	pw.Close()
	<-done

	session := task.Session{Agent: task.Codex, ID: events.threadID}
	if events.threadID == "" {
		return Turn{}, fmt.Errorf("codex never started a thread (%v): %w", runErr, ErrNoSession)
	}
	last, err := os.ReadFile(lastPath)
	if err != nil || len(last) == 0 {
		return Turn{Session: session}, fmt.Errorf("codex ended without a final message: %v %s", errors.Join(runErr, err), events.lastError)
	}
	outcome, err := ParseOutcome(last)
	return Turn{Session: session, Outcome: outcome}, err
}

type codexEvents struct {
	log       io.Writer
	onSession func(string)
	threadID  string
	lastError string
}

type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Message  string `json:"message"`
	Error    *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// consume copies the JSONL event stream to the log and reports thread.started immediately.
func (e *codexEvents) consume(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		fmt.Fprintf(e.log, "%s\n", line)
		var ev codexEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "thread.started":
			if e.threadID == "" && ev.ThreadID != "" {
				e.threadID = ev.ThreadID
				e.onSession(ev.ThreadID)
			}
		case "error":
			e.lastError = ev.Message
		case "turn.failed":
			if ev.Error != nil {
				e.lastError = ev.Error.Message
			}
		}
	}
	_, _ = io.Copy(io.Discard, r)
}
