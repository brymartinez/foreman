package tui

import (
	"context"
	"testing"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

func TestLinearRepositoryIsConfirmedBeforeAgentSelection(t *testing.T) {
	key := task.Key{Source: task.Linear, ExternalID: "uuid-42"}
	m := Model{Width: 100, Height: 30}
	m.SetInbox([]app.InboxEntry{{Item: app.InboxItem{Key: key, Identifier: "ENG-42", Kind: task.Issue, State: "open"}}})
	action := m.Key(Key{Kind: KeyEnter})
	if action.Kind != ActionRepository {
		t.Fatalf("action=%+v", action)
	}
	m.RepositoryProposed(action.ProposalSeq, app.RepositoryProposal{Repo: "acme/web", Reason: "triage link"}, nil)
	if m.Mode != TypingRepository || m.Input != "acme/web" {
		t.Fatalf("mode=%v, input=%q", m.Mode, m.Input)
	}
	m.Input = "acme/api"
	m.Key(Key{Kind: KeyEnter})
	if m.Mode != ChoosingAgent || m.pending.Repo != "acme/api" || m.pending.Key.Repo != "acme/api" || m.pending.Key.ExternalID != key.ExternalID {
		t.Fatalf("pending=%+v, mode=%v", m.pending, m.Mode)
	}
}

func TestRepositoryProposalAfterCancelIsIgnored(t *testing.T) {
	m := Model{Mode: ResolvingRepository, proposalSeq: 1}
	m.Key(Key{Kind: KeyEscape})
	m.RepositoryProposed(1, app.RepositoryProposal{Repo: "acme/web"}, nil)
	if m.Mode != Browsing {
		t.Fatal("late repository result reopened prompt")
	}
}

type repositorySource struct {
	routeSource
	proposal app.RepositoryProposal
}

func (s *repositorySource) ProposeRepository(context.Context, task.Key) (app.RepositoryProposal, error) {
	return s.proposal, nil
}

func TestAutoWaitsForRepositoryOrKeepsOriginalInboxIdentity(t *testing.T) {
	key := task.Key{Source: task.Linear, ExternalID: "uuid-42"}
	req := app.StartRequest{Key: key, Agent: task.Claude, NewOnly: true}
	s := &repositorySource{proposal: app.RepositoryProposal{Reason: "choose a repository"}}
	result := autoStart(context.Background(), s, req, nil)
	if result.wait == "" || len(s.starts) != 0 {
		t.Fatalf("result=%+v starts=%+v", result, s.starts)
	}
	s.proposal = app.RepositoryProposal{Repo: "acme/web"}
	result = autoStart(context.Background(), s, req, nil)
	if result.err != nil || len(s.starts) != 1 || s.starts[0].Key.Repo != "acme/web" || s.starts[0].Key.ExternalID != key.ExternalID || result.key != key {
		t.Fatalf("result=%+v starts=%+v", result, s.starts)
	}
}

func TestAutoDoesNotDuplicateLinearIssueDuringRepositorySelection(t *testing.T) {
	key := task.Key{Source: task.Linear, ExternalID: "uuid-42"}
	m := Model{AutoOn: true, AutoMax: 3, AutoAgent: task.Claude, githubFresh: true}
	m.Inbox = []app.InboxEntry{{Item: app.InboxItem{Key: key, Kind: task.Issue, State: "open", Reasons: []task.InboxReason{task.Assigned}, Labels: []string{"triaged"}}}}
	m.Mode = TypingRepository
	m.pending = app.StartRequest{Key: key}
	if len(m.AutoStarts()) != 0 {
		t.Fatal("auto started the issue being selected manually")
	}
	m.Mode = Browsing
	bound := key
	bound.Repo = "acme/web"
	m.starting = map[task.Key]bool{bound: true}
	if len(m.AutoStarts()) != 0 {
		t.Fatal("auto started the unresolved copy of an issue already starting")
	}
}
