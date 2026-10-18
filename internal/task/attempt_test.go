package task

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func at(minutes int) time.Time { return t0.Add(time.Duration(minutes) * time.Minute) }

func TestPhaseOfFollowsTheStage(t *testing.T) {
	for name, tc := range map[string]struct {
		rec  Record
		want Phase
	}{
		"planning is planning":                          {Record{State: Planning, Delegation: &Delegation{}}, PhasePlanning},
		"implementing, a fix included, is implementing": {Record{State: Implementing, Delegation: &Delegation{PR: 7}}, PhaseImplementation},
		"self-review is self-review":                    {Record{State: SelfReviewing, Delegation: &Delegation{PR: 7}}, PhaseSelfReview},
		"adversarial review is adversarial review":      {Record{State: AdversarialReviewing, Delegation: &Delegation{PR: 7}}, PhaseAdversarialReview},
		"opening the PR is opening PR":                  {Record{State: OpeningPR, Delegation: &Delegation{}}, PhaseOpeningPR},
		"waiting for CI is CI wait":                     {Record{State: WaitingForCI, Delegation: &Delegation{PR: 7}}, PhaseCI},
		"a decision is paused":                          {Record{State: NeedsDecision, Delegation: &Delegation{}}, PhasePaused},
		"a failure is paused":                           {Record{State: Failed, Delegation: &Delegation{}}, PhasePaused},
		"PR feedback is paused":                         {Record{State: Reviewed, Delegation: &Delegation{PR: 7}}, PhasePaused},
		"handoff ends the measurement":                  {Record{State: ReadyForHuman, Delegation: &Delegation{PR: 7}}, PhaseEnded},
		"a closed PR ends the measurement":              {Record{State: PRClosed, Delegation: &Delegation{PR: 7}}, PhaseEnded},
		"a merged PR ends the measurement":              {Record{State: Done, Delegation: &Delegation{PR: 7}}, PhaseEnded},
		"an undelegated task has no phase":              {Record{State: Discovered}, ""},
	} {
		if got := tc.rec.PhaseOf(); got != tc.want {
			t.Errorf("%s: PhaseOf() = %q, want %q", name, got, tc.want)
		}
	}
}

func TestAttemptPhasesOfAStagedRunSumToTheHandoff(t *testing.T) {
	rec := &Record{State: Planning, Delegation: &Delegation{}, Attempts: []Attempt{{StartedAt: at(0)}}}
	for i, s := range []State{Planning, Implementing, SelfReviewing, Implementing, SelfReviewing, AdversarialReviewing, OpeningPR, WaitingForCI, ReadyForHuman} {
		rec.State = s
		rec.Track(at(10 * i))
	}
	a := rec.CurrentAttempt()
	got := a.Phases(a.HandoffAt)
	want := map[Phase]time.Duration{
		PhasePlanning: 10 * time.Minute, PhaseImplementation: 20 * time.Minute, PhaseSelfReview: 20 * time.Minute,
		PhaseAdversarialReview: 10 * time.Minute, PhaseOpeningPR: 10 * time.Minute, PhaseCI: 10 * time.Minute,
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
	if a.LegacyReview() {
		t.Error("a staged attempt reports legacy review time")
	}
}

func TestAttemptWithTheOldReviewPhaseReportsLegacyReviewWithoutDoubleCounting(t *testing.T) {
	// Marks written before the upgrade, then after it, in one attempt.
	a := Attempt{StartedAt: at(0), HandoffAt: at(60), Marks: []Mark{
		{At: at(0), Phase: PhaseImplementation},
		{At: at(10), Phase: PhaseReview},
		{At: at(30), Phase: PhaseSelfReview},
		{At: at(40), Phase: PhaseCI},
		{At: at(60), Phase: PhaseEnded},
	}}
	if !a.LegacyReview() {
		t.Fatal("LegacyReview() = false for an attempt with a review mark")
	}
	got := a.Phases(a.HandoffAt)
	var sum time.Duration
	for _, d := range got {
		sum += d
	}
	if got[PhaseReview] != 20*time.Minute || sum != 60*time.Minute {
		t.Fatalf("phases = %v, want 20m legacy review and a 60m sum", got)
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
	for _, p := range append([]Phase{PhaseReview}, MeasuredPhases...) {
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
	rec := &Record{State: Planning, Delegation: &Delegation{Branch: "feat/1-x"}, Attempts: []Attempt{{StartedAt: at(0)}}}
	rec.Track(at(0))
	rec.Track(at(1))
	rec.State, rec.Delegation.PR, rec.Delegation.PRURL = SelfReviewing, 7, "https://x/pull/7"
	rec.Track(at(5))
	a := rec.CurrentAttempt()
	if len(a.Marks) != 2 || a.Marks[0].Phase != PhasePlanning || a.Marks[1] != (Mark{At: at(5), Phase: PhaseSelfReview}) {
		t.Fatalf("marks = %+v, want planning then self-review, with no mark for an unchanged phase", a.Marks)
	}
	if a.PR != 7 || a.PRURL != "https://x/pull/7" || a.Branch != "feat/1-x" {
		t.Fatalf("attempt = %+v, want the verified PR and branch", a)
	}
}

func TestTrackRecordsTheFirstHandoffOnly(t *testing.T) {
	rec := &Record{State: ReadyForHuman, Delegation: &Delegation{PR: 7}, Attempts: []Attempt{{StartedAt: at(0)}}}
	rec.Track(at(30))
	rec.State = Implementing
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
	rec.State = WaitingForCI
	rec.Track(at(25))
	if a := rec.CurrentAttempt(); !a.EndedAt.IsZero() {
		t.Fatalf("a reopened PR left EndedAt %v", a.EndedAt)
	}
}

func TestTrackLeavesARecordWithoutAttemptsAlone(t *testing.T) {
	rec := &Record{State: Implementing, Delegation: &Delegation{}}
	rec.Track(at(0))
	if rec.Attempts != nil {
		t.Fatalf("attempts = %+v", rec.Attempts)
	}
}
