package app

import (
	"context"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

// waiting runs a task to REVIEWED with one comment to answer.
func waiting(t *testing.T, f fixture) task.Key {
	t.Helper()
	key := ready(t, f)
	f.gh.addPRComment(912, prComment(4401, "This retries 4xx too."))
	refresh(t, f)
	if got := f.store.recs[key]; got.State != task.Reviewed {
		t.Fatalf("setup: state %s", got.State)
	}
	return key
}

func TestResumeKeepsTheSessionWorktreeAndPR(t *testing.T) {
	f := newFixture()
	key := waiting(t, f)
	before := *f.store.recs[key].Delegation

	rec, err := f.app.Resume(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != task.Working {
		t.Fatalf("state = %s", rec.State)
	}
	d := rec.Delegation
	if d.Implementer != before.Implementer || d.Worktree != before.Worktree || d.Branch != before.Branch || d.PR != before.PR || d.PRHead != before.PRHead {
		t.Fatalf("delegation changed:\n got %+v\nwant %+v", *d, before)
	}
	if len(f.wt.reqs) != 1 {
		t.Errorf("resume touched the worktree: %+v", f.wt.reqs)
	}
	if len(f.runners.spawned) != 2 || d.RunnerPID == 0 {
		t.Errorf("spawned %d runners, pid %d", len(f.runners.spawned), d.RunnerPID)
	}
	if len(rec.UnhandledFeedback()) != 1 {
		t.Errorf("the comment to answer was lost: %+v", rec.Feedback)
	}
}

func TestResumeRefusesATaskWithNothingToAnswer(t *testing.T) {
	f := newFixture()
	key := ready(t, f)
	if _, err := f.app.Resume(context.Background(), key); err == nil || !strings.Contains(err.Error(), "READY_FOR_HUMAN") {
		t.Fatalf("err = %v", err)
	}
}

func TestResumeRefusesATaskThatIsAlreadyRunning(t *testing.T) {
	f := newFixture()
	key := waiting(t, f)
	f.store.recs[key].Delegation.RunnerPID = 77
	f.runners.alive[77] = true
	if _, err := f.app.Resume(context.Background(), key); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("err = %v", err)
	}
}

func TestStartPointsAReviewedTaskAtResume(t *testing.T) {
	f := newFixture()
	waiting(t, f)
	_, err := f.app.Start(context.Background(), startReq(t))
	if err == nil || !strings.Contains(err.Error(), "select it in the inbox to resume") {
		t.Fatalf("err = %v", err)
	}
}
