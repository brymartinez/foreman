package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/store"
	"github.com/brymartinez/foreman/internal/task"
)

// fakeWorkspace records every call the app makes to the shared store.
type fakeWorkspace struct {
	pull       WorkspacePull
	pullErr    error
	pulls      int
	published  []task.Key
	publishErr error
	blocked    map[task.Key]string
	acks       []WorkspaceAck
	knowledge  []string
}

func (w *fakeWorkspace) Pull(context.Context) (WorkspacePull, error) {
	w.pulls++
	return w.pull, w.pullErr
}
func (w *fakeWorkspace) Publish(_ context.Context, key task.Key) error {
	w.published = append(w.published, key)
	return w.publishErr
}
func (w *fakeWorkspace) Blocked(_ context.Context, key task.Key, reason string) error {
	if w.blocked == nil {
		w.blocked = map[task.Key]string{}
	}
	w.blocked[key] = reason
	return nil
}
func (w *fakeWorkspace) PublishKnowledge(_ context.Context, repo string) error {
	w.knowledge = append(w.knowledge, repo)
	return nil
}
func (w *fakeWorkspace) Ack(_ context.Context, a WorkspaceAck) error {
	w.acks = append(w.acks, a)
	return nil
}

func TestEveryTaskUpdatePublishesTheSavedRecord(t *testing.T) {
	f := newFixture()
	ws := &fakeWorkspace{}
	f.app.Workspace = ws
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(ws.published) == 0 || ws.published[len(ws.published)-1] != rec.Key {
		t.Fatalf("published = %v, want the started task %v", ws.published, rec.Key)
	}
}

func TestAFailedPublishNeverChangesTheTask(t *testing.T) {
	f := newFixture()
	f.app.Workspace = &fakeWorkspace{publishErr: errors.New("store is down")}
	var logged []string
	f.app.WorkspaceLog = func(format string, args ...any) { logged = append(logged, format) }
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil || rec.State == task.Failed {
		t.Fatalf("start = %+v, %v", rec, err)
	}
	if got := f.store.recs[rec.Key]; got == nil || got.State != rec.State {
		t.Fatal("the task changed")
	}
	if len(logged) == 0 {
		t.Fatal("the failure was not logged")
	}
}

