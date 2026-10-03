package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/brymartinez/foreman/internal/routing"
	"github.com/brymartinez/foreman/internal/task"
)

type InboxEntry struct {
	Item    InboxItem
	Changes []string
	// Delegated says foreman has a run for this item, and State is that run's state.
	// Together they are what keeps work already in hand out of INBOX.
	Delegated bool
	State     task.State
}

// InboxReport lists each item once, in the section with the highest priority:
// NEEDS DECISION, then REVIEWED, then READY FOR YOU, then CHANGED, then NEW.
type InboxReport struct {
	New     []InboxEntry
	Changed []InboxEntry
	// Current contains every item in the current refresh, including unchanged items.
	// It is used by the interactive picker; the report sections retain their diff semantics.
	Current       []InboxEntry
	NeedsDecision []*task.Record
	// Reviewed holds the tasks whose PR has comments the agent has not answered.
	Reviewed       []*task.Record
	Ready          []*task.Record
	WaitingChanges map[task.Key][]string
	Unchanged      int
	// Truncated lists searches that hit the result limit.
	Truncated []string
	Warnings  []error
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
	ReviewRounds         int // reviewer passes per review; 0 means DefaultReviewRounds
	FixRounds            int // fix and re-review cycles before asking; 0 means DefaultFixRounds
	ImplementerBudgetUSD float64
	ReviewerBudgetUSD    float64
	Notifier             Notifier
	// CleanOnDone removes a DONE task's worktree when inbox runs.
	CleanOnDone bool
	// DeleteMergedBranches deletes the local branch with the worktree once its PR is merged.
	DeleteMergedBranches bool
	// IncludedRepositories, when nonempty, limits the inbox to these repositories
	// and overrides ExcludedRepositories.
	IncludedRepositories []task.Repo
	ExcludedRepositories []task.Repo
	// HideMentionOnly drops items the searches found only because they mention the
	// developer. False shows every item, which is the default.
	HideMentionOnly bool
	// Routing is routing.yaml, or nil when routing is off. RoutingErr is why it
	// failed to load; Route reports it, and commands that never route ignore it.
	Routing         *routing.Config
	RoutingErr      error
	RoutingDefaults routing.Defaults
	Selector        Selector
	// Quota may be nil, which makes every agent's quota unknown.
	Quota QuotaChecker
	Now   func() time.Time
}

// Inbox is InboxView with the configured repository filter.
func (a App) Inbox(ctx context.Context) (InboxReport, error) {
	return a.InboxView(ctx, false)
}

// InboxView fetches GitHub state, reports what changed since the last run, and
// records the new state as seen. Only records that changed are rewritten. allRepos
// ignores the include and exclude lists. Items and records outside the view are
// dropped before Reconcile, so no record is written for them and no waiting task
// from them is reported.
func (a App) InboxView(ctx context.Context, allRepos bool) (InboxReport, error) {
	recs, err := a.Store.List(ctx)
	if err != nil {
		return InboxReport{}, err
	}
	dead := a.deadRunners(recs)
	notes, checked, warnings, err := a.gate(ctx, recs)
	if err != nil {
		return InboxReport{}, err
	}
	if err := a.reap(ctx, recs, reapable(recs, dead, checked)); err != nil {
		return InboxReport{}, err
	}
	if a.CleanOnDone {
		a.cleanDone(ctx, recs, notes)
	}
	search, err := a.GitHub.InboxItems(ctx)
	if err != nil {
		return InboxReport{}, err
	}
	items := a.visibleItems(search.Items, allRepos)
	tracked, gone, refreshWarnings, err := a.fetchTracked(ctx, recs, items, allRepos)
	if err != nil {
		return InboxReport{}, err
	}
	// Taken before Reconcile, which calls observe and rewrites these in place.
	stored := storedReasons(recs)
	report, dirty := Reconcile(append(items, tracked...), a.visibleRecords(recs, allRepos), notes, len(search.Truncated) == 0, a.Now())
	report.Truncated = search.Truncated
	report.Warnings = append(warnings, refreshWarnings...)
	for _, seen := range dirty {
		if _, err := a.update(ctx, seen.Key, func(cur *task.Record) (*task.Record, error) {
			return mergeObserved(cur, seen), nil
		}); err != nil {
			return InboxReport{}, err
		}
	}
	for _, key := range gone {
		// The reasons are read before stopTracking clears them. This note is about an
		// item that is already off GitHub, so nothing reports its reasons any more, and
		// why foreman was tracking it is the only thing left to judge it by.
		var tracked []task.InboxReason
		rec, err := a.update(ctx, key, func(cur *task.Record) (*task.Record, error) {
			if cur == nil || cur.Delegation != nil {
				return nil, nil
			}
			if cur.GitHub != nil {
				tracked = slices.Clone(cur.GitHub.Reasons)
			}
			stopTracking(cur, a.Now())
			return cur, nil
		})
		if err != nil {
			return InboxReport{}, err
		}
		// The setting is applied here rather than in hideMentionOnly: the entry carries
		// no reasons of its own, and that is what tells InboxActivity to always show a
		// gone note. Giving it the tracked reasons would hide the note from the picker
		// even with mentions on.
		if rec == nil || (a.HideMentionOnly && mentionOnly(tracked)) {
			continue
		}
		report.Changed = append(report.Changed, InboxEntry{Item: InboxItem{Key: rec.Key, Kind: rec.Kind, Title: rec.Title, URL: rec.URL}, Changes: []string{goneNote}})
	}
	if a.HideMentionOnly {
		hideMentionOnly(&report, stored)
	}
	return report, nil
}

