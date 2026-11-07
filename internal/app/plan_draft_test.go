package app

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
)

func TestPlannerUsesConfirmedScopeWithoutHelperCheck(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.gh.issueComments = []IssueComment{{ID: 1, URL: "https://example.test/triage", Body: "## Triage decisions\nUse activity."}}
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2")}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !f.store.recs[key].PlanPosted() || len(f.claude.scopeReviewed) != 0 {
		t.Fatalf("plan posted %t helper checks %d", f.store.recs[key].PlanPosted(), len(f.claude.scopeReviewed))
	}
	if !strings.Contains(f.claude.prompts[0], "Use activity") {
		t.Fatalf("planner did not receive confirmed scope: %s", f.claude.prompts[0])
	}
	if f.claude.starts != 2 {
		t.Fatalf("planner stages %v", f.claude.stages)
	}
}
