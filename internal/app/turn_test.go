package app

import (
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func TestFailTaskRecordsAFailedOutcome(t *testing.T) {
	rec := &task.Record{State: task.Reviewing, LastOutcome: &task.Outcome{Status: "review_clean"}}
	failTask(rec, "conflict check failed: exit status 128", now)
	want := task.Outcome{Status: "failed", Summary: "conflict check failed: exit status 128", At: now}
	if rec.State != task.Failed || rec.LastOutcome == nil || *rec.LastOutcome != want {
		t.Fatalf("state %s outcome %+v, want FAILED %+v", rec.State, rec.LastOutcome, want)
	}
}
