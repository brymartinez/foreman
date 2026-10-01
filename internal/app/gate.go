package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

// gate is the readiness gate. It runs whenever inbox or status is invoked, and
// advances each task from what GitHub reports now: a merged PR is DONE, one closed
// without merging is PR_CLOSED, and a clean review with finished CI is
// READY_FOR_HUMAN. CI belongs to the repo's owners, so foreman only reads GitHub's
// check results and hands failures to the developer instead of fixing them (D46).
// It returns what changed, per task.
func (a App) gate(ctx context.Context, recs []*task.Record) (map[task.Key][]string, []error, error) {
	notes := map[task.Key][]string{}
	var warnings []error
	for i, r := range recs {
		if !watched(r) {
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
		if pr.State == "closed" {
			updated, note, err := a.endClosed(ctx, r)
			if err != nil {
				return nil, nil, err
			}
			if endedBy(updated, pr.Key.Number) {
				if err := a.stopRun(ctx, updated); err != nil {
					warnings = append(warnings, fmt.Errorf("stop the runner of %s#%d after PR #%d closed: %w", r.Repo, r.Number, r.Delegation.PR, err))
				}
			}
			if note != "" {
				notes[r.Key] = append(notes[r.Key], note)
			}
			recs[i] = updated
			continue
		}
		if r.State != task.PRClosed && !gated(r) {
			// A task a runner is driving, with a PR that is still open: it is the
			// runner's to advance, so only the PR's state was worth a look.
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
			if cur.Delegation == nil || cur.Delegation.PR != r.Delegation.PR {
				return cur, nil
			}
			// The state is read again under the lock, so a runner that saved since the
			// list is not overwritten by what this refresh expected to find.
			switch {
			case cur.State == task.PRClosed && pr.State == "merged":
				// Reopened and merged between two refreshes: the work shipped.
				note = advance(cur, pr, ci, a.Now())
			case cur.State == task.PRClosed:
				note = reopen(cur, pr, ci, a.Now())
			case gated(cur):
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

// watched tasks have a PR whose state the refresh must read. It is wider than gated,
// because a PR closed while an agent is running still ends the task; the extra tasks
// only cost the PR lookup, not the CI one.
func watched(r *task.Record) bool {
	d := r.Delegation
	if d == nil || d.PR == 0 {
		return false
	}
	if r.State == task.PRClosed {
		return true
	}
	_, active := attention[r.State]
	return active
}

// endClosed records a task whose PR was closed without merging, and returns the task
// as it now stands with the inbox note for the change. The state is written before the
// runner is stopped, because a runner that finds its task already PR_CLOSED records
// nothing: stopping it first would have it save the stop as a FAILED run and notify
// the developer about a failure that did not happen (D107).
func (a App) endClosed(ctx context.Context, r *task.Record) (*task.Record, string, error) {
	var note string
	updated, err := a.update(ctx, r.Key, func(cur *task.Record) (*task.Record, error) {
		// Read again under the lock: a start since the list may have given the task
		// another PR, and that run is not this PR's to end.
		if cur.Delegation == nil || cur.Delegation.PR != r.Delegation.PR || cur.State == task.PRClosed {
			return cur, nil
		}
		note = closePR(cur, a.Now())
		return cur, nil
	})
	return updated, note, err
}

// endedBy reports a task this refresh has ended for the closed pr: PR_CLOSED, and
// still on that PR. endClosed writes nothing when a start since the list gave the
// task another PR, and that run is not the closed PR's to stop; the record it
// returned is then the new run's, pid and all. An already PR_CLOSED task on the same
// PR is ours, which is how a runner that would not stop is stopped on a later refresh.
func endedBy(rec *task.Record, pr int) bool {
	return rec != nil && rec.Delegation != nil && rec.Delegation.PR == pr && rec.State == task.PRClosed
}

// stopRun stops the runner a task still holds once its PR is closed, so no agent keeps
// working on a PR that is gone, and clears the pid it stopped. It takes the pid from
// the record the locked write returned and clears only that pid, so a run started
// since keeps its runner; the next refresh ends that one instead. A stop that fails
// leaves the pid in place, and every refresh tries again.
func (a App) stopRun(ctx context.Context, r *task.Record) error {
	if r.Delegation == nil || r.Delegation.RunnerPID == 0 {
		return nil
	}
	pid := r.Delegation.RunnerPID
	if a.Runners.Alive(pid) {
		stopped, err := a.Runners.Stop(r.Key, pid)
		if err != nil {
			return fmt.Errorf("stop runner %d: %w", pid, err)
		}
		if !stopped {
			return fmt.Errorf("runner %d has not exited yet", pid)
		}
	}
	_, err := a.update(ctx, r.Key, func(cur *task.Record) (*task.Record, error) {
		if cur != nil && cur.Delegation != nil && cur.Delegation.RunnerPID == pid {
			cur.Delegation.RunnerPID = 0
		}
		return cur, nil
	})
	return err
}

// closePR ends a task whose PR was closed without merging. The agent did not fail, so
// the outcome says only what happened and nothing notifies the developer, who closed
// the PR. The branch and worktree stay for a later start. The runner's pid stays too:
// stopRun needs it, and clears it once the runner is gone.
func closePR(rec *task.Record, now time.Time) string {
	d := rec.Delegation
	rec.State = task.PRClosed
	summary := fmt.Sprintf("PR #%d closed without merging; the branch and worktree are kept", d.PR)
	rec.LastOutcome = &task.Outcome{Status: "pr_closed", Summary: summary, At: now}
	return summary
}

// reopen returns a PR_CLOSED task to delegated work once its PR is open again. The
// review it already has decides where: a clean review of this head with finished CI
// hands the task to the developer, and anything else leaves it under review, which is
// where the gate picks it up again. The review must name the head GitHub reports now.
// A review from before heads were recorded (D63) has none, and cannot be shown to
// cover the reopened PR, so it does not hand anything over.
func reopen(rec *task.Record, pr PullRequest, ci task.CIStatus, now time.Time) string {
	d := rec.Delegation
	if pr.HeadSHA != "" {
		d.PRHead = pr.HeadSHA
	}
	rec.CI = &ci
	latest := rec.LatestReview()
	reviewed := latest != nil && latest.Clean && latest.PR == d.PR && latest.Head != "" && latest.Head == pr.HeadSHA
	rec.State = task.Reviewing
	if reviewed && ci.State != task.CIPending {
		rec.State = task.ReadyForHuman
	}
	summary := fmt.Sprintf("PR #%d reopened; it is back under review", d.PR)
	if rec.State == task.ReadyForHuman {
		summary = fmt.Sprintf("PR #%d reopened; its review is clean and CI has finished", d.PR)
	}
	rec.LastOutcome = &task.Outcome{Status: "pr_reopened", Summary: summary, At: now}
	return summary
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
	// A closed PR never reaches here; gate ends that task before it gates on CI.
	if pr.State == "merged" {
		rec.State = task.Done
		return outcome("merged", fmt.Sprintf("PR #%d merged", d.PR))
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
