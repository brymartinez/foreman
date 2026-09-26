package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

func TestInboxStopsCheckingAnIssueGitHubNoLongerHas(t *testing.T) {
	gone := seen(2, task.Discovered, "open", t0, task.Assigned)
	delegated := seen(3, task.Reviewing, "open", t0, task.Assigned)
	delegated.Delegation = &task.Delegation{}
	store := &fakeStore{recs: map[task.Key]*task.Record{gone.Key: gone, delegated.Key: delegated}}
	notFound := fmt.Errorf("%w: gh: Not Found (HTTP 404)", ErrGone)
	gh := &refreshGitHub{fakeGitHub: &fakeGitHub{}, calls: map[task.Key]int{}, errs: map[task.Key]error{gone.Key: notFound, delegated.Key: notFound}}
	a := App{Store: store, GitHub: gh, Runners: &fakeRunners{}, Now: func() time.Time { return now }}

	first, err := a.Inbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Changed) != 1 || first.Changed[0].Item.Key != gone.Key || !slices.Equal(first.Changed[0].Changes, []string{goneNote}) {
		t.Fatalf("gone issue not reported once: %+v", first.Changed)
	}
	if len(first.Warnings) != 1 || !errors.Is(first.Warnings[0], ErrGone) {
		t.Fatalf("delegated work must keep warning: %v", first.Warnings)
	}
	if got := store.recs[gone.Key]; len(got.GitHub.Reasons) != 0 || !got.LastGitHubSync.Equal(now) || got.State != task.Discovered {
		t.Fatalf("gone issue record: %+v", got)
	}

	second, err := a.Inbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Changed) != 0 || gh.calls[gone.Key] != 1 || gh.calls[delegated.Key] != 2 {
		t.Fatalf("second run: changed %+v calls %v", second.Changed, gh.calls)
	}
}

func TestStatusListsTasksWhenAPRLookupFails(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	delete(f.gh.prs, f.store.recs[key].Delegation.PR)
	recs, warnings, err := f.app.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Key != key || len(warnings) != 1 {
		t.Fatalf("recs %d warnings %v", len(recs), warnings)
	}
}
