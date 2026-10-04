package task

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func at(minutes int) time.Time { return t0.Add(time.Duration(minutes) * time.Minute) }

func TestPhaseOfFollowsStateAndProgress(t *testing.T) {
	posted := []PlanComment{}
	for _, d := range PlanDocs {
		posted = append(posted, PlanComment{Doc: d, URL: "u"})
	}
	clean := []Review{{PR: 7, Clean: true, Head: "h1"}}
	for name, tc := range map[string]struct {
		rec  Record
		want Phase
	}{
		"working without a plan is planning":           {Record{State: Working, Delegation: &Delegation{}}, PhasePlanning},
		"working with a posted plan is implementation": {Record{State: Working, Delegation: &Delegation{}, Plan: posted}, PhaseImplementation},
		"working with a PR is review and fixes":        {Record{State: Working, Delegation: &Delegation{PR: 7}}, PhaseReview},
		"reviewing an unreviewed head is review":       {Record{State: Reviewing, Delegation: &Delegation{PR: 7, PRHead: "h2"}, Reviews: clean}, PhaseReview},
		"reviewing a clean head is CI wait":            {Record{State: Reviewing, Delegation: &Delegation{PR: 7, PRHead: "h1"}, Reviews: clean}, PhaseCI},
		"a decision is paused":                         {Record{State: NeedsDecision, Delegation: &Delegation{}}, PhasePaused},
		"a failure is paused":                          {Record{State: Failed, Delegation: &Delegation{}}, PhasePaused},
		"PR feedback is paused":                        {Record{State: Reviewed, Delegation: &Delegation{PR: 7}}, PhasePaused},
		"handoff ends the measurement":                 {Record{State: ReadyForHuman, Delegation: &Delegation{PR: 7}}, PhaseEnded},
		"a closed PR ends the measurement":             {Record{State: PRClosed, Delegation: &Delegation{PR: 7}}, PhaseEnded},
		"a merged PR ends the measurement":             {Record{State: Done, Delegation: &Delegation{PR: 7}}, PhaseEnded},
		"an undelegated task has no phase":             {Record{State: Discovered}, ""},
	} {
		if got := tc.rec.PhaseOf(); got != tc.want {
			t.Errorf("%s: PhaseOf() = %q, want %q", name, got, tc.want)
		}
	}
}

func TestAttemptPhasesAddRepeatedVisitsAndSumToTheTotal(t *testing.T) {
	a := Attempt{StartedAt: at(0), HandoffAt: at(100), Marks: []Mark{
		{At: at(0), Phase: PhasePlanning},
		{At: at(10), Phase: PhaseImplementation},
		{At: at(40), Phase: PhaseReview},
		{At: at(50), Phase: PhasePaused},
		{At: at(65), Phase: PhaseReview},
		{At: at(75), Phase: PhaseCI},
		{At: at(100), Phase: PhaseEnded},
	}}
	got := a.Phases(a.HandoffAt)
	want := map[Phase]time.Duration{
		PhasePlanning: 10 * time.Minute, PhaseImplementation: 30 * time.Minute,
		PhaseReview: 20 * time.Minute, PhasePaused: 15 * time.Minute, PhaseCI: 25 * time.Minute,
	}
	var sum time.Duration
	for _, p := range MeasuredPhases {
		if got[p] != want[p] {
			t.Errorf("%s = %v, want %v", p, got[p], want[p])
		}
		sum += got[p]
	}
	if sum != a.HandoffAt.Sub(a.StartedAt) {
		t.Errorf("phases sum to %v, want the total %v", sum, a.HandoffAt.Sub(a.StartedAt))
	}
}

func TestAttemptPhasesStopAtTheFirstHandoff(t *testing.T) {
	a := Attempt{StartedAt: at(5), HandoffAt: at(30), Marks: []Mark{
		{At: at(0), Phase: PhasePlanning},
		{At: at(20), Phase: PhaseCI},
		{At: at(30), Phase: PhaseEnded},
		{At: at(60), Phase: PhaseReview},
		{At: at(90), Phase: PhaseEnded},
	}}
	got := a.Phases(a.HandoffAt)
	if got[PhasePlanning] != 15*time.Minute || got[PhaseCI] != 10*time.Minute || got[PhaseReview] != 0 {
		t.Fatalf("phases = %v, want 15m planning from the first turn and 10m CI up to the handoff", got)
	}
}

