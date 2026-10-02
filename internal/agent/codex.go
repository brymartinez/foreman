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
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

// Codex drives `codex exec`. The workspace-write sandbox blocks network by default;
// the agent needs it to push and open the PR (D7), so it is enabled explicitly. It also
// can't write outside the worktree, where a linked worktree keeps its git data, so that
// directory is added as a writable root (D112).
type Codex struct {
	Bin       string
	Model     string
	PlanModel string
	// ReviewModel replaces Model for reviews; "" keeps Model (D85).
	ReviewModel           string
	ReasoningEffort       string
	PlanReasoningEffort   string
	ReviewReasoningEffort string
	Exec                  Exec
}

func (Codex) Name() task.Agent { return task.Codex }

func (c Codex) Start(ctx context.Context, in StartInput, onSession func(task.Session)) (Turn, error) {
	return c.run(ctx, in, func(schema, last string) []string {
		return append([]string{
			"exec", "--json",
			"-C", in.Worktree,
			"-s", "workspace-write",
			"-c", "sandbox_workspace_write.network_access=true",
			"--output-schema", schema,
			"-o", last,
		}, gitWriteArgs(in.Worktree)...)
	}, "", onSession)
}

// Resume continues a thread by id. `codex exec resume` has no -s or -C flag, so the
// sandbox is set through -c and the directory through the process cwd.
func (c Codex) Resume(ctx context.Context, s task.Session, in StartInput) (Turn, error) {
	return c.run(ctx, in, func(schema, last string) []string {
		return append([]string{
			"exec", "resume", "--json",
			"-c", `sandbox_mode="workspace-write"`,
			"-c", "sandbox_workspace_write.network_access=true",
			"--output-schema", schema,
			"-o", last,
		}, gitWriteArgs(in.Worktree)...)
	}, s.ID, func(task.Session) {})
}

// gitWriteArgs makes the repository's git directory writable in the sandbox. A linked
// worktree's index, refs, and objects live in the main clone's .git, outside the
// worktree, so without it `git commit` fails. `exec resume` has no --add-dir flag, so
// both commands set it through -c (D112).
func gitWriteArgs(worktree string) []string {
	dir := gitCommonDir(worktree)
	if dir == "" {
		return nil
	}
	roots, _ := json.Marshal([]string{dir})
	return []string{"-c", "sandbox_workspace_write.writable_roots=" + string(roots)}
}

// gitCommonDir follows a linked worktree's .git file to the main clone's .git. It
// returns "" for a plain checkout, whose .git is already inside the worktree.
func gitCommonDir(worktree string) string {
	b, err := os.ReadFile(filepath.Join(worktree, ".git"))
	if err != nil {
		return ""
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
	if !ok {
		return ""
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(worktree, gitdir)
	}
	common, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		return filepath.Clean(gitdir)
	}
	dir := strings.TrimSpace(string(common))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(gitdir, dir)
	}
	return filepath.Clean(dir)
}

// Review runs a disposable (--ephemeral) session in the read-only sandbox.
func (c Codex) Review(ctx context.Context, in StartInput) (ReviewOutcome, error) {
	if c.ReviewModel != "" {
		c.Model = c.ReviewModel
	}
	if c.ReviewReasoningEffort != "" {
		c.ReasoningEffort = c.ReviewReasoningEffort
	}
	logTurn(in.Log, task.Codex, c.Model, in.Step)
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
	if in.Stage == task.StagePlan {
		if c.PlanModel != "" {
			c.Model = c.PlanModel
		}
		if c.PlanReasoningEffort != "" {
			c.ReasoningEffort = c.PlanReasoningEffort
		}
	}
	logTurn(in.Log, task.Codex, c.Model, in.Step)
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
	if c.ReasoningEffort != "" {
		args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", c.ReasoningEffort))
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
			// The cleaned log renders this event itself, so the adapter only counts it
			// (D125).
			if json.Unmarshal(line, &done) == nil {
				e.usage.CodexInputTokens += done.Usage.InputTokens
				e.usage.CodexCachedTokens += done.Usage.CachedInputTokens
				e.usage.CodexOutputTokens += done.Usage.OutputTokens
				e.usage.CodexReasoningTokens += done.Usage.ReasoningOutputTokens
				e.usage.CodexTurns++
			}
		}
	}
	_, _ = io.Copy(io.Discard, r)
}
