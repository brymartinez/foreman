package app

import (
	"slices"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func TestDelegableOffersOnlyOpenUntrackedIssues(t *testing.T) {
	report := InboxReport{Current: []InboxEntry{
		{Item: InboxItem{Key: task.Key{Repo: "a/one", Number: 1}, Kind: task.Issue, Title: "Open issue"}},
		{Item: InboxItem{Key: task.Key{Repo: "a/one", Number: 2}, Kind: task.PullRequest, Title: "A PR"}},
		{Item: InboxItem{Key: task.Key{Repo: "a/one", Number: 3}, Kind: task.Issue, Title: "Tracked", Reasons: []task.InboxReason{task.Tracked}}},
		{Item: InboxItem{Key: task.Key{Repo: "a/one", Number: 4}, Kind: task.Issue, Title: "Closed", State: "closed"}},
		{Item: InboxItem{Key: task.Key{Repo: "a/one", Number: 5}, Kind: task.Issue, Title: "Merged", State: "merged"}},
	}}
	got := Delegable(report)
	if len(got) != 1 || got[0].Item.Key.Number != 1 {
		t.Fatalf("Delegable = %+v; want only the open untracked issue", got)
	}
}

func TestDelegableHidesDelegatedWorkInProgress(t *testing.T) {
	entry := func(n int, state task.State) InboxEntry {
		return InboxEntry{
			Item:      InboxItem{Key: task.Key{Repo: "a/one", Number: n}, Kind: task.Issue, Title: string(state), Reasons: []task.InboxReason{task.Tracked}},
			Delegated: true,
			State:     state,
		}
	}
	report := InboxReport{Current: []InboxEntry{
		{Item: InboxItem{Key: task.Key{Repo: "a/one", Number: 1}, Kind: task.Issue, Title: "Never delegated"}},
		entry(2, task.Working),
		entry(3, task.Reviewing),
		entry(4, task.NeedsDecision),
		entry(5, task.ReadyForHuman),
		entry(6, task.Failed),
		entry(7, task.Reviewed),
	}}
	var got []int
	for _, e := range Delegable(report) {
		got = append(got, e.Item.Key.Number)
	}
	if !slices.Equal(got, []int{1, 6, 7}) {
		t.Fatalf("Delegable = %v; want the undelegated issue, the FAILED one, and the REVIEWED one", got)
	}
}
