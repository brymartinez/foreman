package tui

import (
	"context"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
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

func TestManualLinearStartReleasesItsSlotForTheNextAutoIssue(t *testing.T) {
	first := task.Key{Repo: "a/b", Source: task.Linear, ExternalID: "uuid-1"}
	second := task.Key{Repo: "a/b", Source: task.Linear, ExternalID: "uuid-2"}
	m := model()
	m.SetTasks(nil)
	m.SetAuto(Auto{Enabled: true, Agent: task.Codex, Max: 1})
	m.SetInbox([]app.InboxEntry{
		{Item: app.InboxItem{Key: first, Identifier: "WEB-1", Kind: task.Issue, State: "open", Reasons: []task.InboxReason{task.Assigned}}},
		{Item: app.InboxItem{Key: second, Identifier: "WEB-2", Kind: task.Issue, State: "open", Reasons: []task.InboxReason{task.Assigned}, Labels: []string{"triaged"}}},
	})
	m.githubFresh = true
	action := press(&m, Key{Kind: KeyEnter}, rune_('1'), Key{Kind: KeyEnter})
	if action.Kind != ActionStart || action.Start.Key != first || !m.starting[first] {
		t.Fatalf("manual start = %+v, starting = %+v", action, m.starting)
	}
	m.Done(first)
	if got := m.AutoStarts(); len(got) != 1 || got[0].Key != second {
		t.Fatalf("auto starts after manual start = %+v, starting = %+v", got, m.starting)
	}
}

func TestLinearAutoRetryPassesItsPriorSelectionToRouting(t *testing.T) {
	k := task.Key{Repo: "a/b", Source: task.Linear, ExternalID: "uuid-2"}
	m := model()
	m.SetTasks(nil)
	m.SetAuto(Auto{Enabled: true, Max: 1})
	m.RouteOn = true
	m.SetInbox([]app.InboxEntry{{Item: app.InboxItem{Key: k, Identifier: "WEB-2", Kind: task.Issue, State: "open", Reasons: []task.InboxReason{task.Assigned}, Labels: []string{"triaged"}}}})
	m.githubFresh = true
	m.autoWaiting(k, "quota exhausted", agent.Selection{Rule: 2})
	m.retryWaiting()
	s := &routeSource{d: app.Decision{Wait: "still exhausted"}}
	results := make(chan result, 1)
	if n := launchAuto(context.Background(), s, &m, results); n != 1 {
		t.Fatalf("launched %d auto starts", n)
	}
	<-results
	if len(s.routed) != 1 || s.routed[0].Prior == nil || s.routed[0].Prior.Rule != 2 {
		t.Fatalf("route requests = %+v", s.routed)
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
