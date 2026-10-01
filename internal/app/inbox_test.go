package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

var (
	t0  = time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	t1  = t0.Add(time.Hour)
	now = t0.Add(2 * time.Hour)
)

func key(n int) task.Key { return task.Key{Repo: "ridezoomo/armada-backend", Number: n} }

func item(n int, state string, updated time.Time, reasons ...task.InboxReason) InboxItem {
	return InboxItem{Key: key(n), Kind: task.Issue, Title: "Issue", URL: "u", State: state, UpdatedAt: updated, Reasons: reasons}
}

func seen(n int, state task.State, ghState string, updated time.Time, reasons ...task.InboxReason) *task.Record {
	return &task.Record{
		Key: key(n), Kind: task.Issue, Title: "Issue", State: state,
		GitHub: &task.Snapshot{UpdatedAt: updated, State: ghState, Reasons: reasons},
	}
}

func TestReconcile(t *testing.T) {
	recs := []*task.Record{
		seen(1, task.Discovered, "open", t0, task.Assigned),              // unchanged
		seen(2, task.Discovered, "open", t0, task.Mentioned),             // new activity
		seen(3, task.Discovered, "open", t0, task.Mentioned),             // now assigned
		seen(4, task.Discovered, "open", t0, task.Assigned),              // closed -> DONE
		seen(5, task.NeedsDecision, "open", t0, task.Assigned),           // changed but shown as decision
		seen(6, task.ReadyForHuman, "open", t0, task.Assigned),           // unchanged, shown as ready
		seen(7, task.Working, "open", t0, task.Assigned, task.Mentioned), // tracked fetch, unchanged
	}
	items := []InboxItem{
		item(1, "open", t0, task.Assigned),
		item(2, "open", t1, task.Mentioned),
		item(3, "open", t1, task.Mentioned, task.Assigned),
		item(4, "closed", t1, task.Tracked),
		item(5, "open", t1, task.Assigned),
		item(6, "open", t0, task.Assigned),
		item(7, "open", t0, task.Tracked),
		item(8, "open", t1, task.ReviewRequested),
	}
	// Incomplete searches: an item's absence from them says nothing (D15).
	report, dirty := Reconcile(items, recs, nil, false, now)

	if got := entryNumbers(report.New); !slices.Equal(got, []int{8}) {
		t.Errorf("new = %v", got)
	}
	if report.New[0].Changes[0] != "review requested" {
		t.Errorf("new reason = %v", report.New[0].Changes)
	}
	wantChanged := map[int]string{2: "new activity", 3: "assigned to you", 4: "closed"}
	if got := entryNumbers(report.Changed); !slices.Equal(got, []int{2, 3, 4}) {
		t.Errorf("changed = %v", got)
	}
	for _, e := range report.Changed {
		if strings.Join(e.Changes, ", ") != wantChanged[e.Item.Key.Number] {
			t.Errorf("#%d changes = %v", e.Item.Key.Number, e.Changes)
		}
	}
	if len(report.NeedsDecision) != 1 || report.NeedsDecision[0].Number != 5 {
		t.Errorf("needs decision = %v", report.NeedsDecision)
	}
	if len(report.Ready) != 1 || report.Ready[0].Number != 6 {
		t.Errorf("ready = %v", report.Ready)
	}
	if report.Unchanged != 2 {
		t.Errorf("unchanged = %d, want 2 (#1, #7)", report.Unchanged)
	}

	dirtyNums := []int{}
	for _, r := range dirty {
		dirtyNums = append(dirtyNums, r.Number)
	}
	slices.Sort(dirtyNums)
	if !slices.Equal(dirtyNums, []int{2, 3, 4, 5, 8}) {
		t.Errorf("dirty = %v", dirtyNums)
	}
	if recs[3].State != task.Done {
		t.Errorf("closed discovered item state = %s, want DONE", recs[3].State)
	}
	if !recs[1].LastGitHubSync.Equal(now) || !recs[1].GitHub.UpdatedAt.Equal(t1) {
		t.Errorf("snapshot not advanced: %+v", recs[1].GitHub)
	}
}

func TestReconcileTrackedFetchKeepsSearchReasons(t *testing.T) {
	rec := seen(7, task.Working, "open", t0, task.Assigned)
	_, dirty := Reconcile([]InboxItem{item(7, "closed", t1, task.Tracked)}, []*task.Record{rec}, nil, false, now)
	if len(dirty) != 1 || !slices.Equal(rec.GitHub.Reasons, []task.InboxReason{task.Assigned}) {
		t.Fatalf("reasons = %v", rec.GitHub.Reasons)
	}
	if rec.State != task.Working {
		t.Errorf("delegated task state changed to %s; Stage 1 must not touch delegated state", rec.State)
	}
}

