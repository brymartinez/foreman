package agent

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

const branchJSON = `{"status":"branch_proposed","summary":"repo workstream","branchName":"bryan/db-kub-189-project-comms","uncertain":false}`

func TestClaudeBranchProposalUsesToolFreeScratchSession(t *testing.T) {
	var got Command
	exec := func(_ context.Context, c Command) error {
		got = c
		fmt.Fprintf(c.Stdout, `{"type":"result","subtype":"success","structured_output":%s}`, branchJSON)
		return nil
	}
	p := BranchProposers{Claude: Claude{Bin: "claude", Model: "opus", PlanModel: "sonnet", Exec: exec}}
	out, err := p.Propose(context.Background(), BranchInput{Agent: task.Claude, Prompt: "name the branch"})
	if err != nil || out.BranchName != "bryan/db-kub-189-project-comms" {
		t.Fatalf("outcome %+v error %v", out, err)
	}
	if argValue(got.Args, "--model") != "sonnet" || argValue(got.Args, "--tools") != "" || !slices.Contains(got.Args, "--tools") || !slices.Contains(got.Args, "--no-session-persistence") || argValue(got.Args, "--json-schema") != string(BranchSchema) {
		t.Fatalf("argv = %q", got.Args)
	}
	if _, err := os.Stat(got.Dir); err == nil {
		t.Fatalf("scratch directory remains: %s", got.Dir)
	}
}

func TestCodexBranchProposalUsesReadOnlyScratchSession(t *testing.T) {
	var got Command
	exec := func(_ context.Context, c Command) error {
		got = c
		fmt.Fprintln(c.Stdout, `{"type":"thread.started","thread_id":"th-1"}`)
		return os.WriteFile(argValue(c.Args, "-o"), []byte(branchJSON), 0o600)
	}
	p := BranchProposers{Codex: Codex{Bin: "codex", Model: "big", PlanModel: "small", ReasoningEffort: "high", PlanReasoningEffort: "low", Exec: exec}}
	out, err := p.Propose(context.Background(), BranchInput{Agent: task.Codex, Prompt: "name the branch"})
	if err != nil || out.BranchName != "bryan/db-kub-189-project-comms" {
		t.Fatalf("outcome %+v error %v", out, err)
	}
	if argValue(got.Args, "-m") != "small" || !slices.Contains(got.Args, `model_reasoning_effort="low"`) || argValue(got.Args, "-s") != "read-only" || !slices.Contains(got.Args, "--ephemeral") || argValue(got.Args, "--output-schema") == "" {
		t.Fatalf("argv = %q", got.Args)
	}
	if _, err := os.Stat(got.Dir); err == nil {
		t.Fatalf("scratch directory remains: %s", got.Dir)
	}
}
