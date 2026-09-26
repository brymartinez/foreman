package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

// gate is the readiness gate. It runs whenever inbox or status is invoked, and
// advances each task no runner is driving from what GitHub reports now: a merged PR
// is DONE, a closed one FAILED, and a clean review with finished CI READY_FOR_HUMAN.
// CI belongs to the repo's owners, so foreman only reads GitHub's check results and
// hands failures to the developer instead of fixing them (D46). It returns what
// changed, per task.
func (a App) gate(ctx context.Context, recs []*task.Record) (map[task.Key][]string, []error, error) {
	notes := map[task.Key][]string{}
	var warnings []error
	for i, r := range recs {
		if !gated(r) {
			continue
		}
		pr, err := a.GitHub.GetPullRequest(ctx, task.Key{Repo: r.Repo, Number: r.Delegation.PR})
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			warnings = append(warnings, fmt.Errorf("refresh %s#%d PR: %w", r.Repo, r.Number, err))
			continue
		}
		var ci task.CIStatus
		if pr.State == "open" {
			if ci, err = a.GitHub.CIStatus(ctx, pr.Key); err != nil {
				if ctx.Err() != nil {
					return nil, nil, ctx.Err()
				}
				warnings = append(warnings, fmt.Errorf("refresh %s#%d CI: %w", r.Repo, r.Number, err))
				continue
			}
			ci.CheckedAt = a.Now()
		}
		var note string
		updated, err := a.update(ctx, r.Key, func(cur *task.Record) (*task.Record, error) {
			if gated(cur) && cur.Delegation.PR == r.Delegation.PR {
				note = advance(cur, pr, ci, a.Now())
			}
			return cur, nil
		})
		if err != nil {
			return nil, nil, err
		}
		if updated.State == task.Working && r.State != task.Working {
			if updated, err = a.launch(ctx, r.Key); err != nil {
				return nil, nil, err
			}
		}
		if note != "" {
			notes[r.Key] = append(notes[r.Key], note)
		}
		recs[i] = updated
	}
	return notes, warnings, nil
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
// inbox note for the change, if the developer should hear about it. A task that is
// already READY_FOR_HUMAN only has its CI refreshed, so the view stays current.
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
	if pr.HeadSHA != "" {
		// A READY task's PR is the developer's; the checklist flags new commits.
		d.PRHead = pr.HeadSHA
		if rec.State == task.Reviewing && rec.CommitsSinceReview() {
			// Commits landed after the clean review: review them before handing over (D63).
			rec.State, rec.CI = task.Working, nil
			return outcome("re_review", fmt.Sprintf("PR #%d has new commits since its clean review; %s is reviewing them", d.PR, d.Reviewer))
		}
	}
	prev := rec.CI
	rec.CI = &ci
	if rec.State == task.ReadyForHuman {
		if prev != nil && prev.State != ci.State && ci.State != task.CIPending {
			return fmt.Sprintf("CI now %s", ciText(ci))
		}
		return ""
	}
	switch {
	case ci.State == task.CIPending:
		return ""
	case ci.Green():
		rec.State = task.ReadyForHuman
		outcome("ready", fmt.Sprintf("PR #%d passed review and CI", d.PR))
		return ""
	}
	rec.State = task.ReadyForHuman
	outcome("ready", fmt.Sprintf("PR #%d passed review; CI failed (%s)", d.PR, checkNames(ci.Failing)))
	return ""
}

func ciText(ci task.CIStatus) string {
	if ci.State == task.CIFailing {
		return fmt.Sprintf("failing (%s)", checkNames(ci.Failing))
	}
	return string(ci.State)
}

func checkNames(checks []task.CheckRun) string {
	names := make([]string, len(checks))
	for i, c := range checks {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}
