package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

// Claude drives `claude -p`. Headless Claude denies any tool not pre-approved, so
// AllowedTools must cover what the agent needs to build, test, push, and open a PR (D8).
type Claude struct {
	Bin          string
	Model        string
	AllowedTools []string
	Exec         Exec
	NewSessionID func() string
}

func (Claude) Name() task.Agent { return task.Claude }

func (c Claude) Start(ctx context.Context, in StartInput, onSession func(task.Session)) (Turn, error) {
	newID := c.NewSessionID
	if newID == nil {
		newID = NewUUID
	}
	session := task.Session{Agent: task.Claude, ID: newID()}
	onSession(session)
	return c.run(ctx, in, session, "--session-id")
}

// Resume continues a session by id; `claude --resume` keeps the id.
func (c Claude) Resume(ctx context.Context, s task.Session, in StartInput) (Turn, error) {
	return c.run(ctx, in, s, "--resume")
}

func (c Claude) run(ctx context.Context, in StartInput, session task.Session, sessionFlag string) (Turn, error) {
	args := []string{
		"-p", in.Prompt,
		sessionFlag, session.ID,
		"--output-format", "json",
		"--json-schema", string(OutcomeSchema),
		"--permission-mode", "acceptEdits",
	}
	if len(c.AllowedTools) > 0 {
		args = append(args, "--allowedTools="+strings.Join(c.AllowedTools, ","))
	}
	raw, err := c.exec(ctx, in, args)
	if err != nil {
		return Turn{Session: session}, err
	}
	outcome, err := ParseOutcome(raw)
	return Turn{Session: session, Outcome: outcome}, err
}

// ReviewTools lets the reviewer read code and git history and nothing else; dontAsk
// denies every other tool without prompting.
var ReviewTools = []string{"Read", "Grep", "Glob", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)", "Bash(git status:*)"}

// Review runs a disposable, read-only session.
func (c Claude) Review(ctx context.Context, in StartInput) (ReviewOutcome, error) {
	args := []string{
		"-p", in.Prompt,
		"--no-session-persistence",
		"--output-format", "json",
		"--json-schema", string(ReviewSchema),
		"--permission-mode", "dontAsk",
		"--allowedTools=" + strings.Join(ReviewTools, ","),
	}
	raw, err := c.exec(ctx, in, args)
	if err != nil {
		return ReviewOutcome{}, err
	}
	return ParseReview(raw)
}

// exec runs claude -p and returns its structured_output.
func (c Claude) exec(ctx context.Context, in StartInput, args []string) (json.RawMessage, error) {
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	var stdout bytes.Buffer
	runErr := c.Exec(ctx, Command{Name: c.Bin, Args: args, Dir: in.Worktree, Stdout: &stdout, Stderr: logOrDiscard(in.Log)})
	if len(bytes.TrimSpace(stdout.Bytes())) == 0 {
		return nil, fmt.Errorf("claude printed no result (%v): %w", runErr, ErrNoSession)
	}
	raw, err := parseClaudeResult(stdout.Bytes(), in.Log)
	if err != nil && runErr != nil {
		return nil, fmt.Errorf("claude exited: %w (%v)", runErr, err)
	}
	return raw, err
}

type claudeResult struct {
	SessionID         string            `json:"session_id"`
	IsError           bool              `json:"is_error"`
	Subtype           string            `json:"subtype"`
	Result            string            `json:"result"`
	StructuredOutput  json.RawMessage   `json:"structured_output"`
	PermissionDenials []json.RawMessage `json:"permission_denials"`
	TotalCostUSD      float64           `json:"total_cost_usd"`
}

func parseClaudeResult(stdout []byte, log io.Writer) (json.RawMessage, error) {
	var r claudeResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &r); err != nil {
		return nil, fmt.Errorf("claude output is not a JSON result: %w", err)
	}
	if log != nil {
		fmt.Fprintf(log, "claude: subtype=%s cost=$%.2f permission_denials=%d\n", r.Subtype, r.TotalCostUSD, len(r.PermissionDenials))
		for _, d := range r.PermissionDenials {
			fmt.Fprintf(log, "claude: denied %s\n", d)
		}
	}
	if r.IsError || len(r.StructuredOutput) == 0 || string(r.StructuredOutput) == "null" {
		return nil, fmt.Errorf("claude ended without a structured outcome (%s): %s", r.Subtype, truncate(r.Result, 500))
	}
	return r.StructuredOutput, nil
}

func NewUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func logOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
