package app

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

type InboxEntry struct {
	Item    InboxItem
	Changes []string
}

// InboxReport lists each item once, in the section with the highest priority:
// NEEDS DECISION, then READY FOR YOU, then CHANGED, then NEW.
type InboxReport struct {
	New           []InboxEntry
	Changed       []InboxEntry
	NeedsDecision []*task.Record
	Ready         []*task.Record
	Unchanged     int
}

type App struct {
	Store        TaskStore
	GitHub       GitHubClient
	Worktrees    WorktreeManager
	Agents       map[task.Agent]AgentAdapter
	Runners      Runners
	WorktreesDir string
	Now          func() time.Time
}

// Inbox fetches GitHub state, reports what changed since the last run, and records
// the new state as seen. Only records that changed are rewritten.
func (a App) Inbox(ctx context.Context) (InboxReport, error) {
	recs, err := a.listReaped(ctx)
	if err != nil {
		return InboxReport{}, err
	}
	items, err := a.GitHub.InboxItems(ctx)
	if err != nil {
		return InboxReport{}, err
	}
	tracked, err := a.fetchTracked(ctx, recs, items)
	if err != nil {
		return InboxReport{}, err
	}
	report, dirty := Reconcile(append(items, tracked...), recs, a.Now())
	for _, seen := range dirty {
		if _, err := a.Store.Update(ctx, seen.Key, func(cur *task.Record) (*task.Record, error) {
			return mergeObserved(cur, seen), nil
		}); err != nil {
			return InboxReport{}, err
		}
	}
	return report, nil
}

// mergeObserved copies only the GitHub-derived fields onto the current file, so a
// runner that saved since List keeps its state, session, and outcome.
func mergeObserved(cur, seen *task.Record) *task.Record {
	if cur == nil {
		return seen
	}
	cur.Title, cur.URL, cur.Kind = seen.Title, seen.URL, seen.Kind
	cur.GitHub, cur.LastGitHubSync = seen.GitHub, seen.LastGitHubSync
	if cur.State == task.Discovered && seen.State == task.Done {
		cur.State = task.Done
	}
	return cur
}

// fetchTracked re-fetches delegated tasks that no search returned, so a closed issue
// or merged PR is still noticed after it leaves the "assigned to me" results.
func (a App) fetchTracked(ctx context.Context, recs []*task.Record, found []InboxItem) ([]InboxItem, error) {
	inSearch := map[task.Key]bool{}
	for _, it := range found {
		inSearch[it.Key] = true
	}
	var keys []task.Key
	for _, r := range recs {
		if r.Delegation != nil && r.State != task.Done && !inSearch[r.Key] {
			keys = append(keys, r.Key)
		}
	}
	out := make([]InboxItem, len(keys))
	errs := make([]error, len(keys))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, k := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i], errs[i] = a.GitHub.GetItem(ctx, k)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Reconcile compares current GitHub items with stored records. It returns the report
// and the records that must be saved (new, or with a changed snapshot). It is pure.
func Reconcile(items []InboxItem, recs []*task.Record, now time.Time) (InboxReport, []*task.Record) {
	byKey := map[task.Key]*task.Record{}
	for _, r := range recs {
		byKey[r.Key] = r
	}
	var report InboxReport
	var dirty []*task.Record
	for _, it := range items {
		rec := byKey[it.Key]
		if rec == nil {
			rec = discovered(it, now)
			byKey[it.Key] = rec
			dirty = append(dirty, rec)
			if !needsHuman(rec) {
				report.New = append(report.New, InboxEntry{Item: it, Changes: reasonText(it.Reasons)})
			}
			continue
		}
		changes := describeChanges(rec.GitHub, it)
		if len(changes) == 0 {
			if !needsHuman(rec) {
				report.Unchanged++
			}
			continue
		}
		observe(rec, it, now)
		dirty = append(dirty, rec)
		if !needsHuman(rec) {
			report.Changed = append(report.Changed, InboxEntry{Item: it, Changes: changes})
		}
	}
	for _, r := range recs {
		switch r.State {
		case task.NeedsDecision:
			report.NeedsDecision = append(report.NeedsDecision, r)
		case task.ReadyForHuman:
			report.Ready = append(report.Ready, r)
		}
	}
	return report, dirty
}

func needsHuman(r *task.Record) bool {
	return r.State == task.NeedsDecision || r.State == task.ReadyForHuman
}

func discovered(it InboxItem, now time.Time) *task.Record {
	rec := &task.Record{
		Key:   it.Key,
		Kind:  it.Kind,
		State: task.Discovered,
		Body:  fmt.Sprintf("# #%d %s\n\n## Notes\n\n", it.Key.Number, it.Title),
	}
	observe(rec, it, now)
	return rec
}

// observe records it as the seen GitHub state. Items fetched only because they are
// tracked keep their earlier search reasons.
func observe(rec *task.Record, it InboxItem, now time.Time) {
	reasons := it.Reasons
	if slices.Equal(reasons, []task.InboxReason{task.Tracked}) && rec.GitHub != nil {
		reasons = rec.GitHub.Reasons
	}
	rec.Title, rec.URL, rec.Kind = it.Title, it.URL, it.Kind
	rec.GitHub = &task.Snapshot{UpdatedAt: it.UpdatedAt, State: it.State, Reasons: reasons}
	rec.LastGitHubSync = now
	if rec.State == task.Discovered && it.State != "open" {
		rec.State = task.Done
	}
}

func describeChanges(seen *task.Snapshot, it InboxItem) []string {
	if seen == nil {
		return []string{"now tracked"}
	}
	var changes []string
	if it.State != seen.State {
		switch it.State {
		case "open":
			changes = append(changes, "reopened")
		default:
			changes = append(changes, it.State)
		}
	}
	var added []task.InboxReason
	for _, r := range it.Reasons {
		if r != task.Tracked && !slices.Contains(seen.Reasons, r) {
			added = append(added, r)
		}
	}
	changes = append(changes, reasonText(added)...)
	if len(changes) == 0 && it.UpdatedAt.After(seen.UpdatedAt) {
		changes = append(changes, "new activity")
	}
	return changes
}

func reasonText(reasons []task.InboxReason) []string {
	var out []string
	for _, r := range reasons {
		switch r {
		case task.Assigned:
			out = append(out, "assigned to you")
		case task.Mentioned:
			out = append(out, "mentions you")
		case task.ReviewRequested:
			out = append(out, "review requested")
		}
	}
	return out
}
