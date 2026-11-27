package workspace

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/localissues"
	"github.com/brymartinez/foreman/internal/portable"
	"github.com/brymartinez/foreman/internal/task"
)

// importedTask saves a local issue with imported work in repo that never started.
func (h *harness) importedTask(id, repo string, pr int, at time.Time) *task.Record {
	h.t.Helper()
	work := &portable.Work{Repo: repo, State: "open", Branch: "feat/x"}
	if pr > 0 {
		work.PR = &portable.PR{Number: pr, URL: "https://github.com/" + repo + "/pull/7"}
	}
	rec := portable.RecordFor(portable.Issue{ID: id, Title: "Add storage", Work: work}, at)
	h.saveTask(rec)
	return rec
}

func (h *harness) queueWithRepository(repo string) TaskEntry {
	h.t.Helper()
	es := h.entries()
	if len(es) != 1 {
		h.t.Fatalf("entries = %d", len(es))
	}
	h.mem.UserEdit(es[0].Ref, func(e *TaskEntry) { e.Repository, e.Queue = repo, true })
	return es[0]
}

func TestQueueingAnIdleImportedTaskDoesNotKeepItsOldRepository(t *testing.T) {
	h := newHarness(t)
	h.issue("L-0A1B2C3D")
	rec := h.importedTask("L-0A1B2C3D", "o/old", 0, t0)
	if err := h.s.Publish(h.ctx, rec.Key); err != nil {
		t.Fatal(err)
	}
	h.queueWithRepository("o/new")
	queued := h.pull().Queued
	if len(queued) != 1 || queued[0].BlockedReason != "" {
		t.Fatalf("queued = %+v", queued)
	}
	if queued[0].Key.Repo != "" {
		t.Fatalf("the start is bound to %s; the issue now names o/new", queued[0].Key.Repo)
	}
	if li, err := h.s.issueFor(queued[0].Key); err != nil || li.Repo != "o/new" {
		t.Fatalf("issue = %+v, %v", li, err)
	}
}

func TestQueueingAResetTaskUnderAnotherRepositoryIsHeld(t *testing.T) {
	h := newHarness(t)
	h.issue("L-0A1B2C3D")
	rec := h.importedTask("L-0A1B2C3D", "o/old", 7, t0)
	if err := h.s.Publish(h.ctx, rec.Key); err != nil {
		t.Fatal(err)
	}
	h.queueWithRepository("o/new")
	queued := h.pull().Queued
	if len(queued) != 1 || !strings.Contains(queued[0].BlockedReason, "o/old") || !strings.Contains(queued[0].BlockedReason, "o/new") {
		t.Fatalf("queued = %+v", queued)
	}
	h.queueWithRepository("o/old")
	if queued = h.pull().Queued; len(queued) != 1 || queued[0].BlockedReason != "" || queued[0].Key != rec.Key {
		t.Fatalf("queued = %+v", queued)
	}
}

func TestClearingTheSharedSourceURLClearsTheIssue(t *testing.T) {
	h := newHarness(t)
	ref := h.mem.UserCreate(TaskEntry{Name: "With a link", SourceURL: "https://example.com/1"})
	h.pull()
	id := h.entries()[0].ID
	h.mem.UserEdit(ref, func(e *TaskEntry) { e.SourceURL = "" })
	h.pull()
	li, err := localissues.GetIn(h.s.DB, id)
	if err != nil || li.SourceURL != "" {
		t.Fatalf("source URL = %q, %v", li.SourceURL, err)
	}
}

// editsBeforeWrite runs a person's edit after the host decided to write and before
// the store applies the write.
type editsBeforeWrite struct {
	*Memory
	upsert, confirm func()
}

func (b *editsBeforeWrite) UpsertTask(ctx context.Context, p TaskPublish) (string, error) {
	if edit := b.upsert; edit != nil {
		b.upsert = nil
		edit()
	}
	return b.Memory.UpsertTask(ctx, p)
}

func (b *editsBeforeWrite) ConfirmScope(ctx context.Context, id, triage string, unchanged func(string) bool) (bool, error) {
	if edit := b.confirm; edit != nil {
		b.confirm = nil
		edit()
	}
	return b.Memory.ConfirmScope(ctx, id, triage, unchanged)
}

