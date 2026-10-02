package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

// attention orders status output: what needs the user first, running work last.
var attention = map[task.State]int{
	task.NeedsDecision: 0,
	task.ReadyForHuman: 1,
	task.Reviewed:      2,
	task.Failed:        3,
	task.Reviewing:     4,
	task.Working:       5,
}

// Status returns delegated work that is not finished, most urgent first.
func (a App) Status(ctx context.Context) ([]*task.Record, []error, error) {
	recs, err := a.Store.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	dead := a.deadRunners(recs)
	_, checked, warnings, err := a.gate(ctx, recs)
	if err != nil {
		return nil, nil, err
	}
	if err := a.reap(ctx, recs, reapable(recs, dead, checked)); err != nil {
		return nil, nil, err
	}
	return activeTasks(recs), warnings, nil
}

// Tasks returns the same work as Status, from the task files alone. It skips the
// readiness gate, so it makes no GitHub call and the dashboard can re-read it every
// second (D95).
func (a App) Tasks(ctx context.Context) ([]*task.Record, error) {
	recs, err := a.listReaped(ctx)
	if err != nil {
		return nil, err
	}
	return activeTasks(recs), nil
}

func activeTasks(recs []*task.Record) []*task.Record {
	var active []*task.Record
	for _, r := range recs {
		if _, ok := attention[r.State]; ok && r.Delegation != nil {
			active = append(active, r)
		}
	}
	sort.SliceStable(active, func(i, j int) bool { return attention[active[i].State] < attention[active[j].State] })
	return active
}

