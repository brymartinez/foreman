package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"

	"github.com/brymartinez/foreman/internal/task"
)

type StartInput struct {
	Worktree string
	Prompt   string
	// Log receives the agent's stderr and progress events; the runner points it at the task log.
	Log          io.Writer
	MaxBudgetUSD float64
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
func RunCommand(ctx context.Context, c Command) error {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = c.Dir, c.Stdout, c.Stderr
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devnull.Close()
	cmd.Stdin = devnull
	return cmd.Run()
}
