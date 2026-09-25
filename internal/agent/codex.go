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

// Review runs a disposable (--ephemeral) session in the read-only sandbox.
func (c Codex) Review(ctx context.Context, in StartInput) (ReviewOutcome, error) {
	_, raw, usage, err := c.exec(ctx, in, ReviewSchema, func(schema, last string) []string {
		return []string{
			"exec", "--json", "--ephemeral",
			"-C", in.Worktree,
			"-s", "read-only",
			"--output-schema", schema,
			"-o", last,
		}
	}, "", func(task.Session) {})
	if err != nil {
		return ReviewOutcome{}, err
	}
	out, err := ParseReview(raw)
	out.Usage = usage
	return out, err
}

func (c Codex) run(ctx context.Context, in StartInput, baseArgs func(schema, last string) []string, resumeID string, onSession func(task.Session)) (Turn, error) {
	session, raw, usage, err := c.exec(ctx, in, OutcomeSchema, baseArgs, resumeID, onSession)
	if err != nil {
		return Turn{Session: session, Usage: usage}, err
	}
	outcome, err := ParseOutcome(raw)
	return Turn{Session: session, Outcome: outcome, Usage: usage}, err
}

// exec runs codex with schema and returns the thread and the final message.
func (c Codex) exec(ctx context.Context, in StartInput, schema []byte, baseArgs func(schema, last string) []string, resumeID string, onSession func(task.Session)) (task.Session, []byte, task.Usage, error) {
	dir, err := os.MkdirTemp("", "foreman-codex-*")
	if err != nil {
		return task.Session{}, nil, task.Usage{}, err
	}
	defer os.RemoveAll(dir)
	schemaPath, lastPath := filepath.Join(dir, "schema.json"), filepath.Join(dir, "last.json")
	if err := os.WriteFile(schemaPath, schema, 0o600); err != nil {
		return task.Session{}, nil, task.Usage{}, err
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
		return task.Session{}, nil, events.usage, fmt.Errorf("codex never started a thread (%v): %w", runErr, ErrNoSession)
	}
	last, err := os.ReadFile(lastPath)
	if err != nil || len(last) == 0 {
		return session, nil, events.usage, fmt.Errorf("codex ended without a final message: %v %s", errors.Join(runErr, err), events.lastError)
	}
	return session, last, events.usage, nil
}

type codexEvents struct {
	log       io.Writer
	onSession func(string)
	threadID  string
	lastError string
	usage     task.Usage
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
		case "turn.completed":
			var done struct {
				Usage struct {
					InputTokens           int64 `json:"input_tokens"`
					CachedInputTokens     int64 `json:"cached_input_tokens"`
					OutputTokens          int64 `json:"output_tokens"`
					ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(line, &done) == nil {
				e.usage.CodexInputTokens += done.Usage.InputTokens
				e.usage.CodexCachedTokens += done.Usage.CachedInputTokens
				e.usage.CodexOutputTokens += done.Usage.OutputTokens
				e.usage.CodexReasoningTokens += done.Usage.ReasoningOutputTokens
				e.usage.CodexTurns++
				fmt.Fprintf(e.log, "codex: turn completed input=%d cached=%d output=%d reasoning=%d (USD unavailable)\n", done.Usage.InputTokens, done.Usage.CachedInputTokens, done.Usage.OutputTokens, done.Usage.ReasoningOutputTokens)
			}
		}
	}
	_, _ = io.Copy(io.Discard, r)
}
