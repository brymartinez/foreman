package app

import (
	"context"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

func TestRepositoryProposalUsesIssueAndTriageEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, body, triage string
		want               task.Repo
	}{
		{"issue link", "Change https://github.com/acme/web/blob/main/app.go", "", "acme/web"},
		{"triage link", "Fix cache", "## Triage decisions\nRepository: https://github.com/acme/api", "acme/api"},
		{"no destination", "Fix cache", "", ""},
		{"multiple repositories", "https://github.com/acme/web", "## Triage decisions\nhttps://github.com/acme/api", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := &lifecycleTracker{issue: Issue{Body: tc.body}, comments: []TrackerComment{{Body: tc.triage}}}
			a := App{Linear: tracker}
			p, err := a.ProposeRepository(context.Background(), task.Key{Source: task.Linear, ExternalID: "uuid"})
			if err != nil || p.Repo != tc.want {
				t.Fatalf("proposal = %+v, error = %v", p, err)
			}
			if p.Reason == "" {
				t.Fatal("proposal has no explanation")
			}
		})
	}
}

func TestTeamDiscoveryDoesNotRequireOrPersistRepository(t *testing.T) {
	key := task.Key{Source: task.Linear, ExternalID: "uuid-42"}
	tracker := &inboxTracker{items: []InboxItem{{Key: key, Identifier: "ENG-42", Kind: task.Issue, State: "open", Reasons: []task.InboxReason{task.Assigned}}}}
	store := &fakeStore{recs: map[task.Key]*task.Record{}}
	a := App{Store: store, GitHub: &fakeGitHub{}, Linear: tracker, LinearTeam: "https://linear.app/acme/team/ENG", IncludedRepositories: []task.Repo{"acme/web"}, Runners: &fakeRunners{}, Now: func() time.Time { return now }}
	for i := 0; i < 2; i++ {
		report, err := a.InboxSources(context.Background(), task.Linear)
		if err != nil || len(report.Current) != 1 || report.Current[0].Item.Key != key {
			t.Fatalf("report=%+v, error=%v", report, err)
		}
		if len(store.recs) != 0 {
			t.Fatal("unresolved issue created a repository-bound task")
		}
	}
}

func TestUnresolvedIssueIgnoresOldDiscoveryMapping(t *testing.T) {
	key := task.Key{Repo: "acme/old", Source: task.Linear, ExternalID: "uuid-42"}
	store := &fakeStore{recs: map[task.Key]*task.Record{key: {Key: key, Kind: task.Issue, State: task.Discovered}}}
	tracker := &inboxTracker{items: []InboxItem{{Key: task.Key{Source: task.Linear, ExternalID: key.ExternalID}, Identifier: "ENG-42", Kind: task.Issue, State: "open"}}}
	a := App{Store: store, GitHub: &fakeGitHub{}, Linear: tracker, LinearTeam: "https://linear.app/acme/team/ENG", Runners: &fakeRunners{}, Now: func() time.Time { return now }}
	report, err := a.InboxSources(context.Background(), task.Linear)
	if err != nil || len(report.Current) != 1 || report.Current[0].Item.Key.Repo != "" {
		t.Fatalf("report=%+v error=%v", report, err)
	}
}

func TestLinearStartRefusesDifferentRepositoryForExistingDelegation(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: "acme/old", Source: task.Linear, ExternalID: "uuid-42"}
	f.store.recs[key] = &task.Record{Key: key, Delegation: &task.Delegation{}, State: task.Failed}
	other := key
	other.Repo = "acme/new"
	if _, err := f.app.Start(context.Background(), StartRequest{Key: other, Repo: other.Repo, Agent: task.Claude}); err == nil {
		t.Fatal("started an existing issue in another repository")
	}
	if len(f.wt.reqs) != 0 {
		t.Fatal("prepared a worktree before rejecting a duplicate delegation")
	}
}
