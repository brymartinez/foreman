package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

type inboxTracker struct {
	items    []InboxItem
	err      error
	projects map[task.Repo]string
}

type failingInboxGitHub struct{ *fakeGitHub }

func (f failingInboxGitHub) InboxItems(context.Context) (InboxSearch, error) {
	return InboxSearch{}, errors.New("rate limited")
}

func (f *inboxTracker) AssignedIssues(_ context.Context, projects map[task.Repo]string) ([]InboxItem, error) {
	f.projects = projects
	return f.items, f.err
}
func (f *inboxTracker) GetIssue(context.Context, task.Key) (Issue, error) { return Issue{}, nil }
func (f *inboxTracker) IssueComments(context.Context, task.Key) ([]TrackerComment, error) {
	return nil, nil
}
func (f *inboxTracker) GetIssueComment(context.Context, string) (TrackerComment, error) {
	return TrackerComment{}, nil
}
func (f *inboxTracker) PostIssueComment(context.Context, task.Key, string, string) (TrackerComment, error) {
	return TrackerComment{}, nil
}

func TestMixedInboxShowsGitHubAndMappedLinearIssuesWithoutMappedGitHubIssues(t *testing.T) {
	linearKey := task.Key{Repo: "acme/web", Source: task.Linear, ExternalID: "uuid-42"}
	tracker := &inboxTracker{items: []InboxItem{{Key: linearKey, Identifier: "WEB-42", Kind: task.Issue, Title: "Linear work", State: "open", Reasons: []task.InboxReason{task.Assigned}}}}
	a := App{Store: &fakeStore{recs: map[task.Key]*task.Record{}}, GitHub: &fakeGitHub{items: []InboxItem{repoItem("acme/api", 1, task.Assigned), repoItem("acme/web", 2, task.Assigned)}}, Linear: tracker, LinearProjects: map[task.Repo]string{"acme/web": "https://linear.app/acme/project/web-123"}, Runners: &fakeRunners{}, Now: func() time.Time { return now }}
	report, err := a.InboxSources(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Current) != 2 || report.Current[0].Item.Key.Repo != "acme/api" || report.Current[1].Item.Key != linearKey {
		t.Fatalf("current = %+v", report.Current)
	}
	if len(tracker.projects) != 1 || tracker.projects["acme/web"] == "" {
		t.Fatalf("projects = %+v", tracker.projects)
	}
	rec, err := a.Store.Get(context.Background(), linearKey)
	if err != nil || rec == nil || rec.Identifier != "WEB-42" {
		t.Fatalf("stored Linear identifier = %+v, %v", rec, err)
	}
}

func TestGitHubInboxSelectionSkipsLinearAndDoesNotRequireItsKey(t *testing.T) {
	a := App{Store: &fakeStore{recs: map[task.Key]*task.Record{}}, GitHub: &fakeGitHub{items: []InboxItem{repoItem("acme/api", 1, task.Assigned), repoItem("acme/web", 2, task.Assigned)}}, LinearProjects: map[task.Repo]string{"acme/web": "https://linear.app/acme/project/web-123"}, Runners: &fakeRunners{}, Now: func() time.Time { return now }}
	report, err := a.InboxSources(context.Background(), task.GitHub)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Current) != 1 || report.Current[0].Item.Key.Repo != "acme/api" {
		t.Fatalf("current = %+v", report.Current)
	}
}

func TestMixedInboxKeepsGitHubResultsAndWarnsWhenLinearFails(t *testing.T) {
	a := App{Store: &fakeStore{recs: map[task.Key]*task.Record{}}, GitHub: &fakeGitHub{items: []InboxItem{repoItem("acme/api", 1, task.Assigned)}}, Linear: &inboxTracker{err: errors.New("rate limited")}, LinearProjects: map[task.Repo]string{"acme/web": "https://linear.app/acme/project/web-123"}, Runners: &fakeRunners{}, Now: func() time.Time { return now }}
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

func TestMixedInboxKeepsLinearResultsAndWarnsWhenGitHubFails(t *testing.T) {
	linearKey := task.Key{Repo: "acme/web", Source: task.Linear, ExternalID: "uuid-42"}
	a := App{Store: &fakeStore{recs: map[task.Key]*task.Record{}}, GitHub: failingInboxGitHub{&fakeGitHub{}}, Linear: &inboxTracker{items: []InboxItem{{Key: linearKey, Identifier: "WEB-42", Kind: task.Issue, Title: "Linear work", State: "open", Reasons: []task.InboxReason{task.Assigned}}}}, LinearProjects: map[task.Repo]string{"acme/web": "https://linear.app/acme/project/web-123"}, Runners: &fakeRunners{}, Now: func() time.Time { return now }}
	report, err := a.InboxSources(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Current) != 1 || report.Current[0].Item.Key != linearKey || report.SourcesFresh {
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

func TestLinearInboxSelectionAppliesRepositoryFilters(t *testing.T) {
	tracker := &inboxTracker{items: []InboxItem{{Key: task.Key{Repo: "acme/web", Source: task.Linear, ExternalID: "uuid-42"}, Identifier: "WEB-42", Kind: task.Issue, State: "open"}}}
	a := App{Store: &fakeStore{recs: map[task.Key]*task.Record{}}, GitHub: &fakeGitHub{}, Linear: tracker, LinearProjects: map[task.Repo]string{"acme/web": "web-project", "acme/other": "other-project"}, IncludedRepositories: []task.Repo{"acme/web"}, Runners: &fakeRunners{}, Now: func() time.Time { return now }}
	report, err := a.InboxSources(context.Background(), task.Linear)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Current) != 1 || len(tracker.projects) != 1 || tracker.projects["acme/web"] == "" {
		t.Fatalf("current = %+v, projects = %v", report.Current, tracker.projects)
	}
}
