package task

import "time"

// Phase is where an attempt's time goes. The measured phases tile the time from the
// attempt's first agent turn to its first handoff.
type Phase string

const (
	PhasePlanning          Phase = "planning"
	PhaseImplementation    Phase = "implementation" // implementer turns, review fixes included
	PhaseSelfReview        Phase = "self_review"
	PhaseAdversarialReview Phase = "adversarial_review"
	PhaseOpeningPR         Phase = "opening_pr"
	PhaseCI                Phase = "ci"
	PhasePaused            Phase = "paused" // waiting on the developer: a decision, a failure, PR feedback
	// PhaseEnded is a handed-over, merged, or closed PR. It is not measured.
	PhaseEnded Phase = "ended"
	// PhaseReview is the review rounds and the fix turns between them together, as
	// attempts recorded them before the stages. Foreman no longer writes it.
	PhaseReview Phase = "review"
)

// MeasuredPhases lists the phases an attempt's total is split into, in display order.
// An attempt with legacy review time adds PhaseReview.
var MeasuredPhases = []Phase{PhasePlanning, PhaseImplementation, PhaseSelfReview, PhaseAdversarialReview, PhaseOpeningPR, PhaseCI, PhasePaused}

// ValidPhase reports whether p is a phase a task file may hold.
func ValidPhase(p Phase) bool {
	switch p {
	case PhasePlanning, PhaseImplementation, PhaseSelfReview, PhaseAdversarialReview, PhaseOpeningPR, PhaseCI, PhasePaused, PhaseEnded, PhaseReview:
		return true
	}
	return false
}

// Mark opens a phase. The phase lasts until the next mark.
type Mark struct {
	At    time.Time `yaml:"at"`
	Phase Phase     `yaml:"phase"`
}

// Attempt is one run of work on an issue that produces one PR. A restart continues
// the attempt while it keeps the PR or before any PR exists; a restart that will open
// a new PR starts another.
type Attempt struct {
	// StartedAt is the attempt's first agent turn.
	StartedAt time.Time `yaml:"started_at,omitempty"`
	Agent     Agent     `yaml:"agent,omitempty"`
	Branch    string    `yaml:"branch,omitempty"`
	PR        int       `yaml:"pr,omitempty"`
	PRURL     string    `yaml:"pr_url,omitempty"`
	// LogStart is the task log's size when the attempt began, so its lines start there.
	LogStart int64 `yaml:"log_start,omitempty"`
	// HandoffAt is the first READY_FOR_HUMAN. Later handoffs leave it alone.
	HandoffAt time.Time `yaml:"handoff_at,omitempty"`
	// EndedAt is when the PR merged or closed, as foreman saw it; zero while open.
	EndedAt time.Time `yaml:"ended_at,omitempty"`
	Marks   []Mark    `yaml:"marks,omitempty"`
}

// CurrentAttempt is the latest attempt, or nil.
func (r *Record) CurrentAttempt() *Attempt {
	if len(r.Attempts) == 0 {
		return nil
	}
	return &r.Attempts[len(r.Attempts)-1]
}

// PhaseOf is the phase of the task's state: each stage is its own phase.
func (r *Record) PhaseOf() Phase {
	if r.Delegation == nil {
		return ""
	}
	switch r.State {
	case Planning:
		return PhasePlanning
	case Implementing:
		return PhaseImplementation
	case SelfReviewing:
		return PhaseSelfReview
	case AdversarialReviewing:
		return PhaseAdversarialReview
	case OpeningPR:
		return PhaseOpeningPR
	case WaitingForCI:
		return PhaseCI
	case NeedsDecision, Failed, Reviewed:
		return PhasePaused
	case ReadyForHuman, Done, PRClosed:
		return PhaseEnded
	}
	return ""
}

// LegacyReview reports an attempt with time in the old combined review phase. Its
// review and fix time cannot be split into the stages afterwards.
func (a *Attempt) LegacyReview() bool {
	for _, m := range a.Marks {
		if m.Phase == PhaseReview {
			return true
		}
	}
	return false
}

// Track records the current phase on the current attempt. It is called on every task
// write, so the runner and the dashboard's refresh both leave marks. A task with no
// attempt predates this record and is left alone.
func (r *Record) Track(now time.Time) {
	a := r.CurrentAttempt()
	p := r.PhaseOf()
	if a == nil || p == "" {
		return
	}
	if d := r.Delegation; d.PR != 0 && a.PR == 0 {
		a.PR, a.PRURL, a.Branch = d.PR, d.PRURL, d.Branch
	}
	if n := len(a.Marks); n == 0 || a.Marks[n-1].Phase != p {
		a.Marks = append(a.Marks, Mark{At: now, Phase: p})
	}
	if r.State == ReadyForHuman && a.HandoffAt.IsZero() {
		a.HandoffAt = now
	}
	switch {
	case r.State != Done && r.State != PRClosed:
		a.EndedAt = time.Time{}
	case a.EndedAt.IsZero():
		a.EndedAt = now
	}
}

// Phases sums the time in each measured phase from StartedAt to end. Time before the
// first mark counts in that mark's phase, and a closed PR's gap before a reopen counts
// as paused, so the phases add up to end minus StartedAt. It is nil without a start.
func (a *Attempt) Phases(end time.Time) map[Phase]time.Duration {
	if a.StartedAt.IsZero() || len(a.Marks) == 0 || !end.After(a.StartedAt) {
		return nil
	}
	out := map[Phase]time.Duration{}
	for i, m := range a.Marks {
		from, to := m.At, end
		if i == 0 || from.Before(a.StartedAt) {
			from = a.StartedAt
		}
		if i+1 < len(a.Marks) && a.Marks[i+1].At.Before(end) {
			to = a.Marks[i+1].At
		}
		if !to.After(from) {
			continue
		}
		p := m.Phase
		if p == PhaseEnded {
			p = PhasePaused
		}
		out[p] += to.Sub(from)
	}
	return out
}