func TestReconcileSecondRunIsQuiet(t *testing.T) {
	items := []InboxItem{item(8, "open", t1, task.ReviewRequested)}
	report1, dirty := Reconcile(items, nil, nil, true, now)
	if len(report1.Current) != 1 || report1.Current[0].Item.Key != key(8) {
		t.Fatalf("first current = %+v", report1.Current)
	}
	report, dirty2 := Reconcile(items, dirty, nil, true, now.Add(time.Minute))
	if len(report.New)+len(report.Changed) != 0 || len(dirty2) != 0 || report.Unchanged != 1 {
		t.Fatalf("second run not quiet: %+v, dirty %d", report, len(dirty2))
	}
	if len(report.Current) != 1 || report.Current[0].Item.Key != key(8) {
		t.Fatalf("second current = %+v", report.Current)
	}
}

func TestFilterInboxItemsIgnoresExactRepositories(t *testing.T) {
	items := []InboxItem{item(1, "open", t0, task.Assigned), {Key: task.Key{Repo: "acme/widgets", Number: 1}, State: "open"}}
	got := filterInboxItems(items, []task.Repo{"ridezoomo/armada-backend"})
	if len(got) != 1 || got[0].Key != items[1].Key {
		t.Fatalf("filtered items = %+v", got)
	}
}

func entryNumbers(es []InboxEntry) []int {
	var out []int
	for _, e := range es {
		out = append(out, e.Item.Key.Number)
	}
	return out
}

type fakeStore struct{ recs map[task.Key]*task.Record }

func (f *fakeStore) Get(_ context.Context, k task.Key) (*task.Record, error) { return f.recs[k], nil }
func (f *fakeStore) Save(_ context.Context, r *task.Record) error {
	f.recs[r.Key] = r
	return nil
}
func (f *fakeStore) Update(ctx context.Context, k task.Key, fn func(*task.Record) (*task.Record, error)) (*task.Record, error) {
	cur := f.recs[k]
	if cur != nil {
		// The Markdown store decodes a record per read, so the copy must not share the
		// delegation with the caller's: a write here is not visible to a list taken
		// before it.
		c := *cur
		if c.Delegation != nil {
			d := *c.Delegation
			c.Delegation = &d
		}
		cur = &c
	}
	next, err := fn(cur)
	if err == nil && next != nil {
		f.recs[k] = next
	}
	return next, err
}

func (f *fakeStore) List(context.Context) ([]*task.Record, error) {
	var out []*task.Record
	for _, r := range f.recs {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b *task.Record) int {
		return strings.Compare(string(a.Repo), string(b.Repo))*1000 + a.Number - b.Number
	})
	return out, nil
}

type fakeGitHub struct {
	items          []InboxItem
	truncated      []string
	fetched        []task.Key
	issue          Issue
	repo           RepoInfo
	prs            map[int]PullRequest
	onPR           func() // runs once, inside the next GetPullRequest
	ci             map[int]task.CIStatus
	comments       map[int64]IssueComment // posted issue comments by id
	edits          int                    // PostIssueComment calls that edited a comment
	postErr        error                  // fails the PostIssueComment call after the first postOK
	postOK         int
	prComments     map[int][]PRComment // the PR conversation, by PR number
	prCommentErr   error
	prCommentReads int
	mu             sync.Mutex
}

func (f *fakeGitHub) PullRequestComments(_ context.Context, pr task.Key) ([]PRComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prCommentReads++
	if f.prCommentErr != nil {
		return nil, f.prCommentErr
	}
	return slices.Clone(f.prComments[pr.Number]), nil
}

// addPRComment plays a comment arriving on a PR, from any goroutine.
func (f *fakeGitHub) addPRComment(pr int, c PRComment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.prComments == nil {
		f.prComments = map[int][]PRComment{}
	}
	f.prComments[pr] = append(f.prComments[pr], c)
}

func (f *fakeGitHub) PostIssueComment(_ context.Context, k task.Key, id int64, body string) (IssueComment, error) {
	if f.postErr != nil && len(f.comments) >= f.postOK {
		return IssueComment{}, f.postErr
	}
	if f.comments == nil {
		f.comments = map[int64]IssueComment{}
	}
	if id > 0 {
		if _, ok := f.comments[id]; !ok {
			return IssueComment{}, fmt.Errorf("%w: comment %d", ErrGone, id)
		}
		f.edits++
	} else {
		id = int64(1000 + len(f.comments) + 1)
	}
	c := IssueComment{ID: id, URL: fmt.Sprintf("https://github.com/%s/issues/%d#issuecomment-%d", k.Repo, k.Number, id), Body: body}
	f.comments[id] = c
	return c, nil
}

