package agent

import (
	"context"
	"io"
	"maps"
	"os"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func TestClaudeTurnCountsToolCallsByName(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}},{"type":"text","text":"hi"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{}},{"type":"tool_use","name":"Edit","input":{}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"session_id":"sid-1","structured_output":` + prOpened + `,"total_cost_usd":0.5}`,
	}, "\n")
	exec := func(_ context.Context, c Command) error {
		io.WriteString(c.Stdout, stream)
		return nil
	}
	turn, err := Claude{Bin: "claude", Exec: exec, NewSessionID: func() string { return "sid-1" }}.Start(context.Background(), StartInput{Prompt: "x"}, noSession)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int{"Bash": 2, "Edit": 1}; !maps.Equal(turn.Usage.ToolCalls, want) {
		t.Errorf("tool calls = %v, want %v", turn.Usage.ToolCalls, want)
	}
}

func TestCodexTurnCountsCompletedToolItemsByTypeAndRecordsNoCost(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"th-1"}`,
		`{"type":"item.started","item":{"id":"1","type":"command_execution","command":"go test"}}`,
		`{"type":"item.completed","item":{"id":"1","type":"command_execution","command":"go test","exit_code":0}}`,
		`{"type":"item.completed","item":{"id":"2","type":"file_change","changes":[]}}`,
		`{"type":"item.completed","item":{"id":"3","type":"web_search","query":"x"}}`,
		`{"type":"item.completed","item":{"id":"4","type":"mcp_tool_call","server":"s","tool":"t"}}`,
		`{"type":"item.completed","item":{"id":"5","type":"agent_message","text":"done"}}`,
		`{"type":"item.completed","item":{"id":"6","type":"reasoning","text":"hmm"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}`,
	}, "\n") + "\n"
	exec := func(_ context.Context, c Command) error {
		io.WriteString(c.Stdout, stream)
		return os.WriteFile(argValue(c.Args, "-o"), []byte(prOpened), 0o600)
	}
	turn, err := Codex{Bin: "codex", Exec: exec}.Start(context.Background(), StartInput{Prompt: "x"}, noSession)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"command_execution": 1, "file_change": 1, "web_search": 1, "mcp_tool_call": 1}
	if !maps.Equal(turn.Usage.ToolCalls, want) {
		t.Errorf("tool calls = %v, want %v", turn.Usage.ToolCalls, want)
	}
	if turn.Usage.ClaudeCostUSD != 0 {
		t.Errorf("codex turn recorded a cost: %v", turn.Usage.ClaudeCostUSD)
	}
}

func noSession(task.Session) {}
