package app

import (
	"bufio"
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
