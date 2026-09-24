package app

import (
	"context"
	"slices"
	"strings"
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
	report, dirty := Reconcile(items, recs, now)

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
	_, dirty := Reconcile([]InboxItem{item(7, "closed", t1, task.Tracked)}, []*task.Record{rec}, now)
	if len(dirty) != 1 || !slices.Equal(rec.GitHub.Reasons, []task.InboxReason{task.Assigned}) {
		t.Fatalf("reasons = %v", rec.GitHub.Reasons)
	}
	if rec.State != task.Working {
		t.Errorf("delegated task state changed to %s; Stage 1 must not touch delegated state", rec.State)
	}
}

func TestReconcileSecondRunIsQuiet(t *testing.T) {
	items := []InboxItem{item(8, "open", t1, task.ReviewRequested)}
	_, dirty := Reconcile(items, nil, now)
	report, dirty2 := Reconcile(items, dirty, now.Add(time.Minute))
	if len(report.New)+len(report.Changed) != 0 || len(dirty2) != 0 || report.Unchanged != 1 {
		t.Fatalf("second run not quiet: %+v, dirty %d", report, len(dirty2))
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
	items   []InboxItem
	fetched []task.Key
}

func (f *fakeGitHub) InboxItems(context.Context) ([]InboxItem, error) { return f.items, nil }
func (f *fakeGitHub) GetItem(_ context.Context, k task.Key) (InboxItem, error) {
	f.fetched = append(f.fetched, k)
	return item(k.Number, "closed", t1, task.Tracked), nil
}

func TestInboxFetchesOnlyDelegatedTasksMissingFromSearch(t *testing.T) {
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
	report, err := App{Store: store, GitHub: gh, Now: func() time.Time { return now }}.Inbox(context.Background())
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
	recs, err := App{Store: store}.Status(context.Background())
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
