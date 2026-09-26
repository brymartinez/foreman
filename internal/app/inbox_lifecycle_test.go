package app

import (
	"bufio"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

type refreshGitHub struct {
	*fakeGitHub
	mu    sync.Mutex
	calls map[task.Key]int
	errs  map[task.Key]error
}

func (g *refreshGitHub) GetItem(ctx context.Context, k task.Key) (InboxItem, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls[k]++
	if ctx.Err() != nil {
		return InboxItem{}, ctx.Err()
	}
	if err := g.errs[k]; err != nil {
		return InboxItem{}, err
	}
	return item(k.Number, "open", t0, task.Tracked), nil
}

func TestInboxRefreshLifecycle(t *testing.T) {
	old := seen(1, task.Discovered, "open", t0, task.Assigned)
	dead := seen(2, task.Discovered, "open", t0, task.Assigned)
	delegated := seen(3, task.Working, "open", t0)
	delegated.Delegation = &task.Delegation{}
	store := &fakeStore{recs: map[task.Key]*task.Record{old.Key: old, dead.Key: dead, delegated.Key: delegated}}
	gh := &refreshGitHub{fakeGitHub: &fakeGitHub{items: []InboxItem{item(4, "open", t0, task.Assigned)}}, calls: map[task.Key]int{}, errs: map[task.Key]error{dead.Key: errors.New("HTTP 404")}}
	a := App{Store: store, GitHub: gh, Runners: &fakeRunners{}, Now: func() time.Time { return now }}
	for round := 0; round < 2; round++ {
		report, err := a.Inbox(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0].Error(), "#2") {
			t.Fatalf("warnings: %v", report.Warnings)
		}
		var out strings.Builder
		_, err = ChooseInbox(bufio.NewReader(strings.NewReader("q\n")), &out, report)
		if err != nil || !strings.Contains(out.String(), "#4") || strings.Contains(out.String(), "#1") || strings.Contains(out.String(), "#2") || strings.Contains(out.String(), "#3") {
			t.Fatalf("picker: %s, %v", out.String(), err)
		}
	}
	if gh.calls[old.Key] != 1 || gh.calls[dead.Key] != 2 || gh.calls[delegated.Key] != 2 {
		t.Fatalf("calls: %v", gh.calls)
	}
	if len(store.recs[dead.Key].GitHub.Reasons) != 1 {
		t.Fatal("failure cleared stored assignment")
	}
	delete(gh.errs, dead.Key)
	if _, err := a.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.recs[dead.Key].GitHub.Reasons) != 0 {
		t.Fatal("recovered lookup did not clear assignment")
	}
	gh.items = append(gh.items, item(1, "open", t0, task.Assigned))
	if _, err := a.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.recs[old.Key].GitHub.Reasons) != 1 {
		t.Fatal("reassignment not saved")
	}
	gh.items = gh.items[:1]
	if _, err := a.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gh.calls[old.Key] != 2 || gh.calls[dead.Key] != 3 {
		t.Fatalf("reappearance/recovery calls: %v", gh.calls)
	}
}

func TestInboxGateLookupFailuresAreWarnings(t *testing.T) {
	for _, failCI := range []bool{false, true} {
		f := newFixture()
		k := reviewed(t, f)
		healthy := *f.store.recs[k]
		healthy.Key = key(99)
		delegation := *healthy.Delegation
		delegation.PR = 913
		healthy.Delegation = &delegation
		healthy.State = task.ReadyForHuman
		f.store.recs[healthy.Key] = &healthy
		pr := f.gh.prs[912]
		pr.Key = key(913)
		pr.State = "merged"
		f.gh.prs[913] = pr
		if failCI {
			delete(f.gh.ci, f.store.recs[k].Delegation.PR)
		} else {
			delete(f.gh.prs, f.store.recs[k].Delegation.PR)
		}
		f.gh.items = []InboxItem{item(k.Number, "open", t0, task.Assigned), item(99, "open", t0, task.Assigned)}
		report, err := f.app.Inbox(context.Background())
		if err != nil || len(report.Warnings) != 1 || len(report.Current) != 2 {
			t.Fatalf("report %+v err %v", report, err)
		}
		if f.store.recs[k].State != task.Reviewing {
			t.Fatal("lookup failure advanced task")
		}
		if f.store.recs[healthy.Key].State != task.Done {
			t.Fatal("lookup failure blocked healthy PR gate")
		}
	}
}

func TestCancelledRefreshIsNotSwallowed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	gh := &refreshGitHub{fakeGitHub: &fakeGitHub{}, calls: map[task.Key]int{}}
	a := App{GitHub: gh}
	_, _, _, err := a.fetchTracked(ctx, []*task.Record{seen(1, task.Discovered, "open", t0, task.Assigned)}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestWaitingChangesOmitsUnchangedTasks(t *testing.T) {
	r := seen(1, task.NeedsDecision, "open", t0, task.Assigned)
	report, _ := Reconcile([]InboxItem{item(1, "open", t0, task.Assigned)}, []*task.Record{r}, nil, true, now)
	if len(report.WaitingChanges) != 0 {
		t.Fatalf("empty entries: %v", report.WaitingChanges)
	}
}