func (f *fakeGitHub) GetIssueComment(_ context.Context, _ task.Repo, id int64) (IssueComment, error) {
	c, ok := f.comments[id]
	if !ok {
		return IssueComment{}, fmt.Errorf("%w: comment %d", ErrGone, id)
	}
	return c, nil
}

func (f *fakeGitHub) CIStatus(_ context.Context, k task.Key) (task.CIStatus, error) {
	st, ok := f.ci[k.Number]
	if !ok {
		return task.CIStatus{}, fmt.Errorf("gh: no CI for %d", k.Number)
	}
	return st, nil
}

func (f *fakeGitHub) GetPullRequest(_ context.Context, k task.Key) (PullRequest, error) {
	if f.onPR != nil {
		// Plays another process writing between a refresh's read and its own write.
		hook := f.onPR
		f.onPR = nil
		hook()
	}
	pr, ok := f.prs[k.Number]
	if !ok {
		return PullRequest{}, fmt.Errorf("gh: no pull requests found for %d", k.Number)
	}
	return pr, nil
}

func (f *fakeGitHub) OpenPullRequestForBranch(_ context.Context, _ task.Repo, branch string) (*PullRequest, error) {
	for _, pr := range f.prs {
		if pr.HeadRefName == branch && pr.State == "open" {
			return &pr, nil
		}
	}
	return nil, nil
}

func (f *fakeGitHub) GetIssue(_ context.Context, k task.Key) (Issue, error) {
	i := f.issue
	i.Key = k
	return i, nil
}

func (f *fakeGitHub) RepoAt(context.Context, string) (RepoInfo, error) { return f.repo, nil }

func (f *fakeGitHub) InboxItems(context.Context) (InboxSearch, error) {
	return InboxSearch{Items: f.items, Truncated: f.truncated}, nil
}
func (f *fakeGitHub) GetItem(_ context.Context, k task.Key) (InboxItem, error) {
	f.fetched = append(f.fetched, k)
	return item(k.Number, "closed", t1, task.Tracked), nil
}

func TestInboxFetchesDelegatedTasksMissingFromSearch(t *testing.T) {
	working := seen(7, task.Working, "open", t0, task.Assigned)
	working.Delegation = &task.Delegation{Implementer: task.Session{Agent: task.Claude}}
	done := seen(9, task.Done, "closed", t0)
	done.Delegation = &task.Delegation{}
	store := &fakeStore{recs: map[task.Key]*task.Record{
		key(7): working,
		key(9): done,
		key(1): seen(1, task.Discovered, "open", t0, task.Assigned),
	}}
	gh := &fakeGitHub{items: []InboxItem{item(1, "open", t0, task.Assigned)}}
	report, err := App{Store: store, GitHub: gh, Runners: &fakeRunners{}, Now: func() time.Time { return now }}.Inbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gh.fetched, []task.Key{key(7)}) {
		t.Errorf("fetched %v, want only #7", gh.fetched)
	}
	if len(report.Changed) != 1 || report.Changed[0].Changes[0] != "closed" {
		t.Errorf("changed = %+v", report.Changed)
	}
}

