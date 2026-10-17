package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func TestLaunchWithRemovedTaskStopsBeforeSpawning(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: "testorg/example", Number: 825}
	if _, err := f.app.launch(context.Background(), key, "test-launch"); !errors.Is(err, errTaskRemoved) {
		t.Fatalf("launch error = %v, want task removed", err)
	}
	if len(f.runners.spawned) != 0 || f.store.recs[key] != nil {
		t.Fatalf("spawned = %v, record = %+v", f.runners.spawned, f.store.recs[key])
	}
}

func TestLaunchWithTaskRemovedDuringWorktreeRestoreStopsBeforeSpawning(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: "testorg/example", Number: 825}
	f.store.recs[key] = &task.Record{Key: key, State: task.Working, Delegation: &task.Delegation{
		Branch: "fix/825", Worktree: "/wt/825", WorktreeRemovedAt: now,
	}}
	f.wt.onEnsure = func() { delete(f.store.recs, key) }
	if _, err := f.app.launch(context.Background(), key, "test-launch"); !errors.Is(err, errTaskRemoved) {
		t.Fatalf("launch error = %v, want task removed", err)
	}
	if len(f.runners.spawned) != 0 || f.store.recs[key] != nil {
		t.Fatalf("spawned = %v, record = %+v", f.runners.spawned, f.store.recs[key])
	}
}

func TestLaunchWithTaskRemovedDuringSpawnStopsRunner(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: "testorg/example", Number: 825}
	f.store.recs[key] = &task.Record{Key: key, State: task.Working, Delegation: &task.Delegation{}}
	f.runners.onSpawn = func() {
		f.runners.alive[4242] = true
		delete(f.store.recs, key)
	}
	if _, err := f.app.launch(context.Background(), key, "test-launch"); !errors.Is(err, errTaskRemoved) {
		t.Fatalf("launch error = %v, want task removed", err)
	}
	if len(f.runners.stopped) != 1 || f.runners.stopped[0] != 4242 || f.runners.alive[4242] || f.store.recs[key] != nil {
		t.Fatalf("stopped = %v, alive = %v, record = %+v", f.runners.stopped, f.runners.alive[4242], f.store.recs[key])
	}
}

func TestLaunchWithTaskRemovedDuringSpawnReportsFailedStop(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeRunners)
		want  string
	}{
		{"stop error", func(r *fakeRunners) { r.stopErr = errors.New("signal denied") }, "signal denied"},
		{"runner still running", func(r *fakeRunners) { r.stuck = true }, "still running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()
			key := task.Key{Repo: "testorg/example", Number: 825}
			f.store.recs[key] = &task.Record{Key: key, State: task.Working, Delegation: &task.Delegation{}}
			f.runners.onSpawn = func() {
				f.runners.alive[4242] = true
				delete(f.store.recs, key)
			}
			tc.setup(f.runners)
			_, err := f.app.launch(context.Background(), key, "test-launch")
			if err == nil || !strings.Contains(err.Error(), tc.want) || errors.Is(err, errTaskRemoved) {
				t.Fatalf("launch error = %v, want failed stop containing %q", err, tc.want)
			}
			if len(f.runners.stopped) != 1 || !f.runners.alive[4242] || f.store.recs[key] != nil {
				t.Fatalf("stopped = %v, alive = %v, record = %+v", f.runners.stopped, f.runners.alive[4242], f.store.recs[key])
			}
		})
	}
}
