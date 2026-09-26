package app

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

func TestInboxRefreshesMissingUndelegatedIssue(t *testing.T) {
	missing := seen(5, task.Discovered, "open", t0, task.Assigned)
	ignored := seen(6, task.Discovered, "open", t0)
	ignored.Repo = "ignored/repo"
	store := &fakeStore{recs: map[task.Key]*task.Record{
		missing.Key: missing,
		ignored.Key: ignored,
		key(7):      seen(7, task.Done, "closed", t0),
		key(8):      seen(8, task.Discovered, "open", t0, task.Assigned),
	}}
	gh := &fakeGitHub{items: []InboxItem{item(8, "open", t0, task.Assigned)}}
	a := App{Store: store, GitHub: gh, Runners: &fakeRunners{}, IgnoredRepositories: []task.Repo{ignored.Repo}, Now: func() time.Time { return now }}
	report, err := a.Inbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gh.fetched, []task.Key{missing.Key}) {
		t.Fatalf("fetched = %v, want missing undelegated issue only", gh.fetched)
	}
	if got := store.recs[missing.Key]; got.State != task.Done || got.GitHub.State != "closed" || !got.LastGitHubSync.Equal(now) {
		t.Fatalf("missing issue not refreshed: %+v", got)
	}
	if len(report.Changed) != 1 || !slices.Contains(report.Changed[0].Changes, "closed") {
		t.Fatalf("closure not reported: %+v", report.Changed)
	}
}

type missingOpenIssue struct{ *fakeGitHub }

func (f missingOpenIssue) GetItem(_ context.Context, k task.Key) (InboxItem, error) {
	f.fetched = append(f.fetched, k)
	return item(k.Number, "open", t0, task.Tracked), nil
}

func TestMissingOpenIssueLosesAssignmentOnlyForCompleteSearch(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "truncated"}[truncated], func(t *testing.T) {
			rec := seen(5, task.Discovered, "open", t0, task.Assigned)
			store := &fakeStore{recs: map[task.Key]*task.Record{rec.Key: rec}}
			gh := missingOpenIssue{&fakeGitHub{}}
			if truncated {
				gh.truncated = []string{"assigned issues"}
			}
			a := App{Store: store, GitHub: gh, Runners: &fakeRunners{}, Now: func() time.Time { return now }}
			report, err := a.Inbox(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(gh.fetched) != 1 {
				t.Fatalf("missing issue was not fetched: %v", gh.fetched)
			}
			if truncated {
				if len(report.Changed) != 0 || !slices.Contains(store.recs[rec.Key].GitHub.Reasons, task.Assigned) {
					t.Fatalf("incomplete search lost assignment: %+v", report)
				}
				if _, err := a.Inbox(context.Background()); err != nil || len(gh.fetched) != 2 {
					t.Fatalf("truncated search stopped tracking: %v, %v", gh.fetched, err)
				}
			} else {
				if len(report.Changed) != 1 || !slices.Contains(report.Changed[0].Changes, "no longer assigned to you") || len(store.recs[rec.Key].GitHub.Reasons) != 0 {
					t.Fatalf("lost assignment not reported and saved: %+v", report)
				}
				again, err := a.Inbox(context.Background())
				if err != nil || len(again.Changed) != 0 {
					t.Fatalf("repeated change: %+v, %v", again, err)
				}
				if len(gh.fetched) != 1 {
					t.Fatalf("unrelated issue fetched again: %v", gh.fetched)
				}
			}
		})
	}
}
