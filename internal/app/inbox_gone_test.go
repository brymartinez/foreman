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
	delegated := seen(3, task.WaitingForCI, "open", t0, task.Assigned)
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

func TestGoneNoteFollowsTheMentionsSetting(t *testing.T) {
	for _, tc := range []struct {
		name    string
		hide    bool
		reasons []task.InboxReason
		want    int
	}{
		// The item is already off GitHub, so the stored reasons are all that is left to
		// judge it by. stopTracking clears them, so they have to be read first.
		{"mention only, mentions off", true, []task.InboxReason{task.Mentioned}, 0},
		{"mention only, mentions on", false, []task.InboxReason{task.Mentioned}, 1},
		{"also assigned, mentions off", true, []task.InboxReason{task.Mentioned, task.Assigned}, 1},
		{"review requested, mentions off", true, []task.InboxReason{task.Mentioned, task.ReviewRequested}, 1},
		{"assigned only, mentions off", true, []task.InboxReason{task.Assigned}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gone := seen(2, task.Discovered, "open", t0, tc.reasons...)
			store := &fakeStore{recs: map[task.Key]*task.Record{gone.Key: gone}}
			notFound := fmt.Errorf("%w: gh: Not Found (HTTP 404)", ErrGone)
			gh := &refreshGitHub{fakeGitHub: &fakeGitHub{}, calls: map[task.Key]int{}, errs: map[task.Key]error{gone.Key: notFound}}
			a := App{Store: store, GitHub: gh, Runners: &fakeRunners{}, HideMentionOnly: tc.hide, Now: func() time.Time { return now }}

			report, err := a.Inbox(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Changed) != tc.want {
				t.Errorf("changed = %+v, want %d", report.Changed, tc.want)
			}
			// The item stops being checked either way; the setting hides the note, not
			// the bookkeeping that ends the refreshes.
			if got := store.recs[gone.Key]; len(got.GitHub.Reasons) != 0 {
				t.Errorf("reasons not cleared: %+v", got.GitHub.Reasons)
			}
			if tc.want == 1 && len(report.Changed[0].Item.Reasons) != 0 {
				t.Errorf("the gone note must carry no reasons, so InboxActivity still shows it: %+v", report.Changed[0].Item.Reasons)
			}
		})
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
