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
// check results and hands failures to the developer instead of fixing them.
// It returns what changed, per task, and the tasks whose PR state it read. A caller
// that fails a dead runner must check the second map first: reaping a task this pass
// could not read would record a PR the developer closed as a failed run.
func (a App) gate(ctx context.Context, recs []*task.Record) (map[task.Key][]string, map[task.Key]bool, []error, error) {
	notes := map[task.Key][]string{}
	checked := map[task.Key]bool{}
	var warnings []error
	for i, r := range recs {
		if !watched(r) {
			continue
		}
		pr, err := a.GitHub.GetPullRequest(ctx, task.Key{Repo: r.Repo, Number: r.Delegation.PR})
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, nil, ctx.Err()
			}
			warnings = append(warnings, fmt.Errorf("refresh %s#%d PR: %w", r.Repo, r.Number, err))
			continue
		}
		checked[r.Key] = true
		if pr.State == "closed" {
			updated, note, err := a.endClosed(ctx, r)
			if err != nil {
				return nil, nil, nil, err
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
		if r.State != task.PRClosed && !(pr.State == "merged" && r.Delegation.RunnerPID == 0) && !gated(r) && !rereview(r) {
			// A running task keeps its state until the runner stops.
			continue
		}
		if r.State == task.PRClosed && r.Delegation.RunnerPID != 0 {
			// The task kept its runner because an earlier stop did not take. Finish
			// that stop before handing the task back: PR_CLOSED is what holds the old
			// runner off, and reopening lifts it. A stop that fails again leaves the
			// task closed, and the next refresh comes back here.
			if err := a.stopRun(ctx, r); err != nil {
				warnings = append(warnings, fmt.Errorf("stop the runner of %s#%d before PR #%d reopens: %w", r.Repo, r.Number, r.Delegation.PR, err))
				continue
			}
		}
		if r.State == task.PRClosed && !r.Delegation.WorktreeRemovedAt.IsZero() {
			// Cleanup removed the worktree and kept the branch. A reopened PR needs the
			// worktree to be worked on, and a merged one needs it back so cleanup deletes
			// the branch along with it. A failed restore only warns: the task still
			// follows its PR, a merged one keeps its branch, and a launch tries the
			// restore again before any runner starts.
			if err := a.restoreWorktree(ctx, r.Key); err != nil {
				warnings = append(warnings, fmt.Errorf("restore the worktree of %s#%d as PR #%d leaves PR_CLOSED: %w", r.Repo, r.Number, r.Delegation.PR, err))
			}
		}
		// The PRD is a review's acceptance criteria, so an edit to it while the task
		// waits for CI, or while its PR was closed, needs new reviews, like an edited
		// description. A PRD that cannot
		// be loaded fails the task, as it would stop the review. Neither needs CI, so the
		// PRD is read first.
		var prd string
		var prdErr error
		checkPRD := pr.State == "open" && (r.State == task.WaitingForCI || r.State == task.PRClosed) && r.Delegation.ClaimVerification
		if checkPRD {
			var text string
			if text, prdErr = a.reviewPRD(ctx, r); prdErr == nil {
				prd = task.Digest(text)
			} else if ctx.Err() != nil {
				return nil, nil, nil, ctx.Err()
			}
		}
		var ci task.CIStatus
		ciRead := true
		if pr.State == "open" {
			if ci, err = a.GitHub.CIStatus(ctx, pr.Key); err != nil {
				if ctx.Err() != nil {
					return nil, nil, nil, ctx.Err()
				}
				warnings = append(warnings, fmt.Errorf("refresh %s#%d CI: %w", r.Repo, r.Number, err))
				if !checkPRD || (prdErr == nil && prd == r.Delegation.PRDDigest) {
					continue
				}
				// The PRD still decides the task; nothing that needs CI runs.
				ciRead = false
			}
			ci.CheckedAt = a.Now()
		}
		var note, token string
		updated, err := a.update(ctx, r.Key, func(cur *task.Record) (*task.Record, error) {
			if cur.Delegation == nil || cur.Delegation.PR != r.Delegation.PR {
				return cur, nil
			}
			// The state is read again under the lock, so a runner that saved since the
			// list is not overwritten by what this refresh expected to find.
			switch {
			case pr.State == "merged" && cur.Delegation.RunnerPID == 0:
				note = advance(cur, pr, ci, a.Now())
			case checkPRD && prdErr != nil && cur.State == task.WaitingForCI && gated(cur):
				failTask(cur, fmt.Sprintf("PR #%d waited for CI, but its PRD could not be loaded: %v", cur.Delegation.PR, prdErr), a.Now())
			case checkPRD && cur.State == task.WaitingForCI && gated(cur) && cur.Delegation.PRDDigest != prd:
				note = restartForPRD(cur, prd, a.Now())
			case !ciRead:
			case cur.State == task.PRClosed:
				if checkPRD && prdErr == nil {
					// The stored digest decides whether the clean review still covers the PR.
					cur.Delegation.PRDDigest = prd
				}
				note = reopen(cur, pr, ci, a.Now())
				if checkPRD && prdErr != nil {
					note = fmt.Sprintf("PR #%d reopened, but its PRD could not be loaded: %v", cur.Delegation.PR, prdErr)
					failTask(cur, note, a.Now())
				}
			case gated(cur):
				note = advance(cur, pr, ci, a.Now())
			case rereview(cur):
				note = restartReview(cur, pr, a.Now())
			}
			// A PR that had a clean review is reviewed again by a runner the gate starts.
			// One without a clean review shows the stage it needs and waits for a start.
			if rereview(cur) {
				var err error
				if token, err = a.claimLaunch(cur); err != nil {
					return nil, err
				}
			}
			return cur, nil
		})
		if err != nil {
			return nil, nil, nil, err
		}
		if token != "" {
			if updated, err = a.launch(ctx, r.Key, token); err != nil {
				return nil, nil, nil, err
			}
		}
		if note != "" {
			notes[r.Key] = append(notes[r.Key], note)
		}
		// A pull request the agents are done with is the developer's to comment on, so
		// this is where feedback arrives. It runs after advance, so a task
		// handed over by this same refresh is checked in the same pass.
		if pr.State == "open" && !a.ReplyToPRFeedback && updated.State == task.Reviewed {
			updated, err = a.update(ctx, r.Key, existing(func(cur *task.Record) {
				if cur.State == task.Reviewed {
					cur.State = task.ReadyForHuman
					cur.Feedback = nil
				}
			}))
			if err != nil {
				return nil, nil, nil, err
			}
		}
		if pr.State == "open" && a.ReplyToPRFeedback && a.handsOver(updated) {
			withFeedback, feedbackNote, err := a.feedback(ctx, updated)
			if err != nil {
				warnings = append(warnings, err)
			} else {
				updated = withFeedback
				if feedbackNote != "" {
					notes[r.Key] = append(notes[r.Key], feedbackNote)
				}
			}
		}
		// Comments come first: a task they moved to REVIEWED is repaired once it is
		// handed over again.
		if repaired, conflictNote, err := a.conflict(ctx, updated, pr); err != nil {
			warnings = append(warnings, err)
		} else {
			updated = repaired
			if conflictNote != "" {
				notes[r.Key] = append(notes[r.Key], conflictNote)
			}
		}
		recs[i] = updated
	}
	return notes, checked, warnings, nil
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
// the developer about a failure that did not happen.
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
// the PR. The branch stays for a later start; cleanup may remove the worktree, and a
// later launch recreates it. The runner's pid stays too:
// stopRun needs it, and clears it once the runner is gone.
func closePR(rec *task.Record, now time.Time) string {
	d := rec.Delegation
	rec.State = task.PRClosed
	summary := fmt.Sprintf("PR #%d closed without merging; the branch is kept", d.PR)
	rec.LastOutcome = &task.Outcome{Status: "pr_closed", Summary: summary, At: now}
	return summary
}

// reopen returns a PR_CLOSED task to delegated work once its PR is open again. The
// review it already has decides where: a clean review of this head and description
// waits for CI, or hands the task to the developer once CI finished. Anything else is
// the stage the change needs; with a clean review of an earlier head or description,
// the gate starts a runner for that review. The review must name the head GitHub
// reports now.
// A review from before heads were recorded has none, and cannot be shown to
// cover the reopened PR, so it does not hand anything over.
func reopen(rec *task.Record, pr PullRequest, ci task.CIStatus, now time.Time) string {
	d := rec.Delegation
	if pr.HeadSHA != "" {
		d.PRHead = pr.HeadSHA
	}
	rec.CI = &ci
	latest := rec.LatestCompeting()
	reviewed := latest != nil && latest.Clean && latest.PR == d.PR && latest.Head != "" && latest.Head == pr.HeadSHA && describedAsReviewed(rec, pr) && rec.ClaimsCovered(latest)
	// The stage follows the PR as GitHub reports it now: a clean review of its head
	// and description waits for CI, and anything else is the stage the change needs.
	if !describedAsReviewed(rec, pr) {
		d.PRDigest = task.ProposalDigest(pr.Title, pr.Body)
	}
	rec.State = rec.NextStage()
	if reviewed {
		rec.State = task.WaitingForCI
	}
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

// describedAsReviewed reports whether the PR's title and description are the ones its
// latest competing review read. A review from before descriptions were recorded has
// no digest and covers its head alone.
func describedAsReviewed(rec *task.Record, pr PullRequest) bool {
	latest := rec.LatestCompeting()
	return latest == nil || latest.Proposal == "" || latest.Proposal == task.ProposalDigest(pr.Title, pr.Body)
}

// gated tasks wait on GitHub alone: the review is clean, or the developer has the PR.
func gated(r *task.Record) bool {
	d := r.Delegation
	if d == nil || d.PR == 0 || d.RunnerPID != 0 {
		return false
	}
	switch r.State {
	case task.ReadyForHuman, task.Reviewed:
		return true
	case task.WaitingForCI:
		latest := r.LatestCompeting()
		return latest != nil && latest.Clean && latest.PR == d.PR
	}
	return false
}

// rereview tasks are in a review stage with no runner, while their PR has a clean
// competing review of an earlier head or description: a reopened PR, or a task file
// from before the stages. The gate starts the runner that reviews the change, as it
// does for new commits on a waiting PR.
func rereview(r *task.Record) bool {
	d := r.Delegation
	if d == nil || d.PR == 0 || d.RunnerPID != 0 || launching(r) || !r.State.Reviewing() {
		return false
	}
	latest := r.LatestCompeting()
	return latest != nil && latest.Clean && latest.PR == d.PR
}

// restartReview stores the head and description the PR has now, so the runner the
// gate starts reviews them instead of taking a turn on a stale record. A PR its clean
// review covers after all waits for CI.
func restartReview(rec *task.Record, pr PullRequest, now time.Time) string {
	d := rec.Delegation
	if pr.HeadSHA != "" {
		d.PRHead = pr.HeadSHA
	}
	d.PRDigest, rec.CI = task.ProposalDigest(pr.Title, pr.Body), nil
	if reviewCovers(rec, d.PRHead, d.PRDigest) {
		rec.State = task.WaitingForCI
		return ""
	}
	rec.State = rec.NextStage()
	summary := fmt.Sprintf("PR #%d changed since its clean review; it is being reviewed again", d.PR)
	rec.LastOutcome = &task.Outcome{Status: "re_review", Summary: summary, At: now}
	return summary
}

// restartForPRD stores the digest of the PRD the issue holds now, so the runner the
// gate starts reviews the change against it.
func restartForPRD(rec *task.Record, prd string, now time.Time) string {
	d := rec.Delegation
	d.PRDDigest = prd
	rec.State, rec.CI = rec.NextStage(), nil
	summary := fmt.Sprintf("the PRD on the issue changed since PR #%d's clean review; it is being reviewed again", d.PR)
	rec.LastOutcome = &task.Outcome{Status: "re_review", Summary: summary, At: now}
	return summary
}

// advance applies the PR state and CI result to a gated task, and returns the
// inbox note for the change, if the developer should hear about it. A task that is
// already READY_FOR_HUMAN or REVIEWED only has its CI refreshed, so the view stays
// current; where it goes next is the developer's call or the feedback check's.
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
		if rec.State == task.WaitingForCI && rec.CommitsSinceReview() {
			// Commits landed after the clean review: review them before handing over.
			rec.State, rec.CI = rec.NextStage(), nil
			return outcome("re_review", fmt.Sprintf("PR #%d has new commits since its clean review; %s is reviewing them", d.PR, d.Reviewer))
		}
	}
	if rec.State == task.WaitingForCI && !describedAsReviewed(rec, pr) {
		// The description is the reviews' requirements, so an edit needs a new
		// self-review and competing review before handing over.
		d.PRDigest = task.ProposalDigest(pr.Title, pr.Body)
		rec.State, rec.CI = rec.NextStage(), nil
		return outcome("re_review", fmt.Sprintf("PR #%d has a new description since its clean review; it is being reviewed again", d.PR))
	}
	prev := rec.CI
	rec.CI = &ci
	if rec.State == task.ReadyForHuman || rec.State == task.Reviewed {
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
