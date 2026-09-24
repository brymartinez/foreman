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
func (a App) Status(ctx context.Context) ([]*task.Record, error) {
	recs, err := a.listReaped(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := a.gate(ctx, recs); err != nil {
		return nil, err
	}
	var active []*task.Record
	for _, r := range recs {
		if _, ok := attention[r.State]; ok && r.Delegation != nil {
			active = append(active, r)
		}
	}
	sort.SliceStable(active, func(i, j int) bool { return attention[active[i].State] < attention[active[j].State] })
	return active, nil
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
		updated, err := a.Store.Update(ctx, r.Key, func(cur *task.Record) (*task.Record, error) {
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