const goneNote = "no longer on GitHub or no longer accessible; foreman stopped checking it"

// hideMentionOnly drops the items a search returned only because they mention the
// developer, for issues and pull requests alike. The searches still run and the
// snapshots still record every reason, so a lost reason is still reported and the
// items come back the moment the setting does. Delegated work waiting on the
// developer is not an inbox find, so it is left alone, and a delegated FAILED or
// REVIEWED task is how the developer restarts or resumes it. The note for an item that left
// GitHub is filtered where it is built, because it carries no reasons of its own.
func hideMentionOnly(r *InboxReport, stored map[task.Key][]task.InboxReason) {
	keep := func(entries []InboxEntry) []InboxEntry {
		var out []InboxEntry
		for _, e := range entries {
			if (e.Delegated && CanDelegate(e)) || !mentionOnly(involvement(e.Item, stored)) {
				out = append(out, e)
			}
		}
		return out
	}
	r.New, r.Changed, r.Current = keep(r.New), keep(r.Changed), keep(r.Current)
}

// involvement is why the developer is involved with it. A tracked fetch carries the
// synthetic `tracked` reason alone, because no search returned the item: a search that
// hit its result limit can leave a tracked item out. What the task stored is then the
// only account of the involvement, so the filter judges the item by that.
func involvement(it InboxItem, stored map[task.Key][]task.InboxReason) []task.InboxReason {
	if slices.Equal(it.Reasons, []task.InboxReason{task.Tracked}) {
		return stored[it.Key]
	}
	return it.Reasons
}

// storedReasons indexes why the developer is involved with each item foreman tracks.
// It must be read before Reconcile, which rewrites the records it reads.
func storedReasons(recs []*task.Record) map[task.Key][]task.InboxReason {
	stored := map[task.Key][]task.InboxReason{}
	for _, r := range recs {
		if r.GitHub != nil && len(r.GitHub.Reasons) > 0 {
			stored[r.Key] = slices.Clone(r.GitHub.Reasons)
		}
	}
	return stored
}

func mentionOnly(reasons []task.InboxReason) bool {
	return slices.Contains(reasons, task.Mentioned) &&
		!slices.Contains(reasons, task.Assigned) &&
		!slices.Contains(reasons, task.ReviewRequested)
}

// stopTracking clears an undelegated item's reasons, which is what ends its refreshes.
func stopTracking(rec *task.Record, now time.Time) {
	snap := task.Snapshot{}
	if rec.GitHub != nil {
		snap = *rec.GitHub
	}
	snap.Reasons = nil
	rec.GitHub, rec.LastGitHubSync = &snap, now
}

// mergeObserved copies only the GitHub-derived fields onto the current file, so a
// runner that saved since List keeps its state, session, and outcome.
func mergeObserved(cur, seen *task.Record) *task.Record {
	if cur == nil {
		return seen
	}
	cur.Title, cur.URL, cur.Kind = seen.Title, seen.URL, seen.Kind
	cur.GitHub, cur.LastGitHubSync = seen.GitHub, seen.LastGitHubSync
	if seen.State == task.Done && !keepsState(cur.State) {
		cur.State = task.Done
	}
	return cur
}

// keepsState reports a task the inbox leaves in the state it found. A runner owns the
// task it is driving, and the gate has already recorded why a PR_CLOSED task ended: a
// closed issue must not turn that into DONE, which would clean away the worktree and
// branch the developer was promised, and stop the gate watching for the PR to reopen.
func keepsState(s task.State) bool { return running(s) || s == task.PRClosed }

