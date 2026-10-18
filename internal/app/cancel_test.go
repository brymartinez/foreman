package app

import (
	"context"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

// running puts a started task under a live runner with pid 4242.
func runningTask(t *testing.T, f fixture) *task.Record {
	key := started(t, f, startReq(t))
	f.store.recs[key].Delegation.RunnerPID = 4242
	f.runners.alive = map[int]bool{4242: true}
	return f.store.recs[key]
}

func TestCancelStopsTheRunnerWhichRecordsTheStop(t *testing.T) {
	f := newFixture()
	rec := runningTask(t, f)
	f.runners.onStop = func(pid int) {
		r := f.store.recs[rec.Key]
		r.State, r.LastOutcome, r.Delegation.RunnerPID = task.Failed, &task.Outcome{Status: Cancelled, Summary: "run cancelled during implementation (terminated signal received)"}, 0
	}
	got, err := f.app.Cancel(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.runners.stopped) != 1 || f.runners.stopped[0] != 4242 {
		t.Errorf("stopped = %v", f.runners.stopped)
	}
	if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "during implementation") {
		t.Errorf("the runner's record was replaced: %s %+v", got.State, got.LastOutcome)
	}
}

func TestCancelRecordsARunnerThatExitedSilently(t *testing.T) {
	f := newFixture()
	rec := runningTask(t, f)
	got, err := f.app.Cancel(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != task.Failed || got.LastOutcome.Status != Cancelled || got.Delegation.RunnerPID != 0 {
		t.Errorf("state %s outcome %+v pid %d", got.State, got.LastOutcome, got.Delegation.RunnerPID)
	}
	if got.Delegation.Worktree == "" || got.Delegation.Branch == "" {
		t.Errorf("cancel must keep the worktree and branch: %+v", got.Delegation)
	}
}

func TestCancelRefusesWhenNothingRuns(t *testing.T) {
	f := newFixture()
	rec := runningTask(t, f)
	rec.State = task.NeedsDecision
	if _, err := f.app.Cancel(context.Background(), rec); err == nil || !strings.Contains(err.Error(), "NEEDS_DECISION with no runner") {
		t.Errorf("err = %v", err)
	}
	if len(f.runners.stopped) != 0 {
		t.Error("stopped a runner for a task that isn't running")
	}
}

func TestCancelReportsARunnerThatHasNotExited(t *testing.T) {
	f := newFixture()
	rec := runningTask(t, f)
	f.runners.stuck = true
	if _, err := f.app.Cancel(context.Background(), rec); err == nil || !strings.Contains(err.Error(), "has not exited yet") {
		t.Errorf("err = %v", err)
	}
	if got := f.store.recs[rec.Key]; got.State != task.Planning || got.Delegation.RunnerPID != 4242 {
		t.Errorf("a runner still running must stay recorded: %s pid %d", got.State, got.Delegation.RunnerPID)
	}
}
