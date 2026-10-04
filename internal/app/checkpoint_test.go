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

func checkpointTurn(id, next string) agent.Turn {
	return agent.Turn{Session: task.Session{Agent: task.Claude, ID: id}, Outcome: agent.Outcome{
		Status: agent.Checkpoint, Summary: "Committed the first part", NextObjective: next,
		Validation: []task.Check{{Command: "go test ./...", Passed: true}},
	}, Usage: task.Usage{ClaudeCostUSD: 1.25, ClaudeTurns: 1}}
}

func postedPlan(rec *task.Record) {
	for _, doc := range task.PlanDocs {
		rec.SetPlanComment(task.PlanComment{Doc: doc, URL: "https://example.test/plan/" + string(doc)})
	}
}

func TestCheckpointStartsFreshImplementationSessionWithOnlyDurableHandoff(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	postedPlan(f.store.recs[key])
	f.wt.checkpointHeads = []string{"commit1"}
	f.claude.turns = []agent.Turn{checkpointTurn("first-session", "Add the dashboard log pager"), prTurn("second-session")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if f.claude.starts != 2 || len(f.claude.resumed) != 0 {
		t.Fatalf("starts %d resumes %d, want two fresh sessions", f.claude.starts, len(f.claude.resumed))
	}
	if got.State != task.Reviewing || got.Delegation.Checkpoints != 1 || got.Delegation.CheckpointHead != "commit1" || got.Delegation.CheckpointNext != "" {
		t.Fatalf("checkpoint and PR state = %+v", got.Delegation)
	}
	if got.Usage.ClaudeCostUSD != 1.25 || got.Usage.ClaudeTurns != 1 {
		t.Fatalf("checkpoint usage = %+v", got.Usage)
	}
	p := f.claude.prompts[1]
	for _, want := range []string{"commit1", "Add the dashboard log pager", "https://example.test/plan/plan"} {
		if !strings.Contains(p, want) {
			t.Errorf("fresh prompt missing %q", want)
		}
	}
	for _, unwanted := range []string{"Retry failed requests.", "Committed the first part", "first-session"} {
		if strings.Contains(p, unwanted) {
			t.Errorf("fresh prompt carried %q", unwanted)
		}
	}
}

func TestCheckpointVerificationFailureStopsBeforeAnotherSession(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	postedPlan(f.store.recs[key])
	f.wt.checkpointErr = errors.New("checkpoint has uncommitted files")
	f.claude.turn = checkpointTurn("first-session", "Continue work")
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "uncommitted files") || f.claude.starts != 1 || got.Delegation.Checkpoints != 0 {
		t.Fatalf("state %s outcome %+v starts %d checkpoints %d", got.State, got.LastOutcome, f.claude.starts, got.Delegation.Checkpoints)
	}
}

func TestRetryPreservesCheckpointOnlyWhenBranchHeadMatches(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	rec := f.store.recs[key]
	rec.State = task.Failed
	rec.Delegation.CheckpointHead = "commit1"
	rec.Delegation.CheckpointNext = "Add the dashboard log pager"
	rec.Delegation.Checkpoints = 1
	f.wt.head = "commit1"
	got, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if got.Delegation.CheckpointNext != "Add the dashboard log pager" || got.Delegation.Checkpoints != 1 {
		t.Fatalf("matching head lost checkpoint: %+v", got.Delegation)
	}
	got.State = task.Failed
	f.wt.head = "later-commit"
	got, err = f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if got.Delegation.CheckpointNext != "" || got.Delegation.CheckpointHead != "later-commit" || got.Delegation.Checkpoints != 0 {
		t.Fatalf("changed head kept stale checkpoint: %+v", got.Delegation)
	}
}
