package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

type failingInboxGitHub struct{ *fakeGitHub }

type mappedGitHub struct{ *fakeGitHub }

func (g mappedGitHub) GetItem(ctx context.Context, key task.Key) (InboxItem, error) {
	item, err := g.fakeGitHub.GetItem(ctx, key)
	item.Key = key
	return item, err
}

func (f failingInboxGitHub) InboxItems(context.Context) (InboxSearch, error) {
	return InboxSearch{}, errors.New("rate limited")
}

func TestMixedInboxShowsGitHubAndLocalIssuesIndependently(t *testing.T) {
	localKey := task.Key{Repo: "acme/web", Source: task.Local, ExternalID: "L-0000002A"}
	tracker := &fakeLocal{items: []InboxItem{{Key: localKey, Identifier: "L-0000002A", Kind: task.Issue, Title: "Local work", State: "open", Reasons: []task.InboxReason{task.Assigned}}}}
	a := App{Store: &fakeStore{recs: map[task.Key]*task.Record{}}, GitHub: &fakeGitHub{items: []InboxItem{repoItem("acme/api", 1, task.Assigned), repoItem("acme/web", 2, task.Assigned)}}, LocalIssues: tracker, Runners: &fakeRunners{}, Now: func() time.Time { return now }}
	report, err := a.InboxSources(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Current) != 3 || report.Current[0].Item.Key.Repo != "acme/api" || report.Current[1].Item.Key.Repo != "acme/web" || report.Current[2].Item.Key != localKey {
		t.Fatalf("current = %+v", report.Current)
	}
	rec, err := a.Store.Get(context.Background(), localKey)
	if err != nil || rec == nil || rec.Identifier != "L-0000002A" {
		t.Fatalf("stored Local identifier = %+v, %v", rec, err)
	}
}

func TestTeamConfigurationKeepsGitHubIssuesAndPullRequests(t *testing.T) {
	pr := repoItem("acme/web", 9, task.ReviewRequested)
	pr.Kind = task.PullRequest
	a := App{Store: &fakeStore{recs: map[task.Key]*task.Record{}}, GitHub: &fakeGitHub{items: []InboxItem{repoItem("acme/web", 2, task.Assigned), pr}}, LocalIssues: &fakeLocal{}, Runners: &fakeRunners{}, Now: func() time.Time { return now }}
	report, err := a.InboxSources(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Current) != 2 || report.Current[1].Item.Key != pr.Key {
		t.Fatalf("mixed report = %+v", report.Current)
	}
}

func TestGitHubInboxSelectionSkipsLocalAndDoesNotRequireItsKey(t *testing.T) {
	a := App{Store: &fakeStore{recs: map[task.Key]*task.Record{}}, GitHub: &fakeGitHub{items: []InboxItem{repoItem("acme/api", 1, task.Assigned), repoItem("acme/web", 2, task.Assigned)}}, Runners: &fakeRunners{}, Now: func() time.Time { return now }}
	report, err := a.InboxSources(context.Background(), task.GitHub)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Current) != 2 {
		t.Fatalf("current = %+v", report.Current)
	}
}

func TestMixedInboxKeepsGitHubResultsAndWarnsWhenLocalFails(t *testing.T) {
	a := App{Store: &fakeStore{recs: map[task.Key]*task.Record{}}, GitHub: &fakeGitHub{items: []InboxItem{repoItem("acme/api", 1, task.Assigned)}}, LocalIssues: &fakeLocal{err: errors.New("rate limited")}, Runners: &fakeRunners{}, Now: func() time.Time { return now }}
	report, err := a.InboxSources(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Current) != 1 || report.Current[0].Item.Key.Repo != "acme/api" || report.SourcesFresh {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Warnings) == 0 || !strings.Contains(report.Warnings[0].Error(), "rate limited") {
		t.Fatalf("warnings = %v", report.Warnings)
	}
}

func TestMixedInboxKeepsLocalResultsAndWarnsWhenGitHubFails(t *testing.T) {
	localKey := task.Key{Repo: "acme/web", Source: task.Local, ExternalID: "L-0000002A"}
	a := App{Store: &fakeStore{recs: map[task.Key]*task.Record{}}, GitHub: failingInboxGitHub{&fakeGitHub{}}, LocalIssues: &fakeLocal{items: []InboxItem{{Key: localKey, Identifier: "L-0000002A", Kind: task.Issue, Title: "Local work", State: "open", Reasons: []task.InboxReason{task.Assigned}}}}, Runners: &fakeRunners{}, Now: func() time.Time { return now }}
	report, err := a.InboxSources(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Current) != 1 || report.Current[0].Item.Key != localKey || report.SourcesFresh {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Warnings) == 0 || !strings.Contains(report.Warnings[0].Error(), "GitHub refresh") {
		t.Fatalf("warnings = %v", report.Warnings)
	}
}

func TestSingleSelectedSourceFailureReturnsAnError(t *testing.T) {
	a := App{Store: &fakeStore{recs: map[task.Key]*task.Record{}}, GitHub: failingInboxGitHub{&fakeGitHub{}}, Runners: &fakeRunners{}, Now: func() time.Time { return now }}
	if _, err := a.InboxSources(context.Background(), task.GitHub); err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("GitHub-only refresh error = %v", err)
	}
}

func TestLocalInboxSelectionAppliesRepositoryFilters(t *testing.T) {
	tracker := &fakeLocal{items: []InboxItem{{Key: task.Key{Repo: "acme/web", Source: task.Local, ExternalID: "L-0000002A"}, Identifier: "L-0000002A", Kind: task.Issue, State: "open"}}}
	a := App{Store: &fakeStore{recs: map[task.Key]*task.Record{}}, GitHub: &fakeGitHub{}, LocalIssues: tracker, IncludedRepositories: []task.Repo{"acme/web"}, Runners: &fakeRunners{}, Now: func() time.Time { return now }}
	report, err := a.InboxSources(context.Background(), task.Local)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Current) != 1 {
		t.Fatalf("current = %+v", report.Current)
	}
}

func TestMappedRepositoryRefreshesLegacyGitHubDelegationInLocalView(t *testing.T) {
	rec := seen(7, task.Failed, "open", t0, task.Assigned)
	rec.Key.Repo = "acme/web"
	rec.Delegation = &task.Delegation{}
	store := &fakeStore{recs: map[task.Key]*task.Record{rec.Key: rec}}
	gh := &fakeGitHub{}
	a := App{Store: store, GitHub: mappedGitHub{gh}, LocalIssues: &fakeLocal{}, Runners: &fakeRunners{}, Now: func() time.Time { return now }}
	report, err := a.InboxSources(context.Background(), task.Local)
	if err != nil {
		t.Fatal(err)
	}
	if len(gh.fetched) != 1 || gh.fetched[0] != rec.Key || store.recs[rec.Key].State != task.Done {
		t.Fatalf("GitHub refresh = %+v, task = %+v", gh.fetched, store.recs[rec.Key])
	}
	if len(report.Current) != 0 {
		t.Fatalf("Local inbox includes GitHub issue: %+v", report.Current)
	}
}