func TestInboxHidesMentionOnlyItems(t *testing.T) {
	mentionedPR := item(2, "open", t0, task.Mentioned)
	mentionedPR.Kind = task.PullRequest
	items := []InboxItem{
		item(1, "open", t0, task.Mentioned), // mention-only issue: hidden
		mentionedPR,                         // mention-only PR: hidden
		item(3, "open", t0, task.Mentioned, task.Assigned), // also assigned: kept
		item(4, "open", t0, task.ReviewRequested),          // review requested: kept
	}
	shown := func(hide bool) []int {
		store := &fakeStore{recs: map[task.Key]*task.Record{}}
		a := App{Store: store, GitHub: &fakeGitHub{items: items}, Runners: &fakeRunners{},
			HideMentionOnly: hide, Now: func() time.Time { return now }}
		report, err := a.Inbox(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got := entryNumbers(report.New); !slices.Equal(got, entryNumbers(report.Current)) {
			t.Fatalf("hide=%v: new %v and current %v disagree", hide, got, entryNumbers(report.Current))
		}
		return entryNumbers(report.Current)
	}
	if got := shown(false); !slices.Equal(got, []int{1, 2, 3, 4}) {
		t.Errorf("mentions shown = %v, want every item", got)
	}
	if got := shown(true); !slices.Equal(got, []int{3, 4}) {
		t.Errorf("mentions hidden = %v, want only the assigned and review-requested items", got)
	}
}

// trackedGitHub answers a tracked fetch with an open item that has new activity. That
// is what a search hitting its result limit produces: the item is still the
// developer's, but no search returned it, so the fetch has only the `tracked` reason.
type trackedGitHub struct {
	*fakeGitHub
}

func (g *trackedGitHub) GetItem(_ context.Context, k task.Key) (InboxItem, error) {
	return item(k.Number, "open", t1, task.Tracked), nil
}

func TestInboxHidesAMentionOnlyItemATruncatedSearchMissed(t *testing.T) {
	report := func(hide bool, kind task.Kind, reasons ...task.InboxReason) InboxReport {
		rec := seen(1, task.Discovered, "open", t0, reasons...)
		rec.Kind = kind
		store := &fakeStore{recs: map[task.Key]*task.Record{rec.Key: rec}}
		gh := &trackedGitHub{fakeGitHub: &fakeGitHub{truncated: []string{"mentions"}}}
		a := App{Store: store, GitHub: gh, Runners: &fakeRunners{}, HideMentionOnly: hide,
			Now: func() time.Time { return now }}
		got, err := a.Inbox(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Truncated) == 0 {
			t.Fatal("the search must report a truncation for this path to run")
		}
		return got
	}
	for _, tc := range []struct {
		name    string
		hide    bool
		kind    task.Kind
		reasons []task.InboxReason
		want    []int
	}{
		{"mention only, mentions off", true, task.Issue, []task.InboxReason{task.Mentioned}, nil},
		{"mention-only PR, mentions off", true, task.PullRequest, []task.InboxReason{task.Mentioned}, nil},
		{"mention only, mentions on", false, task.Issue, []task.InboxReason{task.Mentioned}, []int{1}},
		{"also assigned, mentions off", true, task.Issue, []task.InboxReason{task.Mentioned, task.Assigned}, []int{1}},
		{"review requested, mentions off", true, task.Issue, []task.InboxReason{task.Mentioned, task.ReviewRequested}, []int{1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := report(tc.hide, tc.kind, tc.reasons...)
			if n := entryNumbers(got.Changed); !slices.Equal(n, tc.want) {
				t.Errorf("changed = %v, want %v", n, tc.want)
			}
			// The dashboard pane reads Current, so the setting has to reach it too.
			if n := entryNumbers(got.Current); !slices.Equal(n, tc.want) {
				t.Errorf("current = %v, want %v", n, tc.want)
			}
		})
	}
}

func TestHideMentionOnlyKeepsChangedEntries(t *testing.T) {
	recs := []*task.Record{
		seen(1, task.Discovered, "open", t0, task.Mentioned),
		seen(3, task.Discovered, "open", t0, task.Mentioned),
	}
	items := []InboxItem{item(1, "open", t1, task.Mentioned), item(3, "open", t1, task.Mentioned, task.Assigned)}
	stored := storedReasons(recs)
	report, _ := Reconcile(items, recs, nil, true, now)
	hideMentionOnly(&report, stored)
	if got := entryNumbers(report.Changed); !slices.Equal(got, []int{3}) {
		t.Errorf("changed = %v, want only the assigned item", got)
	}
}

func TestHideMentionOnlyKeepsDelegatedWorkToResume(t *testing.T) {
	recs := []*task.Record{
		seen(1, task.Reviewed, "open", t0, task.Mentioned),
		seen(2, task.Failed, "open", t0, task.Mentioned),
		seen(3, task.ReadyForHuman, "open", t0, task.Mentioned),
		seen(4, task.Discovered, "open", t0, task.Mentioned),
	}
	for _, r := range recs[:3] {
		r.Delegation = &task.Delegation{PR: 900 + r.Number}
	}
	items := []InboxItem{item(1, "open", t0, task.Mentioned), item(2, "open", t0, task.Mentioned),
		item(3, "open", t0, task.Mentioned), item(4, "open", t0, task.Mentioned)}
	stored := storedReasons(recs)
	report, _ := Reconcile(items, recs, nil, true, now)
	hideMentionOnly(&report, stored)
	if got := entryNumbers(Delegable(report)); !slices.Equal(got, []int{1, 2}) {
		t.Errorf("delegable = %v, want the REVIEWED and FAILED tasks", got)
	}
}

func TestMergeObservedKeepsRunnerState(t *testing.T) {
	cur := seen(7, task.Reviewing, "open", t0, task.Assigned)
	cur.Delegation = &task.Delegation{PR: 912}
	observed := seen(7, task.Working, "closed", t1, task.Assigned)
	got := mergeObserved(cur, observed)
	if got.State != task.Reviewing || got.Delegation.PR != 912 || got.GitHub.State != "closed" {
		t.Fatalf("got %+v / %+v", got, got.GitHub)
	}
}

func TestFind(t *testing.T) {
	other := seen(825, task.Discovered, "open", t0)
	other.Repo = "ridezoomo/sw-automations"
	store := &fakeStore{recs: map[task.Key]*task.Record{
		key(825):  seen(825, task.Discovered, "open", t0),
		other.Key: other,
		key(1):    seen(1, task.Discovered, "open", t0),
	}}
	a := App{Store: store}
	if r, err := a.Find(context.Background(), "", 1); err != nil || r.Number != 1 {
		t.Errorf("unique find: %v %v", r, err)
	}
	if _, err := a.Find(context.Background(), "", 825); err == nil || !strings.Contains(err.Error(), "ridezoomo/sw-automations#825") {
		t.Errorf("ambiguous err = %v", err)
	}
	if r, err := a.Find(context.Background(), "ridezoomo/sw-automations", 825); err != nil || r.Repo != "ridezoomo/sw-automations" {
		t.Errorf("qualified find: %v %v", r, err)
	}
	if _, err := a.Find(context.Background(), "", 404); err == nil || !strings.Contains(err.Error(), "foreman inbox") {
		t.Errorf("missing err = %v", err)
	}
}

func TestStatusOrdersByAttention(t *testing.T) {
	mk := func(n int, s task.State) *task.Record {
		r := seen(n, s, "open", t0)
		r.Delegation = &task.Delegation{Implementer: task.Session{Agent: task.Codex}}
		return r
	}
	store := &fakeStore{recs: map[task.Key]*task.Record{
		key(1): mk(1, task.Working),
		key(2): mk(2, task.ReadyForHuman),
		key(3): mk(3, task.NeedsDecision),
		key(4): mk(4, task.Done),
		key(5): seen(5, task.Discovered, "open", t0),
	}}
	recs, _, err := App{Store: store, Runners: &fakeRunners{}}.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []int
	for _, r := range recs {
		got = append(got, r.Number)
	}
	if !slices.Equal(got, []int{3, 2, 1}) {
		t.Errorf("status order = %v", got)
	}
}

func TestReconcileReportsALostReasonOnceWhenSearchesAreComplete(t *testing.T) {
	rec := seen(3, task.Discovered, "open", t0, task.Assigned, task.Mentioned)
	items := []InboxItem{item(3, "open", t0, task.Mentioned)}
	report, dirty := Reconcile(items, []*task.Record{rec}, nil, true, now)
	if len(report.Changed) != 1 || !slices.Equal(report.Changed[0].Changes, []string{"no longer assigned to you"}) || len(dirty) != 1 {
		t.Fatalf("changed %+v dirty %d", report.Changed, len(dirty))
	}
	if again, _ := Reconcile(items, []*task.Record{rec}, nil, true, now); len(again.Changed) != 0 {
		t.Errorf("reported twice: %+v", again.Changed)
	}
}

func TestReconcileTrustsATrackedFetchOnlyWhenSearchesAreComplete(t *testing.T) {
	fetched := []InboxItem{item(7, "open", t0, task.Tracked)}

	partial := seen(7, task.Working, "open", t0, task.Assigned)
	if report, _ := Reconcile(fetched, []*task.Record{partial}, nil, false, now); len(report.Changed) != 0 {
		t.Errorf("a truncated search reported a lost reason: %+v", report.Changed)
	}

	complete := seen(7, task.Working, "open", t0, task.Assigned)
	report, _ := Reconcile(fetched, []*task.Record{complete}, nil, true, now)
	if len(report.Changed) != 1 || !slices.Equal(report.Changed[0].Changes, []string{"no longer assigned to you"}) || len(complete.GitHub.Reasons) != 0 {
		t.Fatalf("changed %+v reasons %v", report.Changed, complete.GitHub.Reasons)
	}
}

func TestInboxReportsTruncatedSearches(t *testing.T) {
	f := newFixture()
	f.gh.truncated = []string{"issues that mention you"}
	report, err := f.app.Inbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(report.Truncated, f.gh.truncated) {
		t.Errorf("truncated = %q", report.Truncated)
	}
}