func TestSQLiteModeMakesNoWorkspaceCall(t *testing.T) {
	f := newFixture() // Workspace is nil
	if _, err := f.app.Start(context.Background(), startReq(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.InboxView(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if warns := f.app.refreshWorkspace(context.Background()); warns != nil {
		t.Fatalf("warnings = %v", warns)
	}
}

func TestRefreshAppliesAnAnswerAndTheSameSessionResumes(t *testing.T) {
	f, key := blockedTask(t)
	ws := &fakeWorkspace{pull: WorkspacePull{Answers: []WorkspaceAnswer{{Key: key, Text: "B", Token: "tok-1"}}}}
	f.app.Workspace = ws
	f.runners.spawned = nil
	if warns := f.app.refreshWorkspace(context.Background()); len(warns) != 0 {
		t.Fatal(warns)
	}
	got := f.store.recs[key]
	if got.Decisions[0].Answer != "B. 4xx + 5xx" || got.State != task.Planning {
		t.Fatalf("decision %+v state %s", got.Decisions[0], got.State)
	}
	if len(f.runners.spawned) != 1 {
		t.Fatal("the runner was not relaunched")
	}
	if len(ws.acks) != 1 || ws.acks[0].Kind != AckAnswer || ws.acks[0].Token != "tok-1" || ws.acks[0].Key != key {
		t.Fatalf("acks = %+v", ws.acks)
	}
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("implementation")}
	if err := f.run(context.Background(), key, 4243, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(f.claude.resumed) != 1 || !strings.HasPrefix(f.claude.resumed[0], "sid-1: ") {
		t.Fatalf("the answer must resume the same session: %q", f.claude.resumed)
	}
}

func TestRefreshAcksAnAnswerForAQuestionThatWasAlreadyAnswered(t *testing.T) {
	f, key := blockedTask(t)
	if _, err := f.app.Respond(context.Background(), key, "A"); err != nil {
		t.Fatal(err)
	}
	ws := &fakeWorkspace{pull: WorkspacePull{Answers: []WorkspaceAnswer{{Key: key, Text: "B", Token: "tok-2"}}}}
	f.app.Workspace = ws
	f.app.refreshWorkspace(context.Background())
	if got := f.store.recs[key].Decisions[0].Answer; got != "A. 5xx only" {
		t.Fatalf("an answered question changed: %q", got)
	}
	if len(ws.acks) != 1 {
		t.Fatalf("a stale answer must be cleared: %+v", ws.acks)
	}
}

func queuedFixture(t *testing.T) (fixture, task.Key, store.Triage) {
	f, key, _, triage := localFixture(t)
	unbound := key
	unbound.Repo = ""
	return f, unbound, triage
}

func TestQueuedLocalTaskStartsWithoutAutoModeAndClearsItsQueue(t *testing.T) {
	f, unbound, triage := queuedFixture(t)
	bound := unbound
	bound.Repo = "testorg/example"
	if err := triage.Save(unbound, "Keep the cache API. Implement in https://github.com/testorg/example."); err != nil {
		t.Fatal(err)
	}
	ws := &fakeWorkspace{pull: WorkspacePull{Queued: []WorkspaceQueued{{Key: unbound, Token: "ref-1"}}}}
	f.app.Workspace = ws
	if warns := f.app.refreshWorkspace(context.Background()); len(warns) != 0 {
		t.Fatal(warns)
	}
	rec := f.store.recs[bound]
	if rec == nil || rec.Delegation == nil || rec.Delegation.Branch == "" {
		t.Fatalf("the queued task did not start: %+v", f.store.recs)
	}
	if len(f.runners.spawned) != 1 {
		t.Fatal("no runner was spawned")
	}
	if len(ws.acks) != 1 || ws.acks[0].Kind != AckQueue {
		t.Fatalf("acks = %+v", ws.acks)
	}
	if len(ws.blocked) != 0 {
		t.Fatalf("blocked = %v", ws.blocked)
	}
}

func TestQueuedTaskWithoutTriageStaysQueuedAndShowsTheReason(t *testing.T) {
	f, unbound, _ := queuedFixture(t)
	ws := &fakeWorkspace{pull: WorkspacePull{Queued: []WorkspaceQueued{{Key: unbound}}}}
	f.app.Workspace = ws
	f.app.refreshWorkspace(context.Background())
	if len(ws.acks) != 0 {
		t.Fatalf("Queue must stay ticked: %+v", ws.acks)
	}
	if r := ws.blocked[unbound]; !strings.Contains(r, "triage") {
		t.Fatalf("reason = %q", r)
	}
	if len(f.runners.spawned) != 0 {
		t.Fatal("a blocked task started")
	}
}

func TestQueuedTaskWaitsWhenEveryAutoSlotIsInUse(t *testing.T) {
	f, unbound, triage := queuedFixture(t)
	triage.Save(unbound, "Keep the cache API. Implement in https://github.com/testorg/example.")
	busy := task.Key{Repo: "testorg/example", Number: 7}
	f.store.recs[busy] = &task.Record{Key: busy, State: task.Implementing, Delegation: &task.Delegation{}}
	f.app.MaxConcurrent = 1
	ws := &fakeWorkspace{pull: WorkspacePull{Queued: []WorkspaceQueued{{Key: unbound}}}}
	f.app.Workspace = ws
	f.app.refreshWorkspace(context.Background())
	if r := ws.blocked[unbound]; !strings.Contains(r, "auto.max_concurrent") {
		t.Fatalf("reason = %q", r)
	}
	if len(ws.acks) != 0 || len(f.runners.spawned) != 0 {
		t.Fatal("a task started past the limit")
	}
}

func TestQueuedTaskThatAlreadyHasARunIsAckedWithoutStartingAgain(t *testing.T) {
	f := newFixture()
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	f.runners.spawned = nil
	ws := &fakeWorkspace{pull: WorkspacePull{Queued: []WorkspaceQueued{{Key: rec.Key}}}}
	f.app.Workspace = ws
	f.app.refreshWorkspace(context.Background())
	if len(ws.acks) != 1 || len(f.runners.spawned) != 0 {
		t.Fatalf("acks %+v, spawned %d", ws.acks, len(f.runners.spawned))
	}
}

func TestPullFailureBecomesAWarningAndTheInboxStillLoads(t *testing.T) {
	f := newFixture()
	f.app.Workspace = &fakeWorkspace{pullErr: errors.New("store is down")}
	report, err := f.app.InboxView(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range report.Warnings {
		if strings.Contains(w.Error(), "store is down") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v", report.Warnings)
	}
}

func TestLearningPublishesTheRepositoryKnowledgeAfterApply(t *testing.T) {
	f, _, _ := learnFixture(t)
	ws := &fakeWorkspace{}
	f.app.Workspace = ws
	if err := f.app.Learn(context.Background(), learnKey, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(ws.knowledge) != 1 || ws.knowledge[0] != string(learnKey.Repo) {
		t.Fatalf("published knowledge = %v", ws.knowledge)
	}
}

func TestIngestPublishesTheRepositoryKnowledgeAfterReplace(t *testing.T) {
	src := machine(t, kEval("1", 0, "Wrap errors with the operation name.", knowledge.Maintainer))
	dst := machine(t)
	ws := &fakeWorkspace{}
	a := knowledgeApp(dst, &stubMerger{})
	a.Workspace = ws
	res, err := a.IngestKnowledge(context.Background(), IngestInput{Data: exportOf(t, src)})
	if err != nil || !res.Saved {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if len(ws.knowledge) != 1 || ws.knowledge[0] != kRepo {
		t.Fatalf("published knowledge = %v", ws.knowledge)
	}
	ws.knowledge = nil
	a.IngestKnowledge(context.Background(), IngestInput{Data: exportOf(t, src), DryRun: true})
	if len(ws.knowledge) != 0 {
		t.Fatal("a dry run must not publish")
	}
}
