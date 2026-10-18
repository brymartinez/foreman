package app

import (
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func TestFailTaskRecordsAFailedOutcome(t *testing.T) {
	rec := &task.Record{State: task.WaitingForCI, LastOutcome: &task.Outcome{Status: "review_clean"}}
	failTask(rec, "conflict check failed: exit status 128", now)
	want := task.Outcome{Status: "failed", Summary: "conflict check failed: exit status 128", At: now}
	if rec.State != task.Failed || rec.LastOutcome == nil || *rec.LastOutcome != want {
		t.Fatalf("state %s outcome %+v, want FAILED %+v", rec.State, rec.LastOutcome, want)
	}
}

func TestFirstStepResumesWhereTheTaskLeftOff(t *testing.T) {
	cleanAt := func(head string) []task.Review {
		return []task.Review{{Round: 1, PR: 912, Head: head, Clean: true}}
	}
	answer := []task.Decision{{Question: task.Question{Issue: "Retry 4xx?"}, Answer: "A. 5xx only", AnsweredAt: now}}
	tests := []struct {
		name      string
		d         *task.Delegation
		reviews   []task.Review
		decisions []task.Decision
		waiting   int
		want      step
	}{
		{"no PR yet implements", &task.Delegation{}, nil, nil, 0, stepImplement},
		{"waiting comments are answered first", &task.Delegation{PR: 912, PRHead: "c0ffee1"}, nil, nil, 1, stepAnswer},
		{"an unreviewed PR head is reviewed", &task.Delegation{PR: 912, PRHead: "c0ffee1"}, nil, nil, 0, stepPickUpReview},
		{"a head newer than its review is reviewed", &task.Delegation{PR: 912, PRHead: "beef002"}, cleanAt("c0ffee1"), nil, 0, stepPickUpReview},
		{"a reviewed head implements", &task.Delegation{PR: 912, PRHead: "c0ffee1"}, cleanAt("c0ffee1"), nil, 0, stepImplement},
		{"a PR with no recorded head implements", &task.Delegation{PR: 912}, nil, nil, 0, stepImplement},
		{"an answer to deliver implements before an unreviewed head", &task.Delegation{PR: 912, PRHead: "c0ffee1"}, nil, answer, 0, stepImplement},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &task.Record{Delegation: tt.d, Reviews: tt.reviews, Decisions: tt.decisions}
			if got := firstStep(rec, tt.waiting); got != tt.want {
				t.Errorf("firstStep = %v, want %v", got, tt.want)
			}
		})
	}
}
