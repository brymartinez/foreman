package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

func TestUnchangedPlanRevisionStopsBeforeAnotherCheck(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{plannedTurn("sid"), plannedTurn("sid")}
	f.claude.scopeReviews = []agent.ReviewOutcome{{Findings: []task.Finding{{Summary: "Missing date rule", Detail: "Requirements must cover the date filter."}}}}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	rec := f.store.recs[key]
	draft := rec.Delegation.PlanDraft
	if rec.State != task.NeedsDecision || draft == nil || !draft.Stopped || draft.Revisions != 1 || len(f.claude.scopeReviewed) != 1 || len(f.claude.resumed) != 1 || len(f.gh.comments) != 0 {
		t.Fatalf("state %s draft %+v checks %d revisions %d", rec.State, draft, len(f.claude.scopeReviewed), len(f.claude.resumed))
	}
}

type failedPlanCheck struct{ AgentAdapter }

func (a failedPlanCheck) Review(ctx context.Context, in agent.StartInput) (agent.ReviewOutcome, error) {
	if in.Purpose == "plan_check" {
		return agent.ReviewOutcome{}, errors.New("checker unavailable")
	}
	return a.AgentAdapter.Review(ctx, in)
}

func TestRestartAfterFailedCheckUsesSavedDocuments(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.app.Agents[task.Claude] = failedPlanCheck{f.claude}
	f.claude.turns = []agent.Turn{plannedTurn("sid")}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if d := f.store.recs[key].Delegation.PlanDraft; d == nil || len(d.Documents) != 2 {
		t.Fatalf("draft %+v", d)
	}
	f.app.Agents[task.Claude] = f.claude
	started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{prTurn("next")}
	if err := f.run(context.Background(), key, 4243, io.Discard); err != nil {
		t.Fatal(err)
	}
	rec := f.store.recs[key]
	if !rec.PlanPosted() || rec.Delegation.PlanDraft != nil || f.claude.starts != 2 || len(f.claude.scopeReviewed) != 1 {
		t.Fatalf("state %s starts %d checks %d", rec.State, f.claude.starts, len(f.claude.scopeReviewed))
	}
}

func TestLostPlanningSessionStartsOnlyFromDraftAndFindings(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.gh.issueComments = []IssueComment{{ID: 1, URL: "https://example.test/triage", Body: "## Triage decisions\nUse activity."}}
	revised := plannedTurn("new")
	revised.Outcome.Documents[task.PRD] = "R1. Use activity."
	f.claude.turns = []agent.Turn{plannedTurn("old"), revised, prTurn("implementation")}
	f.claude.resumeErr = agent.ErrNoSession
	f.claude.scopeReviews = []agent.ReviewOutcome{{Findings: []task.Finding{{Summary: "Use activity", Detail: "Fix R1."}}}}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(f.claude.prompts) != 3 {
		t.Fatalf("starts %d prompts %d", f.claude.starts, len(f.claude.prompts))
	}
	p := f.claude.prompts[1]
	if !strings.Contains(p, "## Saved draft") || !strings.Contains(p, "Fix R1.") || !strings.Contains(p, "## Confirmed scope") || strings.Contains(p, "## Issue") {
		t.Fatalf("correction prompt %s", p)
	}
}
