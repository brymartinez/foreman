package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/db"
	"github.com/brymartinez/foreman/internal/localissues"
	"github.com/brymartinez/foreman/internal/store"
	"github.com/brymartinez/foreman/internal/task"
)

func TestLocalIntakeResumesAndSavesTheConfirmedDraftOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "foreman.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	var calls int
	session := task.Session{Agent: task.Claude, ID: "first-session"}
	draft := &agent.IntakeDraft{Title: "Check delivery status", Description: "Show the delivery state.",
		SourceURL: "https://example.com/tickets/1", Repo: "example/demo", Decisions: "## Triage decisions\n\nKeep the current delivery labels."}
	intake := localIntake{db: d, lockPath: path + ".lock", planning: task.ModelTarget{Agent: task.Claude, Model: "planning"},
		turns: map[task.Agent]intakeTurn{task.Claude: func(_ context.Context, in agent.StartInput, previous task.Session) (task.Session, agent.IntakeReply, error) {
			calls++
			if in.Model != "planning" {
				t.Fatalf("planning model changed to %q", in.Model)
			}
			if calls == 1 {
				if previous.ID != "" || !strings.Contains(in.Prompt, "https://example.com/tickets/1") {
					t.Fatalf("first turn: session %q, prompt %q", previous.ID, in.Prompt)
				}
				return session, agent.IntakeReply{Text: "Which labels should we use?"}, nil
			}
			if previous != session || !strings.Contains(in.Prompt, "Keep the current labels") {
				t.Fatalf("resumed turn: session %q, prompt %q", previous.ID, in.Prompt)
			}
			return session, agent.IntakeReply{Text: "The draft is ready for review.", Draft: draft}, nil
		}}}
	first, err := intake.Reply(ctx, 0, "https://example.com/tickets/1")
	if err != nil || len(first.Messages) != 2 || first.Draft != nil {
		t.Fatalf("first turn: %+v, %v", first, err)
	}
	other, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	intake.db = other
	intake.planning = task.ModelTarget{Agent: task.Codex, Model: "different"}
	reopened, err := intake.Load(ctx)
	if err != nil || reopened.Revision != first.Revision || len(reopened.Messages) != 2 {
		t.Fatalf("reopened chat: %+v, %v", reopened, err)
	}
	ready, err := intake.Reply(ctx, reopened.Revision, "Keep the current labels")
	if err != nil || ready.Draft == nil || ready.Draft.Title != draft.Title {
		t.Fatalf("ready draft: %+v, %v", ready, err)
	}
	issues := localissues.Store{DB: other}
	if list, err := issues.List(ctx); err != nil || len(list) != 0 {
		t.Fatalf("issues before approval: %v, %v", list, err)
	}
	if _, err := intake.Save(ctx, reopened.Revision); err == nil {
		t.Fatal("stale approval saved an issue")
	}
	id, err := intake.Save(ctx, ready.Revision)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := issues.Get(ctx, id)
	if err != nil || saved.Title != draft.Title || saved.Description != draft.Description || saved.SourceURL != draft.SourceURL || saved.Repo != draft.Repo {
		t.Fatalf("saved issue: %+v, %v", saved, err)
	}
	decisions, err := (store.SQLiteTriage{DB: other}).Read(task.Key{Source: task.Local, ExternalID: id})
	if err != nil || decisions != draft.Decisions {
		t.Fatalf("saved triage: %q, %v", decisions, err)
	}
	if _, err := intake.Save(ctx, ready.Revision); err == nil {
		t.Fatal("repeated approval saved another issue")
	}
	if empty, err := intake.Load(ctx); err != nil || len(empty.Messages) != 0 || empty.Draft != nil || empty.Pending != "" {
		t.Fatalf("chat after save: %+v, %v", empty, err)
	}
}

func TestLocalIntakeRetainsFailedMessageAndRebuildsConversation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "foreman.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	intake := localIntake{db: d, lockPath: path + ".lock", planning: task.ModelTarget{Agent: task.Codex}}
	calls := 0
	intake.turns = map[task.Agent]intakeTurn{task.Codex: func(_ context.Context, in agent.StartInput, session task.Session) (task.Session, agent.IntakeReply, error) {
		calls++
		switch calls {
		case 2:
			return task.Session{}, agent.IntakeReply{}, errors.New("injected provider failure")
		case 3:
			if session.ID != "" || !strings.Contains(in.Prompt, "Initial issue content") || !strings.Contains(in.Prompt, "Amended reply") {
				t.Fatalf("conversation recovery: session %q, prompt %q", session.ID, in.Prompt)
			}
		}
		return task.Session{Agent: task.Codex, ID: "session"}, agent.IntakeReply{Text: "Please clarify the scope."}, nil
	}}
	first, err := intake.Reply(ctx, 0, "Initial issue content")
	if err != nil {
		t.Fatal(err)
	}
	failed, err := intake.Reply(ctx, first.Revision, "Reply to the question")
	if err == nil || failed.Pending != "Reply to the question" || len(failed.Messages) != 2 {
		t.Fatalf("failed turn: %+v, %v", failed, err)
	}
	reopened, err := intake.Load(ctx)
	if err != nil || reopened.Pending != failed.Pending {
		t.Fatalf("pending message after reopen: %+v, %v", reopened, err)
	}
	continued, err := intake.Reply(ctx, reopened.Revision, "Amended reply")
	if err != nil || continued.Pending != "" || len(continued.Messages) != 4 || continued.Messages[2].Text != "Amended reply" {
		t.Fatalf("retried turn: %+v, %v", continued, err)
	}
	if err := intake.Discard(ctx, continued.Revision); err != nil {
		t.Fatal(err)
	}
	if empty, err := intake.Load(ctx); err != nil || len(empty.Messages) != 0 || empty.Revision <= continued.Revision {
		t.Fatalf("discarded chat: %+v, %v", empty, err)
	}
}
