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
	recs, err := a.Store.List(ctx)
	if err != nil {
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
