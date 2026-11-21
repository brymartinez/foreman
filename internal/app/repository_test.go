package app

import (
	"context"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/store"
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
			key := task.Key{Source: task.Local, ExternalID: localID}
			triage := store.Triage{Dir: t.TempDir()}
			if tc.triage != "" {
				if err := triage.Save(key, tc.triage); err != nil {
					t.Fatal(err)
				}
			}
			a := App{LocalIssues: &fakeLocal{issue: Issue{Body: tc.body}}, Triage: triage}
			p, err := a.ProposeRepository(context.Background(), key)
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
	key := task.Key{Source: task.Local, ExternalID: "L-0000002A"}
	tracker := &fakeLocal{items: []InboxItem{{Key: key, Identifier: "ENG-42", Kind: task.Issue, State: "open", Reasons: []task.InboxReason{task.Assigned}}}}
	store := &fakeStore{recs: map[task.Key]*task.Record{}}
	a := App{Store: store, GitHub: &fakeGitHub{}, LocalIssues: tracker, IncludedRepositories: []task.Repo{"acme/web"}, Runners: &fakeRunners{}, Now: func() time.Time { return now }}
	for i := 0; i < 2; i++ {
		report, err := a.InboxSources(context.Background(), task.Local)
		if err != nil || len(report.Current) != 1 || report.Current[0].Item.Key != key {
			t.Fatalf("report=%+v, error=%v", report, err)
		}
		if len(store.recs) != 0 {
			t.Fatal("unresolved issue created a repository-bound task")
		}
	}
}

func TestUnresolvedIssueIgnoresOldDiscoveryMapping(t *testing.T) {
	key := task.Key{Repo: "acme/old", Source: task.Local, ExternalID: "L-0000002A"}
	store := &fakeStore{recs: map[task.Key]*task.Record{key: {Key: key, Kind: task.Issue, State: task.Discovered}}}
	tracker := &fakeLocal{items: []InboxItem{{Key: task.Key{Source: task.Local, ExternalID: key.ExternalID}, Identifier: "ENG-42", Kind: task.Issue, State: "open"}}}
	a := App{Store: store, GitHub: &fakeGitHub{}, LocalIssues: tracker, Runners: &fakeRunners{}, Now: func() time.Time { return now }}
	report, err := a.InboxSources(context.Background(), task.Local)
	if err != nil || len(report.Current) != 1 || report.Current[0].Item.Key.Repo != "" {
		t.Fatalf("report=%+v error=%v", report, err)
	}
}

func TestLocalStartRefusesDifferentRepositoryForExistingDelegation(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: "acme/old", Source: task.Local, ExternalID: "L-0000002A"}
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
