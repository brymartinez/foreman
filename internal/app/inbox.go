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
	New     []InboxEntry
	Changed []InboxEntry
	// Current contains every item in the current refresh, including unchanged items.
	// It is used by the interactive picker; the report sections retain their diff semantics.
	Current       []InboxEntry
	NeedsDecision []*task.Record
	Ready         []*task.Record
	Unchanged     int
	// Truncated lists searches that hit the result limit.
	Truncated []string
}

type App struct {
	Store                TaskStore
	GitHub               GitHubClient
	Repositories         RepositoryManager
	Worktrees            WorktreeManager
	Agents               map[task.Agent]AgentAdapter
	Runners              Runners
	Stages               StageInstructionResolver
	WorktreesDir         string
	RepositoriesDir      string
	ReviewRounds         int // 0 means DefaultReviewRounds
	ImplementerBudgetUSD float64
	ReviewerBudgetUSD    float64
	Notifier             Notifier
	// CleanOnDone removes a DONE task's worktree when inbox runs (D42).
	CleanOnDone bool
	// DeleteMergedBranches deletes the local branch with the worktree once its PR is merged.
	DeleteMergedBranches bool
	IgnoredRepositories  []task.Repo
	Now                  func() time.Time
}

// Inbox fetches GitHub state, reports what changed since the last run, and records
// the new state as seen. Only records that changed are rewritten.
func (a App) Inbox(ctx context.Context) (InboxReport, error) {
	recs, err := a.listReaped(ctx)
	if err != nil {
		return InboxReport{}, err
	}
	notes, err := a.gate(ctx, recs)
	if err != nil {
		return InboxReport{}, err
	}
	if a.CleanOnDone {
		a.cleanDone(ctx, recs, notes)
	}
	search, err := a.GitHub.InboxItems(ctx)
	if err != nil {
		return InboxReport{}, err
	}
	items := filterInboxItems(search.Items, a.IgnoredRepositories)
	tracked, err := a.fetchTracked(ctx, recs, items)
	if err != nil {
		return InboxReport{}, err
	}
	report, dirty := Reconcile(append(items, tracked...), recs, notes, len(search.Truncated) == 0, a.Now())
	report.Truncated = search.Truncated
	for _, seen := range dirty {
		if _, err := a.update(ctx, seen.Key, func(cur *task.Record) (*task.Record, error) {
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
	if seen.State == task.Done && !running(cur.State) {
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
		if r.Delegation != nil && r.State != task.Done && !inSearch[r.Key] && !containsRepo(a.IgnoredRepositories, r.Repo) {
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

func filterInboxItems(items []InboxItem, ignored []task.Repo) []InboxItem {
	if len(ignored) == 0 {
		return items
	}
	filtered := make([]InboxItem, 0, len(items))
	for _, item := range items {
		if !containsRepo(ignored, item.Key.Repo) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func containsRepo(repos []task.Repo, want task.Repo) bool {
	return slices.Contains(repos, want)
}

// Reconcile compares current GitHub items with stored records. notes are the readiness
// gate's changes, reported with the item. complete says every search returned all its
// results, so a reason missing from items was lost. It returns the report and the
// records that must be saved (new, or with a changed snapshot). It is pure.
func Reconcile(items []InboxItem, recs []*task.Record, notes map[task.Key][]string, complete bool, now time.Time) (InboxReport, []*task.Record) {
	byKey := map[task.Key]*task.Record{}
	for _, r := range recs {
		byKey[r.Key] = r
	}
	var report InboxReport
	var dirty []*task.Record
	reported := map[task.Key]bool{}
	for _, it := range items {
		reported[it.Key] = true
		rec := byKey[it.Key]
		if rec == nil {
			rec = discovered(it, now)
			byKey[it.Key] = rec
			dirty = append(dirty, rec)
			report.Current = append(report.Current, InboxEntry{Item: it, Changes: reasonText(it.Reasons)})
			if !needsHuman(rec) {
				report.New = append(report.New, InboxEntry{Item: it, Changes: reasonText(it.Reasons)})
			}
			continue
		}
		changes := describeChanges(rec.GitHub, it, complete)
		current := changes
		if len(current) == 0 {
			current = []string{"no change"}
		}
		report.Current = append(report.Current, InboxEntry{Item: it, Changes: current})
		if len(changes) > 0 {
			observe(rec, it, complete, now)
			dirty = append(dirty, rec)
		}
		changes = append(changes, notes[it.Key]...)
		switch {
		case needsHuman(rec):
		case len(changes) == 0:
			report.Unchanged++
		default:
			report.Changed = append(report.Changed, InboxEntry{Item: it, Changes: changes})
		}
	}
	for _, r := range recs {
		if n := notes[r.Key]; len(n) > 0 && !reported[r.Key] && !needsHuman(r) {
			report.Changed = append(report.Changed, InboxEntry{Item: InboxItem{Key: r.Key, Kind: r.Kind, Title: r.Title, URL: r.URL}, Changes: n})
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
	observe(rec, it, true, now)
	return rec
}

// observe records it as the seen GitHub state.
func observe(rec *task.Record, it InboxItem, complete bool, now time.Time) {
	reasons := it.Reasons
	if slices.Equal(reasons, []task.InboxReason{task.Tracked}) {
		// A tracked fetch means no search returned the item. The searches only return
		// open items, so that proves an open item lost its reasons, when every search
		// was complete.
		reasons = nil
		if (!complete || it.State != "open") && rec.GitHub != nil {
			reasons = rec.GitHub.Reasons
		}
	}
	rec.Title, rec.URL, rec.Kind = it.Title, it.URL, it.Kind
	rec.GitHub = &task.Snapshot{UpdatedAt: it.UpdatedAt, State: it.State, Reasons: reasons}
	rec.LastGitHubSync = now
	// A closed issue or merged PR ends the task unless a runner is still driving it;
	// the gate ends those when their PR is merged or closed.
	if it.State != "open" && !running(rec.State) {
		rec.State = task.Done
	}
}

func describeChanges(seen *task.Snapshot, it InboxItem, complete bool) []string {
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
	if complete && it.State == "open" {
		for _, r := range seen.Reasons {
			if !slices.Contains(it.Reasons, r) {
				changes = append(changes, lostText(r))
			}
		}
	}
	if len(changes) == 0 && it.UpdatedAt.After(seen.UpdatedAt) {
		changes = append(changes, "new activity")
	}
	return changes
}

func lostText(r task.InboxReason) string {
	switch r {
	case task.Assigned:
		return "no longer assigned to you"
	case task.Mentioned:
		return "no longer mentions you"
	case task.ReviewRequested:
		return "review no longer requested"
	}
	return "no longer " + string(r)
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
