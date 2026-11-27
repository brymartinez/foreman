// Package backendtest is the contract every workspace.Backend must meet. A backend's
// own tests call Run with a factory and the hooks that act as a person editing the
// store.
package backendtest

import (
	"context"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/task"
	"github.com/brymartinez/foreman/internal/workspace"
)

// Env is one backend under test, with hooks that act as a person using the store.
type Env struct {
	Backend workspace.Backend
	// UserCreate adds an entry as a person would and returns its Ref.
	UserCreate func(e workspace.TaskEntry) string
	// UserEdit changes the user-owned fields of an entry as a person would.
	UserEdit func(ref string, fn func(*workspace.TaskEntry))
}

func record(id, status string) workspace.Record {
	return workspace.Record{Format: workspace.Format, ID: id, Source: task.Local, Title: "Title " + id, Status: status}
}

func find(t *testing.T, b workspace.Backend, id string) workspace.TaskEntry {
	t.Helper()
	entries, err := b.ListTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("no entry with Foreman ID %s", id)
	return workspace.TaskEntry{}
}

// Run runs the contract. newEnv returns a fresh, empty store each call.
func Run(t *testing.T, newEnv func(t *testing.T) Env) {
	ctx := context.Background()
	t.Run("ensure can be repeated", func(t *testing.T) {
		e := newEnv(t)
		for range 2 {
			if err := e.Backend.Ensure(ctx); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Run("upsert twice by Foreman ID keeps one entry and updates it", func(t *testing.T) {
		e := newEnv(t)
		e.Backend.Ensure(ctx)
		seed := workspace.TaskSeed{Name: "Seeded", Repository: "o/r", Description: "desc"}
		ref1, err := e.Backend.UpsertTask(ctx, workspace.TaskPublish{Record: record("L-0A1B2C3D", "INBOX"), Create: seed})
		if err != nil {
			t.Fatal(err)
		}
		ref2, err := e.Backend.UpsertTask(ctx, workspace.TaskPublish{Record: record("L-0A1B2C3D", "PLANNING"), Create: workspace.TaskSeed{Name: "Other"}})
		if err != nil || ref1 != ref2 {
			t.Fatalf("refs %q %q, %v", ref1, ref2, err)
		}
		entries, _ := e.Backend.ListTasks(ctx)
		if len(entries) != 1 {
			t.Fatalf("entries = %d", len(entries))
		}
		got := entries[0]
		if got.Record == nil || got.Record.Status != "PLANNING" {
			t.Errorf("record = %+v", got.Record)
		}
		if got.Name != "Seeded" || got.Repository != "o/r" || got.Description != "desc" {
			t.Errorf("user fields must come from the first create only: %+v", got)
		}
	})
	t.Run("an update never changes user-owned fields", func(t *testing.T) {
		e := newEnv(t)
		e.Backend.Ensure(ctx)
		ref, _ := e.Backend.UpsertTask(ctx, workspace.TaskPublish{Record: record("L-0A1B2C3D", "INBOX"), Create: workspace.TaskSeed{Name: "A"}})
		e.UserEdit(ref, func(x *workspace.TaskEntry) {
			x.Name, x.Repository, x.ConfirmedTriage, x.ScopeConfirmed, x.Queue, x.Answer = "Renamed", "x/y", "scope", true, true, "B"
		})
		e.Backend.UpsertTask(ctx, workspace.TaskPublish{Record: record("L-0A1B2C3D", "IMPLEMENTING")})
		got := find(t, e.Backend, "L-0A1B2C3D")
		if got.Name != "Renamed" || got.Repository != "x/y" || got.ConfirmedTriage != "scope" || !got.ScopeConfirmed || !got.Queue || got.Answer != "B" {
			t.Errorf("user fields changed: %+v", got)
		}
	})
	t.Run("an entry a user created gets its Foreman ID assigned", func(t *testing.T) {
		e := newEnv(t)
		e.Backend.Ensure(ctx)
		ref := e.UserCreate(workspace.TaskEntry{Name: "From my phone", Description: "do it"})
		entries, _ := e.Backend.ListTasks(ctx)
		if len(entries) != 1 || entries[0].ID != "" || entries[0].Name != "From my phone" {
			t.Fatalf("entries = %+v", entries)
		}
		if err := e.Backend.AssignID(ctx, ref, "L-AAAAAAAA"); err != nil {
			t.Fatal(err)
		}
		got := find(t, e.Backend, "L-AAAAAAAA")
		if got.Name != "From my phone" || got.Description != "do it" {
			t.Errorf("assigning an ID changed the user's content: %+v", got)
		}
	})
	t.Run("clearing queue and answer touches only that field", func(t *testing.T) {
		e := newEnv(t)
		e.Backend.Ensure(ctx)
		ref, _ := e.Backend.UpsertTask(ctx, workspace.TaskPublish{Record: record("L-0A1B2C3D", "NEEDS_DECISION")})
		e.UserEdit(ref, func(x *workspace.TaskEntry) { x.Queue, x.Answer, x.ScopeConfirmed = true, "A", true })
		if err := e.Backend.ClearQueue(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if got := find(t, e.Backend, "L-0A1B2C3D"); got.Queue || got.Answer != "A" || !got.ScopeConfirmed {
			t.Fatalf("after ClearQueue: %+v", got)
		}
		if err := e.Backend.ClearAnswer(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if got := find(t, e.Backend, "L-0A1B2C3D"); got.Answer != "" || !got.ScopeConfirmed {
			t.Fatalf("after ClearAnswer: %+v", got)
		}
	})
	t.Run("a publish that replaces the question clears the answer with it", func(t *testing.T) {
		e := newEnv(t)
		e.Backend.Ensure(ctx)
		ref, _ := e.Backend.UpsertTask(ctx, workspace.TaskPublish{Record: record("L-0A1B2C3D", "NEEDS_DECISION")})
		e.UserEdit(ref, func(x *workspace.TaskEntry) { x.Queue, x.Answer = true, "A" })
		if _, err := e.Backend.UpsertTask(ctx, workspace.TaskPublish{Record: record("L-0A1B2C3D", "IMPLEMENTING"), ClearAnswer: true}); err != nil {
			t.Fatal(err)
		}
		if got := find(t, e.Backend, "L-0A1B2C3D"); got.Answer != "" || !got.Queue || got.Record == nil || got.Record.Status != "IMPLEMENTING" {
			t.Fatalf("after the publish: %+v", got)
		}
	})
	t.Run("confirmed scope is written only over the text the caller accepts", func(t *testing.T) {
		e := newEnv(t)
		e.Backend.Ensure(ctx)
		ref, _ := e.Backend.UpsertTask(ctx, workspace.TaskPublish{Record: record("L-0A1B2C3D", "INBOX"), Create: workspace.TaskSeed{Name: "A", Description: "desc", ConfirmedTriage: "old", ScopeConfirmed: true}})
		holds := func(want string) func(string) bool { return func(got string) bool { return got == want } }
		e.UserEdit(ref, func(x *workspace.TaskEntry) { x.ConfirmedTriage = "typed in the store" })
		if wrote, err := e.Backend.ConfirmScope(ctx, "L-0A1B2C3D", "from the host", holds("old")); err != nil || wrote {
			t.Fatalf("overwrote a newer edit: %v, %v", wrote, err)
		}
		if got := find(t, e.Backend, "L-0A1B2C3D"); got.ConfirmedTriage != "typed in the store" {
			t.Fatalf("scope = %q", got.ConfirmedTriage)
		}
		for _, text := range []string{"from the host", ""} {
			current := find(t, e.Backend, "L-0A1B2C3D").ConfirmedTriage
			if wrote, err := e.Backend.ConfirmScope(ctx, "L-0A1B2C3D", text, holds(current)); err != nil || !wrote {
				t.Fatalf("confirm %q: %v, %v", text, wrote, err)
			}
			got := find(t, e.Backend, "L-0A1B2C3D")
			if got.ConfirmedTriage != text || got.ScopeConfirmed != (text != "") || got.Description != "desc" || got.Record == nil {
				t.Fatalf("after confirming %q: %+v", text, got)
			}
		}
		if wrote, err := e.Backend.ConfirmScope(ctx, "L-FFFFFFFF", "x", holds("")); err != nil || wrote {
			t.Fatalf("a missing entry: %v, %v", wrote, err)
		}
	})
	t.Run("duplicates are listed oldest first and trashing one leaves the other", func(t *testing.T) {
		e := newEnv(t)
		e.Backend.Ensure(ctx)
		a := e.UserCreate(workspace.TaskEntry{ID: "L-0A1B2C3D", Name: "first"})
		time.Sleep(1100 * time.Millisecond) // store clocks have one-second resolution
		b := e.UserCreate(workspace.TaskEntry{ID: "L-0A1B2C3D", Name: "second"})
		dups, err := e.Backend.Duplicates(ctx)
		if err != nil || len(dups) != 1 || dups[0].ID != "L-0A1B2C3D" || len(dups[0].Refs) != 2 || dups[0].Refs[0] != a || dups[0].Refs[1] != b {
			t.Fatalf("dups = %+v, %v", dups, err)
		}
		if err := e.Backend.Trash(ctx, b); err != nil {
			t.Fatal(err)
		}
		if dups, _ := e.Backend.Duplicates(ctx); len(dups) != 0 {
			t.Fatalf("dups after trash = %+v", dups)
		}
		if entries, _ := e.Backend.ListTasks(ctx); len(entries) != 1 || entries[0].Name != "first" {
			t.Fatalf("entries = %+v", entries)
		}
	})
	t.Run("knowledge written can be listed with a matching digest", func(t *testing.T) {
		e := newEnv(t)
		e.Backend.Ensure(ctx)
		snap := knowledge.Snapshot{Format: knowledge.FormatVersion, Repo: "o/r", NextID: 1, Learnings: []knowledge.Learning{}, Evaluations: []knowledge.Evaluation{}}
		if err := e.Backend.WriteKnowledge(ctx, workspace.KnowledgePublish{Repo: "o/r", Snapshot: snap, Guidance: "# Guidance\n"}); err != nil {
			t.Fatal(err)
		}
		if err := e.Backend.WriteKnowledge(ctx, workspace.KnowledgePublish{Repo: "o/r", Snapshot: snap}); err != nil {
			t.Fatal(err)
		}
		got, err := e.Backend.ListKnowledge(ctx)
		if err != nil || len(got) != 1 || got[0].Repo != "o/r" || got[0].ParseErr != nil || got[0].Digest != workspace.SnapshotDigest(snap) {
			t.Fatalf("knowledge = %+v, %v", got, err)
		}
	})
	t.Run("check reports its steps and passes on a working store", func(t *testing.T) {
		e := newEnv(t)
		var steps []workspace.Step
		if err := e.Backend.Check(ctx, func(s workspace.Step) { steps = append(steps, s) }); err != nil {
			t.Fatal(err)
		}
		if len(steps) == 0 {
			t.Fatal("no steps reported")
		}
		for _, s := range steps {
			if s.Err != nil {
				t.Errorf("step %s failed: %v", s.Name, s.Err)
			}
		}
		if entries, _ := e.Backend.ListTasks(ctx); len(entries) != 0 {
			t.Errorf("the check left %d entries behind", len(entries))
		}
	})
}