func TestAReplyTypedWhileTheQuestionIsBeingReplacedIsNotApplied(t *testing.T) {
	h := newHarness(t)
	backend := &editsBeforeWrite{Memory: h.mem}
	h.s.Backend = backend
	rec := twoQuestionTask()
	h.saveTask(rec)
	if err := h.s.Publish(h.ctx, rec.Key); err != nil {
		t.Fatal(err)
	}
	ref := h.entries()[0].Ref
	rec.Decisions[0].Answer, rec.Decisions[0].AnsweredAt = "A", t0
	h.saveTask(rec)
	backend.upsert = func() { h.mem.UserEdit(ref, func(e *TaskEntry) { e.Answer = "typed for the first question" }) }
	if err := h.s.Publish(h.ctx, rec.Key); err != nil {
		t.Fatal(err)
	}
	if answers := h.pull().Answers; len(answers) != 0 {
		t.Fatalf("a reply to the earlier question was applied to the next: %+v", answers)
	}
}

func TestAScopeEditMadeDuringAHostConfirmationIsKept(t *testing.T) {
	h := newHarness(t)
	backend := &editsBeforeWrite{Memory: h.mem}
	h.s.Backend = backend
	h.issue("L-0A1B2C3D")
	key := localissues.Key("L-0A1B2C3D", "")
	if err := h.s.Publish(h.ctx, key); err != nil {
		t.Fatal(err)
	}
	ref := h.entries()[0].Ref
	if err := h.s.saveTriage(key, "Scope from the host"); err != nil {
		t.Fatal(err)
	}
	backend.confirm = func() {
		h.mem.UserEdit(ref, func(e *TaskEntry) { e.ConfirmedTriage, e.ScopeConfirmed = "Newer scope from a person", true })
	}
	if err := h.s.Publish(h.ctx, key); err != nil {
		t.Fatal(err)
	}
	if got := h.entries()[0].ConfirmedTriage; got != "Newer scope from a person" {
		t.Fatalf("the shared edit was overwritten: %q", got)
	}
	h.pull()
	if got, err := h.s.triage(key); err != nil || got != "Newer scope from a person" {
		t.Fatalf("host triage = %q, %v", got, err)
	}
}

// delegatedElsewhere seeds a local issue with idle imported work in one repository
// and a run in another.
func (h *harness) delegatedElsewhere() (idle, run *task.Record) {
	h.t.Helper()
	h.issue("L-0A1B2C3D")
	idle = h.importedTask("L-0A1B2C3D", "a/idle", 0, t0.Add(time.Hour))
	run = fullTask()
	run.Delegation.RunnerPID, run.Delegation.Launch = 0, nil
	h.saveTask(run)
	return idle, run
}

func TestEitherRecordOfOneIssuePublishesTheDelegatedOne(t *testing.T) {
	h := newHarness(t)
	idle, run := h.delegatedElsewhere()
	for _, key := range []task.Key{idle.Key, run.Key, localissues.Key("L-0A1B2C3D", "")} {
		if err := h.s.Publish(h.ctx, key); err != nil {
			t.Fatal(err)
		}
		es := h.entries()
		if len(es) != 1 || es[0].Record.Repo != "o/r" || es[0].Record.Status != string(task.NeedsDecision) {
			t.Fatalf("after publishing %+v: %+v", key, es[0].Record)
		}
	}
	h.pull()
	if h.mem.Writes != 1 {
		t.Fatalf("writes = %d: the two records took turns in the entry", h.mem.Writes)
	}
}

func TestMoveCopiesAnIssueThatHasRecordsInTwoRepositories(t *testing.T) {
	h := newHarness(t)
	h.delegatedElsewhere()
	var said []string
	m := h.mover()
	m.Report = func(format string, args ...any) { said = append(said, format) }
	res, err := m.Run(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Tasks != 1 || res.Issues != 0 {
		t.Fatalf("result = %+v", res)
	}
	if es := h.entries(); len(es) != 1 || es[0].Record.Repo != "o/r" {
		t.Fatalf("entries = %+v", es)
	}
	if !strings.Contains(strings.Join(said, "\n"), "stays in the local database") {
		t.Fatalf("the move did not name the record it left: %v", said)
	}
}
