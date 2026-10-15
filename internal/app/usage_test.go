package app

import (
	"maps"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func TestAddUsageAccumulatesToolCallsAcrossTurns(t *testing.T) {
	var total task.Usage
	addUsage(&total, task.Usage{ToolCalls: map[string]int{"Bash": 2}})
	addUsage(&total, task.Usage{ToolCalls: map[string]int{"Bash": 1, "Edit": 3}})
	addUsage(&total, task.Usage{})
	if want := map[string]int{"Bash": 3, "Edit": 3}; !maps.Equal(total.ToolCalls, want) {
		t.Errorf("tool calls = %v, want %v", total.ToolCalls, want)
	}
}
