package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/store"
	"github.com/brymartinez/foreman/internal/task"
)

func TestLocalDocumentsWorkflowAndPlanRevision(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	triage := store.Triage{Dir: t.TempDir()}
	f.app.Triage = triage
	req := startReq(t)
	key := task.Key{Repo: f.gh.repo.Repo, Number: req.Issue}
	f.gh.issue.Labels = nil
	if _, err := f.app.Start(ctx, req); !errors.Is(err, ErrAwaitingTriage) {
		t.Fatalf("start without confirmed triage = %v", err)
	}
	if err := triage.Save(key, "Only retry 5xx responses."); err != nil {
		t.Fatal(err)
	}
	f.claude.turns = []agent.Turn{
		plannedTurn("planner"),
		{Outcome: agent.Outcome{Status: agent.PlanRevision, Summary: "Step 1 names a removed handler."}},
		plannedTurn("planner-2"),
		prTurn("implementer"),
	}
	key = started(t, f, req)
	got := run(t, f, key)
	if got.State != task.WaitingForCI || !got.PlanPosted() || got.Delegation.LocalPlan == nil || len(f.gh.comments) != 0 {
		t.Fatalf("state %s, saved plan %v, tracker comments %v", got.State, got.Delegation.LocalPlan, f.gh.comments)
	}
	if len(f.claude.prompts) != 4 {
		t.Fatalf("agent starting prompts = %d", len(f.claude.prompts))
	}
	for _, want := range []struct {
		turn int
		text string
	}{
		{0, "Only retry 5xx responses."}, {1, "## Implementation plan"}, {2, "Step 1 names a removed handler."},
	} {
		if !strings.Contains(f.claude.prompts[want.turn], want.text) {
			t.Errorf("turn %d did not receive %q", want.turn, want.text)
		}
	}
	path := filepath.Join(triage.Dir, "github", "testorg", "example", "825.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	amended := strings.ReplaceAll(string(raw), "Only retry 5xx responses.", "Retry 5xx once.")
	if err := os.WriteFile(path, []byte(amended), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.app.reviewRequirements(ctx, got); err == nil {
		t.Fatal("review accepted a plan for older triage")
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	f.gh.issue.Body = "Updated requirements."
	if _, _, err := f.app.reviewRequirements(ctx, got); err == nil {
		t.Fatal("review accepted a plan for an older issue description")
	}
}

func TestExistingRunKeepsTrackerDocumentsAfterUpgrade(t *testing.T) {
	f := newFixture()
	req := startReq(t)
	key := started(t, f, req)
	f.app.Triage = store.Triage{Dir: t.TempDir()}
	f.claude.turns = []agent.Turn{plannedTurn("planner"), prTurn("implementer")}
	got := run(t, f, key)
	if got.State != task.WaitingForCI || got.Delegation.LocalDocuments || len(f.gh.comments) != 2 {
		t.Fatalf("state %s, local %t, comments %d", got.State, got.Delegation.LocalDocuments, len(f.gh.comments))
	}
}
