package tui

import (
	"context"
	"testing"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

func TestManualLinearDelegationKeepsSourceIdentity(t *testing.T) {
	k := task.Key{Repo: "a/b", Source: task.Linear, ExternalID: "uuid-42"}
	m := model()
	m.SetInbox([]app.InboxEntry{{Item: app.InboxItem{Key: k, Identifier: "WEB-42", Kind: task.Issue, State: "open"}}})
	got := press(&m, Key{Kind: KeyEnter}, rune_('1'), Key{Kind: KeyEnter})
	if got.Kind != ActionStart || got.Start.Key != k {
		t.Fatalf("manual start = %+v", got)
	}
}

func TestAutoLinearDelegationKeepsSourceIdentity(t *testing.T) {
	k := task.Key{Repo: "a/b", Source: task.Linear, ExternalID: "uuid-42"}
	m := Model{}
	m.SetAuto(Auto{Enabled: true, Agent: task.Codex, Max: 1})
	m.SetInbox([]app.InboxEntry{{Item: app.InboxItem{Key: k, Kind: task.Issue, State: "open", Reasons: []task.InboxReason{task.Assigned}, Labels: []string{"triaged"}}}})
	m.githubFresh = true
	reqs := m.AutoStarts()
	if len(reqs) != 1 || reqs[0].Key != k {
		t.Fatalf("auto starts = %+v", reqs)
	}
	result := autoStart(context.Background(), stubSource{rec: &task.Record{Key: k, State: task.Working, Delegation: &task.Delegation{Branch: "feat/linear-web-42"}}}, reqs[0], nil)
	if result.key != k {
		t.Fatalf("auto result key = %+v", result.key)
	}
}
