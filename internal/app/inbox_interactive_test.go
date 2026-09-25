package app

import (
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func TestChooseInboxCollectsItemAgentAndContext(t *testing.T) {
	report := InboxReport{Current: []InboxEntry{
		{Item: InboxItem{Key: task.Key{Repo: "a/one", Number: 1}, Title: "First"}},
		{Item: InboxItem{Key: task.Key{Repo: "b/two", Number: 2}, Title: "Second"}},
	}}
	got, err := ChooseInbox(strings.NewReader("2\ncodex\n/tmp/context.md\n"), &strings.Builder{}, report)
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
	got, err := ChooseInbox(strings.NewReader("9\nq\n"), &out, report)
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
