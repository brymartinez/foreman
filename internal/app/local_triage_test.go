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

func TestLinearLocalTriageBeforeRepositorySelection(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	key := task.Key{Source: task.Linear, ExternalID: "11111111-1111-4111-8111-111111111111"}
	triage := store.Triage{Dir: t.TempDir()}
	f.app.Triage = triage
	tracker := &lifecycleTracker{issue: Issue{Key: key, Identifier: "WEB-42", Title: "Fix cache", State: "open"}}
	f.app.Linear = tracker
	if err := triage.Save(key, "Implement in https://github.com/testorg/example."); err != nil {
		t.Fatal(err)
	}
	proposal, err := f.app.ProposeRepository(ctx, key)
	if err != nil || proposal.Repo != f.gh.repo.Repo {
		t.Fatalf("repository = %+v, %v", proposal, err)
	}
	key.Repo = proposal.Repo
	started(t, f, StartRequest{Repo: key.Repo, Key: key, Agent: task.Claude, BranchName: f.gh.prs[912].HeadRefName})
	f.claude.turns = []agent.Turn{plannedTurn("planner"), prTurn("implementer")}
	got := run(t, f, key)
	if got.State != task.WaitingForCI || !got.PlanPosted() || len(tracker.posted) != 0 || len(f.gh.comments) != 0 {
		t.Fatalf("state %s (%+v), plan saved %t, Linear comments %d, GitHub comments %d", got.State, got.LastOutcome, got.PlanPosted(), len(tracker.posted), len(f.gh.comments))
	}
}

func TestStandaloneTrackerTriageStartsWithLocalPlanning(t *testing.T) {
	f := newFixture()
	triage := store.Triage{Dir: t.TempDir()}
	f.app.Triage = triage
	key := task.Key{Repo: f.gh.repo.Repo, Source: task.Linear, ExternalID: "11111111-1111-4111-8111-111111111111"}
	tracker := &lifecycleTracker{
		issue: Issue{Key: key, Identifier: "WEB-42", Title: "Fix cache", State: "open", Labels: []string{"triaged"}},
		comments: []TrackerComment{{
			ID: "triage", URL: "https://linear.app/comment/triage",
			Body: "## Triage decisions\nKeep the cache API. Implement in https://github.com/testorg/example.",
		}},
	}
	f.app.Linear = tracker
	unbound := key
	unbound.Repo = ""
	proposal, err := f.app.ProposeRepository(context.Background(), unbound)
	if err != nil || proposal.Repo != key.Repo {
		t.Fatalf("repository = %+v, %v", proposal, err)
	}
	req := StartRequest{Repo: key.Repo, Key: key, Agent: task.Claude, BranchName: f.gh.prs[912].HeadRefName}
	started(t, f, req)
	f.claude.turns = []agent.Turn{plannedTurn("planner"), prTurn("implementer")}
	got := run(t, f, key)
	if got.State != task.WaitingForCI || !got.Delegation.TrackerTriage || !got.PlanPosted() || len(tracker.posted) != 0 || len(f.gh.comments) != 0 {
		t.Fatalf("state %s, delegation %+v, Linear writes %d, GitHub writes %d", got.State, got.Delegation, len(tracker.posted), len(f.gh.comments))
	}
	if !strings.Contains(f.claude.prompts[0], "Keep the cache API.") {
		t.Fatal("planner didn't receive the standalone triage decisions")
	}
	if err := triage.Save(key, "A local record from a different interview."); err != nil {
		t.Fatal(err)
	}
	scope, err := f.app.readRunScope(context.Background(), got)
	if err != nil || !strings.Contains(scope.text, "Keep the cache API.") {
		t.Fatalf("task changed its recorded triage source: %+v, %v", scope, err)
	}
}
