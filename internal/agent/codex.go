package agent

import (
	"bufio"
	"cmp"
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

// Codex drives `codex exec`. Its permission profile allows worktree edits,
// network access for PRs, and the Git metadata paths needed to commit and push.
type Codex struct {
	Bin       string
	Model     string
	PlanModel string
	// ReviewModel replaces Model for reviews; "" keeps Model.
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
			"--output-schema", schema,
			"-o", last,
		}, gitPermissionArgs(in.Worktree)...)
	}, "", onSession)
}

// Resume continues a thread by id. `codex exec resume` uses the process cwd.
func (c Codex) Resume(ctx context.Context, s task.Session, in StartInput) (Turn, error) {
	return c.run(ctx, in, func(schema, last string) []string {
		return append([]string{
			"exec", "resume", "--json",
			"--output-schema", schema,
			"-o", last,
		}, gitPermissionArgs(in.Worktree)...)
	}, s.ID, func(task.Session) {})
}

// gitPermissionArgs grants the Git metadata directories used by a linked worktree.
// Files cannot be permission roots: Codex's sandbox creates a .git mount beneath
// each writable root, which fails when the root is an existing file.
func gitPermissionArgs(worktree string) []string {
	gitdir, common := gitDirs(worktree)
	rules := []string{`":root"="read"`, `":workspace_roots"={"."="write",".git"="write"}`, `":slash_tmp"="write"`, `":tmpdir"="write"`}
	paths := []string{common}
	if gitdir != common {
		paths = append(paths, gitdir)
	}
	for _, path := range paths {
		rules = append(rules, fmt.Sprintf("%q=\"write\"", path))
	}
	return []string{
		"-c", `default_permissions="foreman-git"`,
		"-c", "permissions.foreman-git.filesystem={" + strings.Join(rules, ",") + "}",
		"-c", "permissions.foreman-git.network.enabled=true",
	}
}

// gitDirs follows a linked worktree's .git file to its per-worktree metadata and
// the main clone's shared metadata. A plain checkout uses its own .git for both.
func gitDirs(worktree string) (string, string) {
	plain := filepath.Join(worktree, ".git")
	b, err := os.ReadFile(filepath.Join(worktree, ".git"))
	if err != nil {
		return plain, plain
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
	if !ok {
		return plain, plain
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(worktree, gitdir)
	}
	common, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		gitdir = filepath.Clean(gitdir)
		return gitdir, gitdir
	}
	dir := strings.TrimSpace(string(common))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(gitdir, dir)
	}
	return filepath.Clean(gitdir), filepath.Clean(dir)
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
	if p := in.Profile; p != nil {
		c.Model = cmp.Or(p.Model, c.Model)
		c.PlanModel = cmp.Or(p.PlanModel, c.PlanModel)
		c.ReasoningEffort = cmp.Or(p.ReasoningEffort, c.ReasoningEffort)
		c.PlanReasoningEffort = cmp.Or(p.PlanReasoningEffort, c.PlanReasoningEffort)
	}
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
	Item *struct {
		Type string `json:"type"`
	} `json:"item"`
}

// codexNonToolItems are the item types that are not tool calls. Every other
// completed item counts as one, so a tool type a newer Codex adds is counted too.
var codexNonToolItems = map[string]bool{"agent_message": true, "reasoning": true, "todo_list": true, "error": true}

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
		case "item.completed":
			if ev.Item != nil && ev.Item.Type != "" && !codexNonToolItems[ev.Item.Type] {
				if e.usage.ToolCalls == nil {
					e.usage.ToolCalls = map[string]int{}
				}
				e.usage.ToolCalls[ev.Item.Type]++
			}
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
			// The cleaned log renders this event itself, so the adapter only counts it.
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
