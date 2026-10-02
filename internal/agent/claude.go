package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

// Claude drives `claude -p`.
type Claude struct {
	Bin   string
	Model string
	// PlanModel replaces Model while writing or revising the plan.
	PlanModel string
	// ReviewModel replaces Model for reviews; "" keeps Model.
	ReviewModel  string
	Exec         Exec
	NewSessionID func() string
}

// ImplementTools are pre-approved for the implementer. Headless Claude denies any tool
// not pre-approved, so they cover building, testing, pushing, and opening a PR, plus
// web lookups to match Codex's network access.
var ImplementTools = []string{"Bash", "WebFetch", "WebSearch"}

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
	if in.Stage == task.StagePlan && c.PlanModel != "" {
		c.Model = c.PlanModel
	}
	logTurn(in.Log, task.Claude, c.Model, in.Step)
	if in.Log != nil {
		fmt.Fprintf(in.Log, "claude: transcript %s\n", ClaudeTranscript(in.Worktree, session.ID))
	}
	args := []string{
		"-p", in.Prompt,
		sessionFlag, session.ID,
		"--output-format", "stream-json", "--verbose",
		"--json-schema", string(OutcomeSchema),
		"--permission-mode", "acceptEdits",
		"--allowedTools=" + strings.Join(ImplementTools, ","),
	}
	if in.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", in.MaxBudgetUSD))
	}
	raw, cost, err := c.exec(ctx, in, args)
	if err != nil {
		return Turn{Session: session}, err
	}
	outcome, err := ParseOutcome(raw)
	return Turn{Session: session, Outcome: outcome, Usage: task.Usage{ClaudeCostUSD: cost, ClaudeTurns: 1}}, err
}

// ReviewTools lets the reviewer read code and git history and nothing else; dontAsk
// denies every other tool without prompting.
var ReviewTools = []string{"Read", "Grep", "Glob", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)", "Bash(git status:*)"}

// Review runs a disposable, read-only session.
func (c Claude) Review(ctx context.Context, in StartInput) (ReviewOutcome, error) {
	if c.ReviewModel != "" {
		c.Model = c.ReviewModel
	}
	logTurn(in.Log, task.Claude, c.Model, in.Step)
	args := []string{
		"-p", in.Prompt,
		"--no-session-persistence",
		"--output-format", "stream-json", "--verbose",
		"--json-schema", string(ReviewSchema),
		"--permission-mode", "dontAsk",
		"--allowedTools=" + strings.Join(ReviewTools, ","),
	}
	if in.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", in.MaxBudgetUSD))
	}
	raw, cost, err := c.exec(ctx, in, args)
	if err != nil {
		return ReviewOutcome{}, err
	}
	out, err := ParseReview(raw)
	out.Usage = task.Usage{ClaudeCostUSD: cost, ClaudeTurns: 1}
	return out, err
}

// exec runs claude -p, copies its stream-json events to the log, and returns the
// result event's structured_output.
func (c Claude) exec(ctx context.Context, in StartInput, args []string) (json.RawMessage, float64, error) {
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	pr, pw := io.Pipe()
	var result []byte
	done := make(chan struct{})
	go func() { result = consumeClaudeStream(pr, logOrDiscard(in.Log)); close(done) }()
	runErr := c.Exec(ctx, Command{Name: c.Bin, Args: args, Dir: in.Worktree, Stdout: pw, Stderr: logOrDiscard(in.Log)})
	pw.Close()
	<-done
	if result == nil {
		return nil, 0, fmt.Errorf("claude printed no result (%v): %w", runErr, ErrNoSession)
	}
	var r claudeResult
	_ = json.Unmarshal(result, &r)
	// A resume of a missing session reports error_during_execution without a turn.
	if r.IsError && r.NumTurns == 0 && r.Subtype == "error_during_execution" {
		return nil, 0, fmt.Errorf("claude ran no turn (%v): %w", runErr, ErrNoSession)
	}
	raw, err := parseClaudeResult(result, in.Log)
	if err != nil && runErr != nil {
		return nil, 0, fmt.Errorf("claude exited: %w (%v)", runErr, err)
	}
	return raw, r.TotalCostUSD, err
}

// consumeClaudeStream copies each event line to the log and returns the last result
// event. Other events can follow the result, so it reads to the end.
func consumeClaudeStream(r io.Reader, log io.Writer) []byte {
	var result []byte
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		fmt.Fprintf(log, "%s\n", line)
		var ev struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &ev) == nil && ev.Type == "result" {
			result = bytes.Clone(line)
		}
	}
	_, _ = io.Copy(io.Discard, r)
	return result
}

var transcriptUnsafe = regexp.MustCompile(`[^a-zA-Z0-9]`)

// ClaudeTranscript is where Claude Code keeps a session's full transcript: the
// project directory is the cwd with every non-alphanumeric character replaced by "-".
// It holds every tool call, its output, and per-turn token usage.
func ClaudeTranscript(worktree, sessionID string) string {
	root := os.Getenv("CLAUDE_CONFIG_DIR")
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".claude")
	}
	return filepath.Join(root, "projects", transcriptUnsafe.ReplaceAllString(worktree, "-"), sessionID+".jsonl")
}

type claudeResult struct {
	SessionID         string            `json:"session_id"`
	IsError           bool              `json:"is_error"`
	Subtype           string            `json:"subtype"`
	Result            string            `json:"result"`
	StructuredOutput  json.RawMessage   `json:"structured_output"`
	PermissionDenials []json.RawMessage `json:"permission_denials"`
	TotalCostUSD      float64           `json:"total_cost_usd"`
	NumTurns          int               `json:"num_turns"`
}

func parseClaudeResult(result []byte, log io.Writer) (json.RawMessage, error) {
	var r claudeResult
	if err := json.Unmarshal(result, &r); err != nil {
		return nil, fmt.Errorf("claude output is not a JSON result: %w", err)
	}
	if log != nil {
		fmt.Fprintf(log, "claude: subtype=%s cost=$%.2f permission_denials=%d turns=%d\n", r.Subtype, r.TotalCostUSD, len(r.PermissionDenials), r.NumTurns)
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
