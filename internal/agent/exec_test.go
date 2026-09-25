package agent

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestRunCommandCancelStopsTheToolsAnAgentStarted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	start := time.Now()
	// The background sleep holds stdout open, as a tool left running by the agent would.
	_ = RunCommand(ctx, Command{Name: "sh", Args: []string{"-c", "sleep 30 & sleep 30"}, Stdout: &out, Stderr: &out})
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("a cancelled agent kept the runner waiting %s", took)
	}
}
