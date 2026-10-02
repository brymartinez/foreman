package app

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

func repoItem(repo task.Repo, n int, reasons ...task.InboxReason) InboxItem {
	it := item(n, "open", t0, reasons...)
	it.Key.Repo = repo
	return it
}

// filterItems is one item per repository, so a view's result reads as a list of repositories.
var filterItems = []InboxItem{
	repoItem("acme/one", 1, task.Assigned),
	repoItem("acme/two", 2, task.ReviewRequested),
	repoItem("acme/three", 3, task.Mentioned),
}

func currentRepos(t *testing.T, a App, allRepos bool) []task.Repo {
	t.Helper()
	report, err := a.InboxView(context.Background(), allRepos)
	if err != nil {
		t.Fatal(err)
	}
	var out []task.Repo
	for _, e := range report.Current {
		out = append(out, e.Item.Key.Repo)
	}
	slices.Sort(out)
	return out
}

func filterApp(include, exclude []task.Repo) (App, *fakeStore) {
	store := &fakeStore{recs: map[task.Key]*task.Record{}}
	return App{Store: store, GitHub: &fakeGitHub{items: filterItems}, Runners: &fakeRunners{},
		IncludedRepositories: include, ExcludedRepositories: exclude, Now: func() time.Time { return now }}, store
}

func TestInboxIncludeListShowsOnlyIncluded(t *testing.T) {
	a, store := filterApp([]task.Repo{"acme/one", "acme/three"}, nil)
	if got := currentRepos(t, a, false); !slices.Equal(got, []task.Repo{"acme/one", "acme/three"}) {
		t.Fatalf("shown = %v, want only the included repositories", got)
	}
	for k := range store.recs {
		if k.Repo == "acme/two" {
			t.Errorf("a record was saved for %v, which the include list leaves out", k)
		}
	}
}

func TestInboxIncludeBeatsExclude(t *testing.T) {
	a, _ := filterApp([]task.Repo{"acme/one"}, []task.Repo{"acme/one", "acme/two"})
	if got := currentRepos(t, a, false); !slices.Equal(got, []task.Repo{"acme/one"}) {
		t.Fatalf("shown = %v, want the included repository even though it is also excluded", got)
	}
}

func TestInboxEmptyIncludeUsesExclude(t *testing.T) {
	a, _ := filterApp(nil, []task.Repo{"acme/two"})
	if got := currentRepos(t, a, false); !slices.Equal(got, []task.Repo{"acme/one", "acme/three"}) {
		t.Fatalf("shown = %v, want every repository but the excluded one", got)
	}
}

func TestInboxViewAllIgnoresBothLists(t *testing.T) {
	for name, a := range map[string]App{
		"include": func() App { a, _ := filterApp([]task.Repo{"acme/one"}, nil); return a }(),
		"exclude": func() App { a, _ := filterApp(nil, []task.Repo{"acme/one", "acme/two"}); return a }(),
	} {
		if got := currentRepos(t, a, true); !slices.Equal(got, []task.Repo{"acme/one", "acme/three", "acme/two"}) {
			t.Errorf("%s list: all view shows %v, want every repository", name, got)
		}
	}
}

func TestInboxViewAllKeepsMentionsSetting(t *testing.T) {
	a, _ := filterApp([]task.Repo{"acme/one"}, nil)
	a.HideMentionOnly = true
	if got := currentRepos(t, a, true); !slices.Equal(got, []task.Repo{"acme/one", "acme/two"}) {
		t.Fatalf("all view with mentions off shows %v, want the mention-only item hidden", got)
	}
}

func TestInboxKeepsInvolvementReasons(t *testing.T) {
	for _, allRepos := range []bool{false, true} {
		a, _ := filterApp(nil, nil)
		report, err := a.InboxView(context.Background(), allRepos)
		if err != nil {
			t.Fatal(err)
		}
		got := map[task.Repo][]task.InboxReason{}
		for _, e := range report.Current {
			got[e.Item.Key.Repo] = e.Item.Reasons
		}
		for _, want := range filterItems {
			if !slices.Equal(got[want.Key.Repo], want.Reasons) {
				t.Errorf("allRepos=%v: %s reasons = %v, want %v", allRepos, want.Key.Repo, got[want.Key.Repo], want.Reasons)
			}
		}
	}
}

func trackedIn(repo task.Repo) (*task.Record, *fakeStore) {
	rec := seen(5, task.Discovered, "open", t0, task.Assigned)
	rec.Key.Repo = repo
	return rec, &fakeStore{recs: map[task.Key]*task.Record{rec.Key: rec}}
}

func TestInboxRefreshSkipsFilteredTrackedItems(t *testing.T) {
	_, store := trackedIn("acme/other")
	gh := &fakeGitHub{}
	a := App{Store: store, GitHub: gh, Runners: &fakeRunners{}, IncludedRepositories: []task.Repo{"acme/one"}, Now: func() time.Time { return now }}
	if _, err := a.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(gh.fetched) != 0 {
		t.Fatalf("fetched %v, want no refresh of a tracked item outside the include list", gh.fetched)
	}
}

func TestInboxViewAllRefreshesTrackedInExcludedRepo(t *testing.T) {
	rec, store := trackedIn("acme/other")
	gh := &fakeGitHub{}
	a := App{Store: store, GitHub: gh, Runners: &fakeRunners{}, ExcludedRepositories: []task.Repo{"acme/other"}, Now: func() time.Time { return now }}
	if _, err := a.InboxView(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gh.fetched, []task.Key{rec.Key}) {
		t.Fatalf("fetched %v, want the tracked item in the excluded repository", gh.fetched)
	}
}

func TestStatusAndTasksListDelegatedWorkOutsideFilter(t *testing.T) {
	rec, store := trackedIn("acme/other")
	rec.State = task.Failed
	rec.Delegation = &task.Delegation{Implementer: task.Session{Agent: task.Claude}}
	a := App{Store: store, GitHub: &fakeGitHub{}, Runners: &fakeRunners{}, IncludedRepositories: []task.Repo{"acme/one"}, Now: func() time.Time { return now }}
	status, _, err := a.Status(context.Background())
	if err != nil || len(status) != 1 || status[0].Key != rec.Key {
		t.Errorf("status = %v, err = %v; want the task outside the include list", status, err)
	}
	tasks, err := a.Tasks(context.Background())
	if err != nil || len(tasks) != 1 || tasks[0].Key != rec.Key {
		t.Errorf("tasks = %v, err = %v; want the task outside the include list", tasks, err)
	}
}
