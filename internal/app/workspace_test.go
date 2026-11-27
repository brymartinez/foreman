package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

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
	ackErr     error
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
	return w.ackErr
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

func TestQueuedTaskThatAlreadyHasARunIsPublishedEvenWhenTheAckFails(t *testing.T) {
	f := newFixture()
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	ws := &fakeWorkspace{pull: WorkspacePull{Queued: []WorkspaceQueued{{Key: rec.Key}}}, ackErr: errors.New("store is down")}
	f.app.Workspace = ws
	ws.published = nil
	f.app.refreshWorkspace(context.Background())
	if len(ws.published) != 1 || ws.published[0] != rec.Key {
		t.Fatalf("published = %v, want the delegated task", ws.published)
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

func TestQueuedGitHubIssueInAnExcludedRepositoryDoesNotStart(t *testing.T) {
	f, _, _ := queuedFixture(t)
	f.app.ExcludedRepositories = []task.Repo{"testorg/example"}
	key := task.Key{Repo: "testorg/example", Number: 9}
	ws := &fakeWorkspace{pull: WorkspacePull{Queued: []WorkspaceQueued{{Key: key}}}}
	f.app.Workspace = ws
	f.app.refreshWorkspace(context.Background())
	if len(f.runners.spawned) != 0 || len(ws.acks) != 0 {
		t.Fatalf("an excluded repository started: spawned %d, acks %+v", len(f.runners.spawned), ws.acks)
	}
	if r := ws.blocked[key]; !strings.Contains(r, "filter") {
		t.Fatalf("reason = %q", r)
	}
}

func TestQueuedTaskWhoseLaunchFailedKeepsItsQueueOnEveryRefresh(t *testing.T) {
	f := newFixture()
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	f.store.recs[rec.Key].State = task.Failed
	f.store.recs[rec.Key].LastOutcome = &task.Outcome{Status: "failed", Summary: "the runner could not start"}
	ws := &fakeWorkspace{pull: WorkspacePull{Queued: []WorkspaceQueued{{Key: rec.Key}}}}
	f.app.Workspace = ws
	for range 2 {
		f.app.refreshWorkspace(context.Background())
	}
	if len(ws.acks) != 0 {
		t.Fatalf("a failed launch cleared Queue: %+v", ws.acks)
	}
	if r := ws.blocked[rec.Key]; !strings.Contains(r, "could not start") {
		t.Fatalf("reason = %q", r)
	}
}

func TestAnAnswerForAnEarlierQuestionIsNeitherAppliedNorAcked(t *testing.T) {
	f, key := blockedTask(t)
	cur, _ := f.store.Get(context.Background(), key)
	ws := &fakeWorkspace{pull: WorkspacePull{Answers: []WorkspaceAnswer{{Key: key, Text: "B", Token: "tok", Question: QuestionKey(cur) + "-older"}}}}
	f.app.Workspace = ws
	f.runners.spawned = nil
	if warns := f.app.refreshWorkspace(context.Background()); len(warns) != 0 {
		t.Fatal(warns)
	}
	if got := f.store.recs[key].Decisions[0].Answer; got != "" {
		t.Fatalf("an answer for another question was recorded: %q", got)
	}
	if len(ws.acks) != 0 || len(f.runners.spawned) != 0 {
		t.Fatalf("acks %+v, spawned %d", ws.acks, len(f.runners.spawned))
	}
}

func TestAFailedQueuedStartWithAnEmptySummaryStillShowsAReasonAndKeepsQueue(t *testing.T) {
	f := newFixture()
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	f.store.recs[rec.Key].State = task.Failed
	f.store.recs[rec.Key].LastOutcome = &task.Outcome{Status: "failed"}
	ws := &fakeWorkspace{pull: WorkspacePull{Queued: []WorkspaceQueued{{Key: rec.Key}}}}
	f.app.Workspace = ws
	f.app.refreshWorkspace(context.Background())
	if len(ws.acks) != 0 || ws.blocked[rec.Key] == "" {
		t.Fatalf("acks %+v, reason %q", ws.acks, ws.blocked[rec.Key])
	}
}

func TestQueuedTaskWaitsWhileAnotherStartHoldsTheLastSlot(t *testing.T) {
	f, unbound, triage := queuedFixture(t)
	triage.Save(unbound, "Keep the cache API. Implement in https://github.com/testorg/example.")
	f.app.MaxConcurrent = 1
	other := task.Key{Repo: "testorg/example", Number: 9}
	defer trackStart(other, other.Repo, other.Number)()
	ws := &fakeWorkspace{pull: WorkspacePull{Queued: []WorkspaceQueued{{Key: unbound}}}}
	f.app.Workspace = ws
	f.app.refreshWorkspace(context.Background())
	if r := ws.blocked[unbound]; !strings.Contains(r, "auto.max_concurrent") {
		t.Fatalf("reason = %q", r)
	}
	if len(f.runners.spawned) != 0 {
		t.Fatal("a queued start took the slot another start held")
	}
}

func TestAnUpdateThatTakesALaunchClaimPublishesOnlyAfterTheSpawn(t *testing.T) {
	f, _, _ := queuedFixture(t)
	ws := &fakeWorkspace{}
	f.app.Workspace = ws
	key := task.Key{Repo: "testorg/example", Number: 3}
	f.store.recs[key] = &task.Record{Key: key, State: task.Planning, Delegation: &task.Delegation{}}
	_, err := f.app.update(context.Background(), key, func(cur *task.Record) (*task.Record, error) {
		_, err := f.app.claimLaunch(cur)
		return cur, err
	})
	if err != nil || len(ws.published) != 0 {
		t.Fatalf("err = %v, published = %v: the claim's update published before the runner spawned", err, ws.published)
	}
	if _, err := f.app.update(context.Background(), key, func(cur *task.Record) (*task.Record, error) {
		cur.Delegation.Launch = &task.Launch{Token: "t"}
		return cur, nil
	}); err != nil || len(ws.published) != 1 {
		t.Fatalf("err = %v, published = %v: the update after the spawn must publish", err, ws.published)
	}
}

func TestStoppingLaunchesWaitsForAQueuedStartToSpawnItsRunner(t *testing.T) {
	f, unbound, triage := queuedFixture(t)
	if err := triage.Save(unbound, "Keep the cache API. Implement in https://github.com/testorg/example."); err != nil {
		t.Fatal(err)
	}
	f.app.Launches = &Launches{}
	ws := &fakeWorkspace{pull: WorkspacePull{Queued: []WorkspaceQueued{{Key: unbound, Token: "ref-1"}}}}
	f.app.Workspace = ws
	entered, release := make(chan struct{}), make(chan struct{})
	f.runners.entered, f.runners.release = entered, release
	refreshed := make(chan []error, 1)
	go func() { refreshed <- f.app.refreshWorkspace(context.Background()) }()
	<-entered // the claim is saved and the runner is not yet started

	done := f.app.StopLaunches()
	select {
	case <-done:
		t.Fatal("a quitting process could exit between the claim and the spawn")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the launch never finished")
	}
	if warns := <-refreshed; len(warns) != 0 {
		t.Fatal(warns)
	}
}

func TestAStoppedProcessLaunchesNothingAndLeavesRequestsAlone(t *testing.T) {
	f, unbound, triage := queuedFixture(t)
	if err := triage.Save(unbound, "Keep the cache API. Implement in https://github.com/testorg/example."); err != nil {
		t.Fatal(err)
	}
	f.app.Launches = &Launches{}
	<-f.app.StopLaunches()
	ws := &fakeWorkspace{pull: WorkspacePull{Queued: []WorkspaceQueued{{Key: unbound}}}}
	f.app.Workspace = ws
	f.app.refreshWorkspace(context.Background())
	bound := unbound
	bound.Repo = "testorg/example"
	if _, err := f.app.Start(context.Background(), StartRequest{Key: bound, Repo: bound.Repo, Agent: task.Claude, BranchName: "feat/x"}); !errors.Is(err, errQuitting) {
		t.Fatalf("start = %v, want a refusal", err)
	}
	if len(f.runners.spawned) != 0 || len(ws.acks) != 0 || len(ws.blocked) != 0 {
		t.Fatalf("spawned %d, acks %+v, blocked %v", len(f.runners.spawned), ws.acks, ws.blocked)
	}
	if rec := f.store.recs[bound]; rec != nil && rec.Delegation != nil {
		t.Fatalf("a refused start saved a claim: %+v", rec.Delegation)
	}
}

// routeProbe reads the slots while a queued start is still resolving.
type routeProbe struct {
	TaskStore
	probe func()
}

func (p *routeProbe) Get(ctx context.Context, key task.Key) (*task.Record, error) {
	if probe := p.probe; probe != nil {
		p.probe = nil
		probe()
	}
	return p.TaskStore.Get(ctx, key)
}

func TestAQueuedStartHoldsItsSlotWhileItResolves(t *testing.T) {
	f, unbound, triage := queuedFixture(t)
	if err := triage.Save(unbound, "Keep the cache API. Implement in https://github.com/testorg/example."); err != nil {
		t.Fatal(err)
	}
	f.app.MaxConcurrent = 1
	var during error
	probed := false
	probe := &routeProbe{TaskStore: f.app.Store}
	f.app.Store = probe
	dashboard := f.app
	probe.probe = func() {
		probed = true
		release, err := dashboard.ReserveSlot(context.Background(), task.Key{Repo: "testorg/example", Number: 9})
		if err == nil {
			release()
		}
		during = err
	}
	f.app.Workspace = &fakeWorkspace{pull: WorkspacePull{Queued: []WorkspaceQueued{{Key: unbound}}}}
	if warns := f.app.refreshWorkspace(context.Background()); len(warns) != 0 {
		t.Fatal(warns)
	}
	if !probed || !errors.Is(during, ErrNoSlot) {
		t.Fatalf("probed %v: auto mode got a slot the queued start holds: %v", probed, during)
	}
	if len(f.runners.spawned) != 1 {
		t.Fatalf("spawned = %d", len(f.runners.spawned))
	}
}

func TestAReservedSlotCountsOnceHoweverItsKeyIsWritten(t *testing.T) {
	f, unbound, _ := queuedFixture(t)
	f.app.MaxConcurrent = 2
	release, err := f.app.ReserveSlot(context.Background(), unbound)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	bound := unbound
	bound.Repo = "testorg/example"
	defer trackStart(bound, bound.Repo, 0)() // the start that follows the reservation
	second, err := f.app.ReserveSlot(context.Background(), task.Key{Repo: "testorg/example", Number: 9})
	if err != nil {
		t.Fatalf("one task took two slots: %v", err)
	}
	defer second()
	if _, err := f.app.ReserveSlot(context.Background(), task.Key{Repo: "testorg/example", Number: 10}); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("third reservation = %v", err)
	}
}

// signalStore refuses a cancelled context, as the SQLite store does, and cancels the
// caller as soon as a launch claim is saved.
type signalStore struct {
	TaskStore
	signal context.CancelFunc
}

func (s *signalStore) Get(ctx context.Context, key task.Key) (*task.Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.TaskStore.Get(ctx, key)
}

func (s *signalStore) Update(ctx context.Context, key task.Key, fn func(*task.Record) (*task.Record, error)) (*task.Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rec, err := s.TaskStore.Update(ctx, key, fn)
	if err == nil && rec != nil && rec.Delegation != nil && rec.Delegation.Launch != nil && rec.Delegation.Launch.PID != 0 {
		s.signal()
	}
	return rec, err
}

func TestASignalAfterTheClaimStillSettlesTheLaunch(t *testing.T) {
	for name, spawnErr := range map[string]error{"spawned": nil, "spawn failed": errors.New("no such binary")} {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			f.runners.spawnErr = spawnErr
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.app.Store = &signalStore{TaskStore: f.app.Store, signal: cancel}
			rec, err := f.app.Start(ctx, startReq(t))
			if err != nil {
				t.Fatal(err)
			}
			if ctx.Err() == nil {
				t.Fatal("the signal never arrived")
			}
			if len(f.runners.spawned) != 1 {
				t.Fatalf("spawned = %d", len(f.runners.spawned))
			}
			saved := f.store.recs[rec.Key]
			if spawnErr == nil && saved.Delegation.RunnerPID == 0 {
				t.Fatalf("an active task has no runner: %s %+v", saved.State, saved.Delegation.Launch)
			}
			if spawnErr != nil && (saved.State != task.Failed || saved.Delegation.Launch != nil) {
				t.Fatalf("the failed launch was not recorded: %s %+v", saved.State, saved.Delegation.Launch)
			}
		})
	}
}
