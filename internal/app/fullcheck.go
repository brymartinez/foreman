package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// evaluateFull checks a turn's full-suite report against head and returns the result
// to store, or nil when the turn sent no report. The implementer judges whether each
// changed path is documentation-only from repository-specific evidence. Foreman checks
// only what Git can show: the run's revision is head or its ancestor, at least one full
// check ran and all passed, and every path changed since the revision is named exactly
// with nonempty evidence.
func (a App) evaluateFull(ctx context.Context, rec *task.Record, o agent.Outcome, head string) *task.FullResult {
	report := o.FullSuite
	if report == nil {
		return nil
	}
	res := &task.FullResult{Head: head, At: a.Now()}
	for _, c := range o.Validation {
		if c.Scope == task.FullCheck {
			res.Checks = append(res.Checks, c)
		}
	}
	if report.Status == agent.FullSuiteUnavailable {
		res.Unverified = report.Explanation
		return res
	}
	res.Revision, res.DocOnly = report.Revision, report.DocOnly
	if len(res.Checks) == 0 {
		res.Checks = recordedChecks(rec.FullResult, report.Revision)
	}
	res.Reason = a.uncovered(ctx, rec, res)
	res.Covered = res.Reason == ""
	return res
}

// recordedChecks are the full checks Foreman recorded for a run on revision, for a turn
// that reuses that run and lists only the checks it ran itself. A revision Foreman has
// no run for has none.
func recordedChecks(stored *task.FullResult, revision string) []task.Check {
	if stored == nil || stored.Revision == "" || !sameCommit(stored.Revision, revision) {
		return nil
	}
	return stored.Checks
}

// sameCommit reports whether two names of a commit agree, one possibly abbreviated.
func sameCommit(a, b string) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	return len(a) >= 7 && strings.HasPrefix(b, a)
}

// uncovered is why res does not cover its head, or "" when it does.
func (a App) uncovered(ctx context.Context, rec *task.Record, res *task.FullResult) string {
	if len(res.Checks) == 0 {
		return "the report names no full check"
	}
	var failed []string
	for _, c := range res.Checks {
		if !c.Passed {
			failed = append(failed, c.Command)
		}
	}
	if len(failed) > 0 {
		return "full checks failed: " + strings.Join(failed, ", ")
	}
	paths, err := a.Worktrees.ChangedPaths(ctx, rec.Delegation.Worktree, res.Revision, res.Head)
	if err != nil {
		return fmt.Sprintf("the full run on %s cannot cover %s: %v", short(res.Revision), short(res.Head), err)
	}
	evidenced := map[string]bool{}
	for _, p := range res.DocOnly {
		if strings.TrimSpace(p.Evidence) != "" {
			evidenced[p.Path] = true
		}
	}
	var missing []string
	for _, p := range paths {
		if !evidenced[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return fmt.Sprintf("paths changed since the full run on %s have no documentation-only evidence: %s", short(res.Revision), strings.Join(missing, ", "))
	}
	return ""
}

// recordFull stores a verified turn's full result. A turn that sent no report keeps the
// earlier result, which no longer covers a head the turn moved.
func recordFull(rec *task.Record, res turnResult) {
	if res.err == nil && res.full != nil {
		rec.FullResult = res.full
	}
}

// maxFinalChecks bounds the final-check turns toward one handoff.
const maxFinalChecks = 3

// holdForFinalCheck keeps a cleanly reviewed change that no full result covers from
// handoff, and sends it to the final check instead. A change that is covered starts the
// next handoff with a fresh final-check budget.
func holdForFinalCheck(rec *task.Record) {
	if rec.State != task.WaitingForCI && rec.State != task.OpeningPR {
		return
	}
	if rec.FinalCheckPending() {
		rec.State = task.Implementing
		return
	}
	rec.Delegation.FinalChecks = 0
}

// finalCheck runs the implementer turn that supplies the full-suite result a cleanly
// reviewed change needs before handoff. A turn that keeps the head and covers it hands
// the change off without another review. A turn that commits a fix sends the new head
// through review again.
func (r *taskRun) finalCheck() (step, error) {
	a, d := r.a, r.rec.Delegation
	head, _ := r.rec.CurrentChange()
	if d.FinalChecks >= maxFinalChecks {
		rec, err := a.update(r.final, r.rec.Key, existing(func(cur *task.Record) {
			failTask(cur, fmt.Sprintf("%d final checks left %s without a full-suite result that covers it: %s", maxFinalChecks, short(head), cur.FullResult.Gap(head)), a.Now())
		}))
		r.rec = rec
		return stepDone, err
	}
	if err := r.enter(task.Implementing); err != nil {
		return stepDone, err
	}
	check := agent.FinalCheck{Head: head, Reason: r.rec.FullResult.Gap(head), Local: r.rec.LocalReview(), PR: d.PR}
	if f := r.rec.FullResult; f != nil && f.Revision != "" {
		check.Revision = f.Revision
		// The list only helps the implementer; the turn's own report is what is checked.
		check.Changed, _ = a.Worktrees.ChangedPaths(r.ctx, d.Worktree, f.Revision, head)
	}
	fmt.Fprintf(r.log, "foreman: %s has no full-suite result that covers it (%s); running the final check (%d/%d)\n", short(head), check.Reason, d.FinalChecks+1, maxFinalChecks)
	res := a.runTurn(r.ctx, r.rec, turnRequest{finalCheck: &check}, a.verifiedPR(r.log), r.log)
	rec, err := a.finishTurn(r.ctx, r.final, r.rec.Key, "the final full check", res, func(cur *task.Record) {
		applyTurnResult(cur, res, a.Now())
		cur.Delegation.FinalChecks++
		if res.err == nil && res.turn.Outcome.Status == agent.PROpened {
			handOffIfReviewed(cur)
		}
	}, r.log)
	if err != nil {
		return stepDone, err
	}
	r.rec = rec
	switch {
	case res.err != nil:
		return stepDone, nil
	case rec.State == task.WaitingForCI && a.ReplyToPRFeedback:
		return stepHandover, nil
	case rec.State == task.WaitingForCI:
		return stepDone, nil
	case rec.OpenPending():
		return stepOpenPR, nil
	case rec.State.Reviewing() || rec.State == task.Implementing && res.turn.Outcome.Status == agent.ReviewReady:
		return stepReview, nil
	}
	return stepDone, nil
}

// handOffIfReviewed moves a PR whose final check kept the reviewed head, and covered
// it, on to the readiness gate without another review.
func handOffIfReviewed(rec *task.Record) {
	d := rec.Delegation
	if !reviewCovers(rec, d.PRHead, d.PRDigest) || !rec.FullResult.Covers(d.PRHead) {
		return
	}
	rec.State = task.WaitingForCI
	d.FinalChecks = 0
}

// fullSuiteResume adds the full-suite rules a resumed turn of a gated task works by. A
// turn that must hand in a covering result gets the rule to run the full suite, and a
// final check resumed with answers gets its own instructions back. Every other turn
// runs focused checks.
func fullSuiteResume(rec *task.Record, req turnRequest, resume string) string {
	switch {
	case !rec.Delegation.FullSuiteGate:
		return resume
	case req.finalCheck != nil:
		if msg := agent.FinalCheckMessage(*req.finalCheck); resume != msg {
			return resume + "\n\n" + msg
		}
		return resume
	case needsFullResult(rec, req):
		return resume + agent.FirstRunMessage
	}
	return resume + agent.FullSuiteMessage
}
