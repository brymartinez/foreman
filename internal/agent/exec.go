package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

type StartInput struct {
	Worktree string
	Prompt   string
	Stage    task.Stage
	// Step names the work for the turn's agent:model:step log line; "" logs no line.
	Step string
	// Log receives the agent's stderr and progress events; the runner points it at the task log.
	Log          io.Writer
	MaxBudgetUSD float64
}

// Steps name the work a turn does in its log line. Feedback and merge turns run
// with the implementation model but keep their own names.
const (
	StepPlan      = "plan"
	StepImplement = "implement"
	StepReview    = "review"
	StepFeedback  = "feedback"
	StepMerge     = "merge"
)

// logTurn writes the agent:model:step line that opens a turn. An empty model means
// the CLI default.
func logTurn(w io.Writer, a task.Agent, model, step string) {
	if step == "" {
		return
	}
	if model == "" {
		model = "default"
	}
	fmt.Fprintf(logOrDiscard(w), "%s:%s:%s\n", a, model, step)
}

// ErrNoSession means the CLI never produced a session: nothing ran. For Resume it
// usually means the session is gone, and a fresh turn from durable state is safe.
var ErrNoSession = errors.New("agent produced no session")

type Turn struct {
	Session task.Session
	Outcome Outcome
	Usage   task.Usage
}

// Command is one agent CLI invocation. Adapters build it; Exec runs it.
type Command struct {
	Name   string
	Args   []string
	Dir    string
	Stdout io.Writer
	Stderr io.Writer
}

// Exec runs a Command to completion. Tests replace it with recorded output.
type Exec func(ctx context.Context, c Command) error

// RunCommand connects stdin to /dev/null: claude -p waits 3s for piped stdin otherwise.
// The agent runs in its own process group so a cancelled run stops the tools it
// started too; WaitDelay stops a leftover grandchild holding the output pipe from
// keeping the runner waiting.
func RunCommand(ctx context.Context, c Command) error {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = c.Dir, c.Stdout, c.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devnull.Close()
	cmd.Stdin = devnull
	return cmd.Run()
}