// fetchTracked refreshes missing delegated work and undelegated items whose last
// involvement reasons have not yet been cleared by a complete refresh. gone lists
// undelegated items GitHub no longer has; any other lookup failure is a warning.
func (a App) fetchTracked(ctx context.Context, recs []*task.Record, found []InboxItem, allRepos bool) (items []InboxItem, gone []task.Key, warnings []error, err error) {
	inSearch := map[task.Key]bool{}
	for _, it := range found {
		inSearch[it.Key] = true
	}
	var keys []task.Key
	delegated := map[task.Key]bool{}
	for _, r := range recs {
		delegated[r.Key] = r.Delegation != nil
		if r.Delegation == nil && r.GitHub != nil && len(r.GitHub.Reasons) == 0 {
			continue
		}
		if r.State != task.Done && !inSearch[r.Key] && a.repoVisible(r.Repo, allRepos) {
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
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	for i, err := range errs {
		switch {
		case err == nil:
			items = append(items, out[i])
		case errors.Is(err, ErrGone) && !delegated[keys[i]]:
			gone = append(gone, keys[i])
		default:
			warnings = append(warnings, fmt.Errorf("refresh %s#%d: %w", keys[i].Repo, keys[i].Number, err))
		}
	}
	return items, gone, warnings, nil
}

func (a App) visibleItems(items []InboxItem, allRepos bool) []InboxItem {
	filtered := make([]InboxItem, 0, len(items))
	for _, item := range items {
		if a.repoVisible(item.Key.Repo, allRepos) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (a App) visibleRecords(recs []*task.Record, allRepos bool) []*task.Record {
	visible := make([]*task.Record, 0, len(recs))
	for _, r := range recs {
		if a.repoVisible(r.Repo, allRepos) {
			visible = append(visible, r)
		}
	}
	return visible
}

// repoVisible says whether the inbox shows repo. A nonempty include list decides
// alone; otherwise the exclude list applies.
func (a App) repoVisible(repo task.Repo, allRepos bool) bool {
	switch {
	case allRepos:
		return true
	case len(a.IncludedRepositories) > 0:
		return slices.Contains(a.IncludedRepositories, repo)
	default:
		return !slices.Contains(a.ExcludedRepositories, repo)
	}
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
	report := InboxReport{WaitingChanges: map[task.Key][]string{}}
	var dirty []*task.Record
	reported := map[task.Key]bool{}
	for _, it := range items {
		reported[it.Key] = true
		rec := byKey[it.Key]
		if rec == nil {
			rec = discovered(it, now)
			byKey[it.Key] = rec
			dirty = append(dirty, rec)
			report.Current = append(report.Current, entryFor(rec, it, reasonText(it.Reasons)))
			if !needsHuman(rec) {
				report.New = append(report.New, InboxEntry{Item: it, Changes: reasonText(it.Reasons)})
			}
			continue
		}
		changes := describeChanges(rec.GitHub, it, complete)
		if len(changes) > 0 {
			observe(rec, it, complete, now)
			dirty = append(dirty, rec)
		}
		changes = append(changes, notes[it.Key]...)
		current := changes
		if len(current) == 0 {
			current = []string{"no change"}
		}
		report.Current = append(report.Current, entryFor(rec, it, current))
		switch {
		case needsHuman(rec):
			if len(changes) > 0 {
				report.WaitingChanges[rec.Key] = changes
			}
		case len(changes) == 0:
			report.Unchanged++
		default:
			report.Changed = append(report.Changed, InboxEntry{Item: it, Changes: changes})
		}
	}
	for _, r := range recs {
		if n := notes[r.Key]; len(n) > 0 && !reported[r.Key] {
			if needsHuman(r) {
				report.WaitingChanges[r.Key] = n
			} else {
				report.Changed = append(report.Changed, InboxEntry{Item: InboxItem{Key: r.Key, Kind: r.Kind, Title: r.Title, URL: r.URL}, Changes: n})
			}
		}
	}
	for _, r := range recs {
		switch r.State {
		case task.NeedsDecision:
			report.NeedsDecision = append(report.NeedsDecision, r)
		case task.Reviewed:
			report.Reviewed = append(report.Reviewed, r)
		case task.ReadyForHuman:
			report.Ready = append(report.Ready, r)
		}
	}
	return report, dirty
}

// entryFor is one item in the current refresh, carrying the state of the run foreman
// has for it. INBOX reads that state to leave work already in hand alone.
func entryFor(rec *task.Record, it InboxItem, changes []string) InboxEntry {
	return InboxEntry{Item: it, Changes: changes, Delegated: rec.Delegation != nil, State: rec.State}
}

func needsHuman(r *task.Record) bool {
	return r.State == task.NeedsDecision || r.State == task.ReadyForHuman || r.State == task.Reviewed
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
	// A closed issue or merged PR ends the task unless the state is already someone
	// else's to set; the gate ends a delegated task when its PR is merged or closed.
	if it.State != "open" && !keepsState(rec.State) {
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
