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
	task.Failed:        2,
	task.Reviewing:     3,
	task.Working:       4,
}

// Status returns delegated work that is not finished, most urgent first.
func (a App) Status(ctx context.Context) ([]*task.Record, []error, error) {
	recs, err := a.listReaped(ctx)
	if err != nil {
		return nil, nil, err
	}
	_, warnings, err := a.gate(ctx, recs)
	if err != nil {
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

// listReaped lists tasks and marks running tasks whose runner is gone as FAILED.
// A runner clears its pid when it records an outcome, so a set pid on a dead
// process means it crashed or was killed.
func (a App) listReaped(ctx context.Context) ([]*task.Record, error) {
	recs, err := a.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	for i, r := range recs {
		if !running(r.State) || r.Delegation == nil || r.Delegation.RunnerPID == 0 || a.Runners.Alive(r.Delegation.RunnerPID) {
			continue
		}
		updated, err := a.update(ctx, r.Key, func(cur *task.Record) (*task.Record, error) {
			d := cur.Delegation
			if !running(cur.State) || d == nil || d.RunnerPID != r.Delegation.RunnerPID {
				return cur, nil
			}
			cur.State = task.Failed
			cur.LastOutcome = &task.Outcome{Status: "failed", Summary: fmt.Sprintf("runner (pid %d) exited without recording an outcome; see %s", d.RunnerPID, d.RunnerLog), At: a.Now()}
			d.RunnerPID = 0
			return cur, nil
		})
		if err != nil {
			return nil, err
		}
		recs[i] = updated
	}
	return recs, nil
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
