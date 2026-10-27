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

func postedPlan(f fixture, rec *task.Record) {
	if f.gh.comments == nil {
		f.gh.comments = map[int64]IssueComment{}
	}
	texts := map[task.PlanDoc]string{}
	for _, doc := range task.PlanDocs {
		id := int64(700 + len(texts))
		text := "Posted " + string(doc) + "."
		url := "https://example.test/plan/" + string(doc)
		comment := IssueComment{ID: id, URL: url, Body: planCommentBody(doc, task.DescriptionFingerprint(f.gh.issue.Body), text)}
		f.gh.comments[id] = comment
		f.gh.issueComments = append(f.gh.issueComments, comment)
		rec.SetPlanComment(task.PlanComment{Doc: doc, URL: url, CommentID: id})
		texts[doc] = text
	}
	scope := confirmedScope{text: "Source: " + f.gh.issue.URL + "\n" + f.gh.issue.Body}
	scope.digest = task.Digest(scope.text)
	rec.Delegation.ScopeDigest = scope.digest
	rec.Delegation.PlanScopeDigest = scope.digest
	rec.Delegation.PlanDigest = planDigest(texts)
}

func TestCheckpointStartsFreshImplementationSessionWithOnlyDurableHandoff(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	postedPlan(f, f.store.recs[key])
	f.wt.checkpointHeads = []string{"commit1"}
	f.claude.turns = []agent.Turn{checkpointTurn("first-session", "Add the dashboard log pager"), prTurn("second-session")}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if f.claude.starts != 2 || len(f.claude.resumed) != 0 {
		t.Fatalf("starts %d resumes %d, want two fresh sessions", f.claude.starts, len(f.claude.resumed))
	}
	if got.State != task.WaitingForCI || got.Delegation.Checkpoints != 1 || got.Delegation.CheckpointHead != "commit1" || got.Delegation.CheckpointNext != "" {
		t.Fatalf("checkpoint and PR state = %+v", got.Delegation)
	}
	if got.Usage.ClaudeCostUSD != 1.25 || got.Usage.ClaudeTurns != 1 {
		t.Fatalf("checkpoint usage = %+v", got.Usage)
	}
	p := f.claude.prompts[1]
	for _, want := range []string{"commit1", "Add the dashboard log pager", "Posted plan."} {
		if !strings.Contains(p, want) {
			t.Errorf("fresh prompt missing %q", want)
		}
	}
	for _, unwanted := range []string{"Committed the first part", "first-session"} {
		if strings.Contains(p, unwanted) {
			t.Errorf("fresh prompt carried %q", unwanted)
		}
	}
}

func TestFailedCheckpointValidationResumesSameSessionForRepair(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	postedPlan(f, f.store.recs[key])
	f.wt.checkpointHeads = []string{"commit1"}
	failed := checkpointTurn("first-session", "Continue work")
	failed.Outcome.Validation = append(failed.Outcome.Validation, task.Check{Command: "pnpm lint", Passed: false})
	f.claude.turns = []agent.Turn{failed, checkpointTurn("first-session", "Continue work"), prTurn("second-session")}
	f.claude.turnErrs = []error{&agent.CheckpointValidationError{Failed: []string{"pnpm lint"}}, nil, nil}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || got.Delegation.Checkpoints != 1 || f.claude.starts != 2 || len(f.claude.resumed) != 1 {
		t.Fatalf("state %s checkpoints %d starts %d resumes %v", got.State, got.Delegation.Checkpoints, f.claude.starts, f.claude.resumed)
	}
	if !strings.Contains(f.claude.resumed[0], "first-session") || !strings.Contains(f.claude.resumed[0], "pnpm lint") {
		t.Fatalf("repair prompt = %q", f.claude.resumed[0])
	}
	if got.Usage.ClaudeTurns != 2 || got.Usage.ClaudeCostUSD != 2.5 {
		t.Fatalf("repair usage lost: %+v", got.Usage)
	}
}

func TestRepeatedFailedCheckpointValidationStopsAfterRepairLimit(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	postedPlan(f, f.store.recs[key])
	failed := checkpointTurn("first-session", "Continue work")
	failed.Outcome.Validation = []task.Check{{Command: "pnpm lint", Passed: false}}
	f.claude.turns = []agent.Turn{failed, failed, failed}
	f.claude.turnErrs = []error{
		&agent.CheckpointValidationError{Failed: []string{"pnpm lint"}},
		&agent.CheckpointValidationError{Failed: []string{"pnpm lint"}},
		&agent.CheckpointValidationError{Failed: []string{"pnpm lint"}},
	}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || f.claude.starts != 1 || len(f.claude.resumed) != 2 || got.Usage.ClaudeTurns != 3 {
		t.Fatalf("state %s starts %d resumes %d usage %+v", got.State, f.claude.starts, len(f.claude.resumed), got.Usage)
	}
}

func TestCheckpointVerificationFailureStopsBeforeAnotherSession(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	postedPlan(f, f.store.recs[key])
	f.wt.checkpointErr = errors.New("checkpoint has uncommitted files")
	f.claude.turn = checkpointTurn("first-session", "Continue work")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
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

func TestCheckpointRepairStopsBeforeResumingWhenGHSwitchesAccounts(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	postedPlan(f, f.store.recs[key])
	failed := checkpointTurn("first-session", "Continue work")
	failed.Outcome.Validation = []task.Check{{Command: "pnpm lint", Passed: false}}
	f.claude.turns = []agent.Turn{failed, checkpointTurn("first-session", "Continue work")}
	f.claude.turnErrs = []error{&agent.CheckpointValidationError{Failed: []string{"pnpm lint"}}, nil}
	f.claude.during = func() { f.accts.active = Account{Login: "octo-home", ID: 7} }
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || len(f.claude.resumed) != 0 || !strings.Contains(got.LastOutcome.Summary, "gh auth switch") {
		t.Fatalf("state %s resumes %v outcome %+v", got.State, f.claude.resumed, got.LastOutcome)
	}
}
