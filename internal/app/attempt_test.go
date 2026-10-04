package app

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

// ticking makes the fixture's clock advance a minute per read, so marks are ordered.
func ticking(f *fixture) {
	at := now
	f.app.Now = func() time.Time {
		at = at.Add(time.Minute)
		return at
	}
}

func TestStartOpensAnAttemptAndTheRunnerStartsIt(t *testing.T) {
	f := newFixture()
	ticking(&f)
	key := started(t, f, startReq(t))
	a := f.store.recs[key].CurrentAttempt()
	if a == nil || a.Agent != task.Claude || !a.StartedAt.IsZero() || a.LogStart != 0 {
		t.Fatalf("attempt after start = %+v, want a claude attempt not started yet at log offset 0", a)
	}
	f.claude.turn = prTurn("sid-1")
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	rec := f.store.recs[key]
	a = rec.CurrentAttempt()
	if len(rec.Attempts) != 1 || a.StartedAt.IsZero() || a.PR != 912 || a.Branch != "feat/825-provider-retries" {
		t.Fatalf("attempt after the run = %+v", a)
	}
	if first := a.Marks[0]; first.Phase != task.PhasePlanning || first.At.After(a.StartedAt) {
		t.Fatalf("first mark %+v, want planning at or before the first turn %v", first, a.StartedAt)
	}
	if last := a.Marks[len(a.Marks)-1]; last.Phase != rec.PhaseOf() {
		t.Fatalf("last mark %+v, want the current phase %s", last, rec.PhaseOf())
	}
}

func TestRefreshHandoffAndMergeWriteMarks(t *testing.T) {
	f := newFixture()
	ticking(&f)
	key := reviewed(t, f)
	setCI(f, task.CIStatus{State: task.CIPassing})
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := f.store.recs[key].CurrentAttempt()
	if a.HandoffAt.IsZero() || a.Marks[len(a.Marks)-1].Phase != task.PhaseEnded {
		t.Fatalf("attempt after the gate's handoff = %+v", a)
	}
	handoff := a.HandoffAt
	setPR(f, "merged")
	f.gh.items = []InboxItem{item(825, "open", t0, task.Assigned)}
	if _, err := f.app.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	a = f.store.recs[key].CurrentAttempt()
	if a.EndedAt.IsZero() || !a.HandoffAt.Equal(handoff) {
		t.Fatalf("attempt after the merge = %+v, want an end and the first handoff kept", a)
	}
}

func TestRefreshClosedPRBeforeHandoffEndsTheAttempt(t *testing.T) {
	f := newFixture()
	ticking(&f)
	key := reviewed(t, f)
	setPR(f, "closed")
	f.gh.items = []InboxItem{item(825, "open", t0, task.Assigned)}
	if _, err := f.app.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec := f.store.recs[key]
	a := rec.CurrentAttempt()
	if rec.State != task.PRClosed || a.EndedAt.IsZero() || !a.HandoffAt.IsZero() {
		t.Fatalf("state %s attempt %+v, want PR_CLOSED ended with no handoff", rec.State, a)
	}
}

func TestRestartAfterAClosedPRStartsASecondAttempt(t *testing.T) {
	f := newFixture()
	ticking(&f)
	key := reviewed(t, f)
	log := filepath.Join(t.TempDir(), "825.log")
	if err := os.WriteFile(log, []byte("2026-10-01T09:00:00Z foreman: runner 1 started\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.store.recs[key].Delegation.RunnerLog = log
	setPR(f, "closed")
	f.gh.items = []InboxItem{item(825, "open", t0, task.Assigned)}
	if _, err := f.app.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	started(t, f, startReq(t))
	rec := f.store.recs[key]
	if len(rec.Attempts) != 2 || rec.Attempts[0].PR != 912 || rec.Attempts[1].PR != 0 || rec.Attempts[1].LogStart != 47 {
		t.Fatalf("attempts = %+v, want the closed PR's attempt and a new one starting at byte 47", rec.Attempts)
	}
}

func TestRestartThatKeepsThePRContinuesItsAttempt(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	f.store.recs[key].State = task.Failed
	started(t, f, startReq(t))
	rec := f.store.recs[key]
	if len(rec.Attempts) != 1 || rec.Delegation.PR != 912 {
		t.Fatalf("attempts = %+v pr %d, want the one attempt for the kept PR", rec.Attempts, rec.Delegation.PR)
	}
}

// Marks are written inside App.update, so a write that skips it would leave a phase
// change unrecorded.
func TestOnlyUpdateWritesTaskFiles(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	updates := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		updates += strings.Count(string(src), "Store.Update(")
		if strings.Contains(string(src), "Store.Save(") {
			t.Errorf("%s calls Store.Save; write through App.update", name)
		}
	}
	if updates != 1 {
		t.Errorf("found %d Store.Update calls, want only the one in App.update", updates)
	}
}