// Find resolves an issue number to one stored task. repo may be empty; the number
// must then be unique across tracked repos.
func (a App) Find(ctx context.Context, repo task.Repo, number int) (*task.Record, error) {
	if repo != "" {
		rec, err := a.Store.Get(ctx, task.Key{Repo: repo, Number: number})
		if err == nil && rec == nil {
			return nil, fmt.Errorf("%s#%d is not tracked; run foreman inbox first", repo, number)
		}
		return rec, err
	}
	recs, err := a.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	var matches []*task.Record
	for _, r := range recs {
		if r.Number == number {
			matches = append(matches, r)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("#%d is not tracked; run foreman inbox first", number)
	case 1:
		return matches[0], nil
	}
	refs := make([]string, len(matches))
	for i, m := range matches {
		refs[i] = fmt.Sprintf("%s#%d", m.Repo, m.Number)
	}
	return nil, fmt.Errorf("#%d is ambiguous; use one of: %s", number, strings.Join(refs, ", "))
}

// listReaped lists tasks and reaps the ones it takes no GitHub call to judge: a task
// with a PR is left to a caller that has read it. It makes no call itself, so the
// dashboard can call it every second (D95).
func (a App) listReaped(ctx context.Context) ([]*task.Record, error) {
	recs, err := a.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	var local []deadRunner
	for _, dr := range a.deadRunners(recs) {
		if dr.pr == 0 {
			local = append(local, dr)
		}
	}
	return recs, a.reap(ctx, recs, local)
}

// deadRunner is a task in a list whose runner is gone: where it sits in the list, the
// pid that was gone, and the PR the task had. reap keeps the pid so it fails only the
// run deadRunners judged, not whatever the task holds by the time it writes. The PR is
// what reapable needs to tell a local judgement from one the gate has to make.
type deadRunner struct {
	index int
	pid   int
	pr    int
}

// reapable drops the dead runners whose task the gate did not read this pass. A task
// with a PR is only safe to fail once the gate has seen that PR: a PR the developer
// closed ends the task instead of failing it (D107), and a GitHub call that fails
// leaves foreman unable to tell the two apart. A failure notification cannot be taken
// back, so an unread task keeps its state and the next refresh judges it (D113).
func reapable(recs []*task.Record, dead []deadRunner, checked map[task.Key]bool) []deadRunner {
	var out []deadRunner
	for _, dr := range dead {
		if dr.pr == 0 || checked[recs[dr.index].Key] {
			out = append(out, dr)
		}
	}
	return out
}

// deadRunners finds the tasks in recs that a runner should be driving but no longer
// is. A runner clears its pid when it records an outcome, so a set pid on a dead
// process means it crashed or was killed.
//
// It is separate from reap because the two happen either side of the readiness gate.
// The gate reads each task's PR first, so a dead runner on a PR the developer closed
// ends the task instead of failing it (D107), and a failure notification cannot be
// taken back once it is sent. Reading the pids first also keeps a runner the gate
// launches in the same pass out of the set.
func (a App) deadRunners(recs []*task.Record) []deadRunner {
	var dead []deadRunner
	for i, r := range recs {
		d := r.Delegation
		if running(r.State) && d != nil && d.RunnerPID != 0 && !a.Runners.Alive(d.RunnerPID) {
			dead = append(dead, deadRunner{index: i, pid: d.RunnerPID, pr: d.PR})
		}
	}
	return dead
}

// reap marks the tasks deadRunners found as FAILED, unless they have moved on since. A
// runner that died during a merge conflict repair leaves a decision instead (D132).
func (a App) reap(ctx context.Context, recs []*task.Record, dead []deadRunner) error {
	for _, dr := range dead {
		updated, err := a.update(ctx, recs[dr.index].Key, func(cur *task.Record) (*task.Record, error) {
			d := cur.Delegation
			if !running(cur.State) || d == nil || d.RunnerPID != dr.pid {
				return cur, nil
			}
			summary := fmt.Sprintf("runner (pid %d) exited without recording an outcome; see %s", d.RunnerPID, d.RunnerLog)
			d.RunnerPID = 0
			if d.ConflictPending {
				failRepair(cur, summary, a.Now())
				return cur, nil
			}
			cur.State = task.Failed
			cur.LastOutcome = &task.Outcome{Status: "failed", Summary: summary, At: a.Now()}
			return cur, nil
		})
		if err != nil {
			return err
		}
		recs[dr.index] = updated
	}
	return nil
}

// running states are the ones a runner drives: implementing, or reviewing and fixing.
func running(s task.State) bool { return s == task.Working || s == task.Reviewing }

// liveRunner reports a task whose runner process is still going. That runner owns the
// worktree and the task file until it exits, whatever state the task is in: a
// PR_CLOSED task keeps its pid until the stop succeeds, so it can have one too (D107).
func (a App) liveRunner(r *task.Record) bool {
	d := r.Delegation
	return d != nil && d.RunnerPID > 0 && a.Runners.Alive(d.RunnerPID)
}

// Cancel stops a running task's runner, which stops its agent. The runner records
// the stop as FAILED with a cancelled outcome. The branch, worktree, and PR are
// kept, so start picks the task up again.
func (a App) Cancel(ctx context.Context, rec *task.Record) (*task.Record, error) {
	d := rec.Delegation
	if !running(rec.State) || d == nil || d.RunnerPID == 0 {
		return nil, fmt.Errorf("%s#%d is %s with no runner; nothing to cancel", rec.Repo, rec.Number, rec.State)
	}
	pid := d.RunnerPID
	stopped, err := a.Runners.Stop(rec.Key, pid)
	if err != nil {
		return nil, fmt.Errorf("stop runner %d: %w", pid, err)
	}
	if !stopped {
		return nil, fmt.Errorf("runner %d has not exited yet; check again with foreman status", pid)
	}
	// A runner that exited without recording its stop still holds its pid.
	return a.update(ctx, rec.Key, existing(func(cur *task.Record) {
		if running(cur.State) && cur.Delegation.RunnerPID == pid {
			cur.State = task.Failed
			cur.LastOutcome = &task.Outcome{Status: Cancelled, Summary: fmt.Sprintf("run cancelled; runner %d exited without recording an outcome", pid), At: a.Now()}
			cur.Delegation.RunnerPID = 0
		}
	}))
}
