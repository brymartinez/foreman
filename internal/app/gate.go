package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

// MaxCIFixes is how many times the gate sends the implementer back to fix failing CI
// before it gives the task to the developer as FAILED (D38).
const MaxCIFixes = 2

// gate is the readiness gate. It runs whenever inbox or status is invoked, and
// advances each task no runner is driving from what GitHub reports now: a merged PR
// is DONE, a closed one FAILED, and a clean review with green CI READY_FOR_HUMAN.
// Failing CI relaunches the implementer. It returns what changed, per task.
func (a App) gate(ctx context.Context, recs []*task.Record) (map[task.Key][]string, error) {
	notes := map[task.Key][]string{}
	for i, r := range recs {
		if !gated(r) {
			continue
		}
		pr, err := a.GitHub.GetPullRequest(ctx, task.Key{Repo: r.Repo, Number: r.Delegation.PR})
		if err != nil {
			return nil, err
		}
		var ci task.CIStatus
		if pr.State == "open" && r.State == task.Reviewing {
			if ci, err = a.GitHub.CIStatus(ctx, pr.Key); err != nil {
				return nil, err
			}
			ci.CheckedAt = a.Now()
		}
		var note string
		relaunch := false
		updated, err := a.Store.Update(ctx, r.Key, func(cur *task.Record) (*task.Record, error) {
			if gated(cur) && cur.Delegation.PR == r.Delegation.PR {
				note = advance(cur, pr, ci, a.Now())
				relaunch = cur.State == task.Working
			}
			return cur, nil
		})
		if err != nil {
			return nil, err
		}
		if note != "" {
			notes[r.Key] = append(notes[r.Key], note)
		}
		if relaunch {
			if updated, err = a.launch(ctx, r.Key); err != nil {
				return nil, err
			}
		}
		recs[i] = updated
	}
	return notes, nil
}

// gated tasks wait on GitHub alone: the review is clean, or the developer has the PR.
func gated(r *task.Record) bool {
	d := r.Delegation
	if d == nil || d.PR == 0 || d.RunnerPID != 0 {
		return false
	}
	switch r.State {
	case task.ReadyForHuman:
		return true
	case task.Reviewing:
		latest := r.LatestReview()
		return latest != nil && latest.Clean && latest.PR == d.PR
	}
	return false
}

// advance applies the PR state and CI result to a gated task, and returns the
// inbox note for the change, if the developer should hear about it.
func advance(rec *task.Record, pr PullRequest, ci task.CIStatus, now time.Time) string {
	d := rec.Delegation
	outcome := func(status, summary string) string {
		rec.LastOutcome = &task.Outcome{Status: status, Summary: summary, At: now}
		return summary
	}
	switch pr.State {
	case "merged":
		rec.State = task.Done
		return outcome("merged", fmt.Sprintf("PR #%d merged", d.PR))
	case "closed":
		rec.State = task.Failed
		return outcome("failed", fmt.Sprintf("PR #%d closed without merging", d.PR))
	}
	if rec.State != task.Reviewing {
		return ""
	}
	rec.CI = &ci
	switch {
	case ci.Green():
		rec.State = task.ReadyForHuman
		outcome("ready", fmt.Sprintf("PR #%d passed review and CI", d.PR))
		return ""
	case ci.State == task.CIPending:
		return ""
	case rec.CIFixes < MaxCIFixes:
		rec.CIFixes++
		rec.State = task.Working
		return outcome("ci_failed", fmt.Sprintf("CI failed (%s); sent back to %s, fix %d of %d", checkNames(ci.Failing), d.Implementer.Agent, rec.CIFixes, MaxCIFixes))
	}
	rec.State = task.Failed
	return outcome("failed", fmt.Sprintf("CI still failing (%s) after %d fixes by %s", checkNames(ci.Failing), MaxCIFixes, d.Implementer.Agent))
}

func checkNames(checks []task.CheckRun) string {
	names := make([]string, len(checks))
	for i, c := range checks {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}