func TestAttemptPhasesCountTimeBeforeTheFirstMarkInItsPhase(t *testing.T) {
	a := Attempt{StartedAt: at(0), Marks: []Mark{{At: at(2), Phase: PhasePlanning}, {At: at(10), Phase: PhaseEnded}}}
	if got := a.Phases(at(10)); got[PhasePlanning] != 10*time.Minute {
		t.Fatalf("planning = %v, want 10m", got[PhasePlanning])
	}
}

func TestAttemptPhasesCountAClosedAndReopenedGapAsPaused(t *testing.T) {
	a := Attempt{StartedAt: at(0), HandoffAt: at(30), Marks: []Mark{
		{At: at(0), Phase: PhaseReview}, {At: at(10), Phase: PhaseEnded}, {At: at(20), Phase: PhaseReview}, {At: at(30), Phase: PhaseEnded},
	}}
	if got := a.Phases(a.HandoffAt); got[PhasePaused] != 10*time.Minute || got[PhaseReview] != 20*time.Minute {
		t.Fatalf("phases = %v", got)
	}
}

func TestAttemptPhasesAreUnknownWithoutAStart(t *testing.T) {
	a := Attempt{Marks: []Mark{{At: at(0), Phase: PhasePlanning}}}
	if got := a.Phases(at(10)); got != nil {
		t.Fatalf("phases = %v, want nil", got)
	}
}

func TestTrackMarksPhaseChangesOnTheCurrentAttempt(t *testing.T) {
	rec := &Record{State: Working, Delegation: &Delegation{Branch: "feat/1-x"}, Attempts: []Attempt{{StartedAt: at(0)}}}
	rec.Track(at(0))
	rec.Track(at(1))
	rec.State, rec.Delegation.PR, rec.Delegation.PRURL = Reviewing, 7, "https://x/pull/7"
	rec.Track(at(5))
	a := rec.CurrentAttempt()
	if len(a.Marks) != 2 || a.Marks[0].Phase != PhasePlanning || a.Marks[1] != (Mark{At: at(5), Phase: PhaseReview}) {
		t.Fatalf("marks = %+v, want planning then review, with no mark for an unchanged phase", a.Marks)
	}
	if a.PR != 7 || a.PRURL != "https://x/pull/7" || a.Branch != "feat/1-x" {
		t.Fatalf("attempt = %+v, want the verified PR and branch", a)
	}
}

func TestTrackRecordsTheFirstHandoffOnly(t *testing.T) {
	rec := &Record{State: ReadyForHuman, Delegation: &Delegation{PR: 7}, Attempts: []Attempt{{StartedAt: at(0)}}}
	rec.Track(at(30))
	rec.State = Working
	rec.Track(at(40))
	rec.State = ReadyForHuman
	rec.Track(at(50))
	if a := rec.CurrentAttempt(); !a.HandoffAt.Equal(at(30)) || len(a.Marks) != 3 {
		t.Fatalf("attempt = %+v, want the handoff at 30m and a mark for each change", a)
	}
}

func TestTrackEndsAnAttemptWhenItsPRClosesOrMerges(t *testing.T) {
	rec := &Record{State: PRClosed, Delegation: &Delegation{PR: 7}, Attempts: []Attempt{{StartedAt: at(0)}}}
	rec.Track(at(20))
	if a := rec.CurrentAttempt(); !a.EndedAt.Equal(at(20)) || !a.HandoffAt.IsZero() {
		t.Fatalf("attempt = %+v, want ended at 20m with no handoff", a)
	}
	rec.State = Reviewing
	rec.Track(at(25))
	if a := rec.CurrentAttempt(); !a.EndedAt.IsZero() {
		t.Fatalf("a reopened PR left EndedAt %v", a.EndedAt)
	}
}

func TestTrackLeavesARecordWithoutAttemptsAlone(t *testing.T) {
	rec := &Record{State: Working, Delegation: &Delegation{}}
	rec.Track(at(0))
	if rec.Attempts != nil {
		t.Fatalf("attempts = %+v", rec.Attempts)
	}
}
