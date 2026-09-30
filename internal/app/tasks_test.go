package app

import (
	"context"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

// TestTasksSkipsTheGate proves the dashboard's one-second refresh costs no GitHub
// call: the same task Status advances is left alone by Tasks.
func TestTasksSkipsTheGate(t *testing.T) {
	f := newFixture()
	k := reviewed(t, f)
	setCI(f, task.CIStatus{State: task.CIPassing})

	recs, err := f.app.Tasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].State != task.Reviewing {
		t.Fatalf("Tasks = %+v; want one REVIEWING task", recs)
	}
	if got := f.store.recs[k]; got.CI != nil {
		t.Errorf("Tasks read CI: %+v", got.CI)
	}

	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[k]; got.State != task.ReadyForHuman {
		t.Fatalf("Status left the task %s; the gate should have advanced it", got.State)
	}
}

func TestTasksOrdersByAttentionAndDropsFinishedWork(t *testing.T) {
	delegated := func(n int, state task.State) *task.Record {
		r := seen(n, state, "open", t0)
		r.Delegation = &task.Delegation{Implementer: task.Session{Agent: task.Claude}}
		return r
	}
	undelegated := seen(4, task.Discovered, "open", t0)
	store := &fakeStore{recs: map[task.Key]*task.Record{
		key(1): delegated(1, task.Working),
		key(2): delegated(2, task.NeedsDecision),
		key(3): delegated(3, task.Done),
		key(4): undelegated,
	}}
	a := App{Store: store, Runners: &fakeRunners{alive: map[int]bool{}}, Now: func() time.Time { return now }}

	recs, err := a.Tasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []int
	for _, r := range recs {
		got = append(got, r.Number)
	}
	if len(got) != 2 || got[0] != 2 || got[1] != 1 {
		t.Fatalf("Tasks numbers = %v; want the waiting task first and no finished or undelegated work", got)
	}
}

func TestTasksReapsADeadRunner(t *testing.T) {
	rec := seen(1, task.Working, "open", t0)
	rec.Delegation = &task.Delegation{Implementer: task.Session{Agent: task.Claude}, RunnerPID: 999, RunnerLog: "/logs/1.log"}
	store := &fakeStore{recs: map[task.Key]*task.Record{key(1): rec}}
	a := App{Store: store, Runners: &fakeRunners{alive: map[int]bool{}}, Now: func() time.Time { return now }}

	recs, err := a.Tasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].State != task.Failed {
		t.Fatalf("Tasks = %+v; want the dead runner's task FAILED", recs)
	}
}
