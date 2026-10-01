package app

import (
	"bufio"
	"slices"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func TestChooseInboxCollectsItemAgentAndContext(t *testing.T) {
	report := InboxReport{Current: []InboxEntry{
		{Item: InboxItem{Key: task.Key{Repo: "a/one", Number: 1}, Title: "First"}},
		{Item: InboxItem{Key: task.Key{Repo: "b/two", Number: 2}, Title: "Second"}},
	}}
	got, err := ChooseInbox(bufio.NewReader(strings.NewReader("2\ncodex\n/tmp/context.md\n")), &strings.Builder{}, report)
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != report.Current[1].Item.Key || got.Agent != task.Codex || got.ContextFile != "/tmp/context.md" {
		t.Fatalf("selection = %+v", got)
	}
}

func TestChooseInboxRepromptsAndAllowsQuit(t *testing.T) {
	report := InboxReport{Current: []InboxEntry{{Item: InboxItem{Key: task.Key{Repo: "a/one", Number: 1}, Title: "First"}}}}
	var out strings.Builder
	got, err := ChooseInbox(bufio.NewReader(strings.NewReader("9\nq\n")), &out, report)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("quit selection = %+v", got)
	}
	if !strings.Contains(out.String(), "choose a number from 1 to 1") {
		t.Errorf("missing invalid-selection guidance: %s", out.String())
	}
}

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

func TestChooseInboxOffersOnlyIssues(t *testing.T) {
	report := InboxReport{Current: []InboxEntry{
		{Item: InboxItem{Key: task.Key{Repo: "a/one", Number: 950}, Kind: task.PullRequest, Title: "Review me"}},
		{Item: InboxItem{Key: task.Key{Repo: "a/one", Number: 42}, Kind: task.Issue, Title: "Add retries"}},
	}}
	var out strings.Builder
	got, err := ChooseInbox(bufio.NewReader(strings.NewReader("1\nclaude\n\n")), &out, report)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Key.Number != 42 || strings.Contains(out.String(), "#950") {
		t.Fatalf("selection %+v from:\n%s", got, out.String())
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

func TestChooseInboxResumesAReviewedItem(t *testing.T) {
	report := InboxReport{Current: []InboxEntry{{
		Item:      InboxItem{Key: task.Key{Repo: "a/one", Number: 42}, Kind: task.Issue, Title: "Add retries"},
		Delegated: true,
		State:     task.Reviewed,
	}}}
	var out strings.Builder
	got, err := ChooseInbox(bufio.NewReader(strings.NewReader("1\n")), &out, report)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || !got.Resume || got.Key.Number != 42 || got.Agent != "" {
		t.Fatalf("selection = %+v", got)
	}
	if strings.Contains(out.String(), "Agent (claude/codex)") || !strings.Contains(out.String(), "REVIEWED") {
		t.Fatalf("picker output:\n%s", out.String())
	}
}
