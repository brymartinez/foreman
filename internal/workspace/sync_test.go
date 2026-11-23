package workspace

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/db"
	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/localissues"
	"github.com/brymartinez/foreman/internal/store"
	"github.com/brymartinez/foreman/internal/task"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Minute)
	return c.t
}

type harness struct {
	t    *testing.T
	s    *Sync
	mem  *Memory
	ctx  context.Context
	know knowledge.SQLStore
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	d, err := db.Open(t.TempDir() + "/foreman.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c := &clock{t: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)}
	mem := &Memory{Now: c.now}
	know := knowledge.SQLStore{DB: d}
	s := &Sync{DB: d, Backend: mem, Tasks: store.SQLite{DB: d}, Issues: localissues.Store{DB: d}, Knowledge: know, Now: c.now, LockWait: 5 * time.Second}
	return &harness{t: t, s: s, mem: mem, ctx: context.Background(), know: know}
}

func (h *harness) saveTask(rec *task.Record) {
	h.t.Helper()
	if err := h.s.Tasks.Save(h.ctx, rec); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) issue(id string) {
	h.t.Helper()
	li := localissues.Issue{ID: id, Title: "Add storage", Description: "desc", Status: localissues.Open, CreatedAt: t0, UpdatedAt: t0}
	if err := localissues.PutIn(h.s.DB, li); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) entries() []TaskEntry {
	h.t.Helper()
	es, err := h.mem.ListTasks(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	return es
}

func (h *harness) pull() app.WorkspacePull {
	h.t.Helper()
	p, err := h.s.Pull(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	return p
}

func TestPublishSkipsAWriteWhenTheRecordIsUnchanged(t *testing.T) {
	h := newHarness(t)
	rec := fullTask()
	h.saveTask(rec)
	for range 3 {
		if err := h.s.Publish(h.ctx, rec.Key); err != nil {
			t.Fatal(err)
		}
	}
	if h.mem.Writes != 1 {
		t.Fatalf("writes = %d, want 1", h.mem.Writes)
	}
	rec.State = task.Implementing
	h.saveTask(rec)
	h.s.Publish(h.ctx, rec.Key)
	if h.mem.Writes != 2 {
		t.Fatalf("writes = %d after a status change, want 2", h.mem.Writes)
	}
	es := h.entries()
	if len(es) != 1 || es[0].Record == nil || es[0].Record.Status != "IMPLEMENTING" {
		t.Fatalf("entries = %+v", es)
	}
}

func TestPublishNeedsOnlySavedStateAndNoAgent(t *testing.T) {
	// Sync has no agent, runner, or network dependency: this builds one from the
	// database and a backend alone and publishes a task that has every host-local field.
	h := newHarness(t)
	rec := fullTask()
	h.saveTask(rec)
	if err := h.s.Publish(h.ctx, rec.Key); err != nil {
		t.Fatal(err)
	}
	raw, _ := h.entries()[0].Record.Encode()
	if strings.Contains(string(raw), "SECRET") {
		t.Fatal("a host-local value reached the store")
	}
}

func TestAFailedPublishLeavesTheDigestAndTheNextRefreshRetries(t *testing.T) {
	h := newHarness(t)
	rec := fullTask()
	h.saveTask(rec)
	h.mem.Fail = errors.New("store is down")
	if err := h.s.Publish(h.ctx, rec.Key); err == nil {
		t.Fatal("must report the failure")
	}
	row, _ := h.s.row("task", ForemanID(rec.Key))
	if row.Digest != "" {
		t.Fatal("a failed publish must not record a digest")
	}
	got, _ := h.s.Tasks.Get(h.ctx, rec.Key)
	if got == nil || got.State != rec.State {
		t.Fatal("a failed publish must not change the task")
	}
	h.mem.Fail = nil
	h.pull() // the refresh republishes stale tasks
	if es := h.entries(); len(es) != 1 || es[0].Record == nil {
		t.Fatalf("the retry did not publish: %+v", es)
	}
}

func TestPublishedEntryShowsNeedsDecisionAndTheOldestQuestion(t *testing.T) {
	h := newHarness(t)
	rec := fullTask()
	h.saveTask(rec)
	h.s.Publish(h.ctx, rec.Key)
	e := h.entries()[0]
	if e.Record.StatusLabel() != "Needs decision" || e.Record.QuestionText() != "Second?" {
		t.Fatalf("status %q question %q", e.Record.StatusLabel(), e.Record.QuestionText())
	}
}

func TestBlockedPublishShowsTheReasonAndChangesTheDigest(t *testing.T) {
	h := newHarness(t)
	h.issue("L-0A1B2C3D")
	key := localissues.Key("L-0A1B2C3D", "")
	h.s.Publish(h.ctx, key)
	w := h.mem.Writes
	h.s.Blocked(h.ctx, key, "no confirmed triage")
	h.s.Blocked(h.ctx, key, "no confirmed triage")
	if h.mem.Writes != w+1 {
		t.Fatalf("writes = %d, want one more than %d", h.mem.Writes, w)
	}
}

func TestPullGivesAUserCreatedEntryANewIDAndAnInboxIssue(t *testing.T) {
	h := newHarness(t)
	ref := h.mem.UserCreate(TaskEntry{Name: "From my phone", Description: "do it", Repository: "o/r", Tags: []string{"x"}})
	h.pull()
	es := h.entries()
	if len(es) != 1 || !localissues.IDPattern.MatchString(es[0].ID) {
		t.Fatalf("entries = %+v", es)
	}
	li, err := localissues.GetIn(h.s.DB, es[0].ID)
	if err != nil || li.Title != "From my phone" || li.Description != "do it" || li.Repo != "o/r" || li.Status != localissues.Open {
		t.Fatalf("issue = %+v, %v", li, err)
	}
	if es[0].Ref != ref {
		t.Fatal("the entry must be kept, not replaced")
	}
	h.pull()
	if issues, _ := h.s.Issues.List(h.ctx); len(issues) != 1 {
		t.Fatalf("a second refresh made %d issues", len(issues))
	}
}

func TestPullSavesConfirmedTriageOnlyWhileScopeIsConfirmed(t *testing.T) {
	h := newHarness(t)
	ref := h.mem.UserCreate(TaskEntry{Name: "Task", ConfirmedTriage: "first scope"})
	h.pull()
	id := h.entries()[0].ID
	key := localissues.Key(id, "")
	if got, _ := h.s.triage(key); got != "" {
		t.Fatalf("triage saved without the box ticked: %q", got)
	}
	h.mem.UserEdit(ref, func(e *TaskEntry) { e.ScopeConfirmed = true })
	h.pull()
	if got, _ := h.s.triage(key); got != "first scope" {
		t.Fatalf("triage = %q", got)
	}
	h.mem.UserEdit(ref, func(e *TaskEntry) { e.ConfirmedTriage = "second scope" })
	h.pull()
	if got, _ := h.s.triage(key); got != "second scope" {
		t.Fatalf("a later edit must replace the scope: %q", got)
	}
	h.mem.UserEdit(ref, func(e *TaskEntry) { e.ScopeConfirmed = false })
	h.pull()
	if got, _ := h.s.triage(key); got != "second scope" {
		t.Fatalf("unticking must not remove confirmed scope: %q", got)
	}
}

func TestPullReturnsAnAnswerOnceAndNeverOneTypedBeforeTheQuestion(t *testing.T) {
	h := newHarness(t)
	rec := fullTask()
	rec.Decisions = rec.Decisions[:1] // nothing pending yet
	h.saveTask(rec)
	h.s.Publish(h.ctx, rec.Key)
	ref := h.entries()[0].Ref
	// An answer typed while no question is waiting is stale once one appears, even
	// if someone edits the entry afterwards.
	h.mem.UserEdit(ref, func(e *TaskEntry) { e.Answer = "old" })
	rec = fullTask()
	h.saveTask(rec)
	h.s.Publish(h.ctx, rec.Key)
	h.mem.UserEdit(ref, func(e *TaskEntry) { e.Tags = []string{"unrelated edit"} })
	if p := h.pull(); len(p.Answers) != 0 {
		t.Fatalf("a stale answer was returned: %+v", p.Answers)
	}
	if h.entries()[0].Answer != "" {
		t.Fatal("publishing a new question must clear an answer typed before it")
	}
	h.mem.UserEdit(ref, func(e *TaskEntry) { e.Answer = "B" })
	p := h.pull()
	if len(p.Answers) != 1 || p.Answers[0].Text != "B" || p.Answers[0].Key != rec.Key {
		t.Fatalf("answers = %+v", p.Answers)
	}
	if err := h.s.Ack(h.ctx, app.WorkspaceAck{Key: rec.Key, Kind: app.AckAnswer, Token: p.Answers[0].Token, Question: p.Answers[0].Question}); err != nil {
		t.Fatal(err)
	}
	if h.entries()[0].Answer != "" {
		t.Fatal("the answer was not cleared")
	}
	// The clear is lost (the field comes back): the answer must not apply again.
	h.mem.UserEdit(ref, func(e *TaskEntry) { e.Answer = "B" })
	if p := h.pull(); len(p.Answers) != 0 {
		t.Fatalf("an applied answer came back: %+v", p.Answers)
	}
	if h.entries()[0].Answer != "" {
		t.Fatal("the leftover answer was not cleared")
	}
}

func twoQuestionTask() *task.Record {
	rec := fullTask()
	at := rec.Decisions[1].AskedAt
	rec.Decisions = []task.Decision{
		{AskedAt: at, Question: rec.Decisions[1].Question},
		{AskedAt: at, Question: task.Question{Issue: "Other?", Options: []task.Option{{Label: "A"}, {Label: "B"}}}},
	}
	return rec
}

func TestTwoQuestionsFromOneTurnTakeTheSameAnswerTextAndAckKeepsTheNextAnswer(t *testing.T) {
	h := newHarness(t)
	rec := twoQuestionTask()
	h.saveTask(rec)
	h.s.Publish(h.ctx, rec.Key)
	ref := h.entries()[0].Ref
	h.mem.UserEdit(ref, func(e *TaskEntry) { e.Answer = "A" })
	p := h.pull()
	if len(p.Answers) != 1 {
		t.Fatalf("answers = %+v", p.Answers)
	}
	first := p.Answers[0]
	// Production order: Respond records the answer and publishes the next question,
	// then the app acks. The person answers the new question in between.
	rec.Decisions[0].Answer = "A"
	h.saveTask(rec)
	if err := h.s.Publish(h.ctx, rec.Key); err != nil {
		t.Fatal(err)
	}
	if h.entries()[0].Answer != "" {
		t.Fatal("publishing the next question must clear the old answer")
	}
	h.mem.UserEdit(ref, func(e *TaskEntry) { e.Answer = "A" })
	if err := h.s.Ack(h.ctx, app.WorkspaceAck{Key: rec.Key, Kind: app.AckAnswer, Token: first.Token, Question: first.Question}); err != nil {
		t.Fatal(err)
	}
	if h.entries()[0].Answer != "A" {
		t.Fatal("the ack of the first answer erased the answer to the second question")
	}
	p = h.pull()
	if len(p.Answers) != 1 || p.Answers[0].Token == first.Token || p.Answers[0].Question == first.Question {
		t.Fatalf("the second question's answer must be its own: %+v", p.Answers)
	}
}

func TestAnAnswerFromASnapshotOlderThanTheQuestionIsNotApplied(t *testing.T) {
	h := newHarness(t)
	rec := twoQuestionTask()
	h.saveTask(rec)
	h.s.Publish(h.ctx, rec.Key)
	ref := h.entries()[0].Ref
	h.mem.UserEdit(ref, func(e *TaskEntry) { e.Answer = "A" })
	snapshot := h.entries()[0] // taken by a refresh that has not read the task yet
	// Meanwhile another process answers the first question and publishes the second.
	rec.Decisions[0].Answer = "A"
	h.saveTask(rec)
	if err := h.s.Publish(h.ctx, rec.Key); err != nil {
		t.Fatal(err)
	}
	if got := h.s.pendingAnswer(h.ctx, snapshot, ForemanID(rec.Key), rec.Key); got != nil {
		t.Fatalf("the old snapshot's answer was handed to the new question: %+v", got)
	}
}

func TestAnAnswerTypedAfterAQuestionPublishedUnderTheEarlierKeyFormatIsStillApplied(t *testing.T) {
	h := newHarness(t)
	rec := fullTask()
	h.saveTask(rec)
	h.s.Publish(h.ctx, rec.Key)
	// The earlier revision stored this question under its own key format.
	if _, err := h.s.DB.Exec(`UPDATE workspace_entries SET question = ?`, legacyQuestionKey(rec)); err != nil {
		t.Fatal(err)
	}
	ref := h.entries()[0].Ref
	h.mem.UserEdit(ref, func(e *TaskEntry) { e.Answer = "B" })
	// A publish forced by an unrelated change must not treat the answer as stale.
	rec.LastOutcome = &task.Outcome{Status: "BLOCKED", Summary: "still waiting", At: t0}
	h.saveTask(rec)
	if err := h.s.Publish(h.ctx, rec.Key); err != nil {
		t.Fatal(err)
	}
	if h.entries()[0].Answer != "B" {
		t.Fatal("a publish after the upgrade cleared a valid answer")
	}
	if p := h.pull(); len(p.Answers) != 1 || p.Answers[0].Text != "B" {
		t.Fatalf("answers = %+v", p.Answers)
	}
}

func TestAnAnswerInTheStoreIsNotTrustedWhenTheRowCannotSayWhichQuestionWasPublished(t *testing.T) {
	// An earlier revision stored an empty key both for "no question yet" and for
	// rows it never tracked, so an answer next to one may belong to no question.
	h := newHarness(t)
	rec := fullTask()
	rec.Decisions = rec.Decisions[:1] // nothing pending
	h.saveTask(rec)
	h.s.Publish(h.ctx, rec.Key)
	if _, err := h.s.DB.Exec(`UPDATE workspace_entries SET question = ''`); err != nil {
		t.Fatal(err)
	}
	ref := h.entries()[0].Ref
	h.mem.UserEdit(ref, func(e *TaskEntry) { e.Answer = "typed for nothing" })
	// The runner saves its first question before the next successful publish.
	rec = fullTask()
	h.saveTask(rec)
	if p := h.pull(); len(p.Answers) != 0 {
		t.Fatalf("an old answer was bound to the new question: %+v", p.Answers)
	}
	if h.entries()[0].Answer != "" {
		t.Fatal("the stale answer was not cleared")
	}
}

func TestPullDoesNotUndoTriageOrIssueEditsMadeOnTheHost(t *testing.T) {
	h := newHarness(t)
	h.issue("L-0A1B2C3D")
	if _, err := h.s.DB.Exec(`INSERT INTO triage (id, decisions, confirmed_at) VALUES ('local/l-0a1b2c3d', 'Scope A', 'x')`); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Publish(h.ctx, localissues.Key("L-0A1B2C3D", "")); err != nil {
		t.Fatal(err)
	}
	h.pull() // the store still holds scope A and the old issue fields
	// `foreman issue triage` and a replacing import change the host, and publish.
	if err := h.s.saveTriage(localissues.Key("L-0A1B2C3D", ""), "Scope B"); err != nil {
		t.Fatal(err)
	}
	li := localissues.Issue{ID: "L-0A1B2C3D", Title: "Renamed on the host", Description: "new", Status: localissues.Open, CreatedAt: t0, UpdatedAt: t0.Add(time.Hour)}
	if err := localissues.PutIn(h.s.DB, li); err != nil {
		t.Fatal(err)
	}
	h.pull()
	if got, _ := h.s.triage(localissues.Key("L-0A1B2C3D", "")); got != "Scope B" {
		t.Fatalf("triage = %q: a pull restored the store's older text", got)
	}
	got, _ := localissues.GetIn(h.s.DB, "L-0A1B2C3D")
	if got.Title != "Renamed on the host" {
		t.Fatalf("title = %q: a pull restored the store's older title", got.Title)
	}
	// A person editing the store field still wins.
	h.mem.UserEdit(h.entries()[0].Ref, func(e *TaskEntry) { e.ConfirmedTriage = "Scope C"; e.ScopeConfirmed = true })
	h.pull()
	if got, _ := h.s.triage(localissues.Key("L-0A1B2C3D", "")); got != "Scope C" {
		t.Fatalf("triage = %q, want the store edit", got)
	}
}

func TestPullReturnsQueuedTasksOldestFirstAndAckClearsQueue(t *testing.T) {
	h := newHarness(t)
	b := h.mem.UserCreate(TaskEntry{Name: "Second", Queue: true})
	a := h.mem.UserCreate(TaskEntry{Name: "First", Queue: true})
	h.pull()
	h.mem.mu.Lock()
	h.mem.find(a).EditedAt = t0
	h.mem.find(b).EditedAt = t0.Add(time.Hour)
	h.mem.mu.Unlock()
	p := h.pull()
	if len(p.Queued) != 2 || p.Queued[0].QueuedAt.After(p.Queued[1].QueuedAt) {
		t.Fatalf("queued = %+v", p.Queued)
	}
	k := p.Queued[0].Key
	if err := h.s.Ack(h.ctx, app.WorkspaceAck{Key: k, Kind: app.AckQueue, Token: p.Queued[0].Token}); err != nil {
		t.Fatal(err)
	}
	queued := 0
	for _, e := range h.entries() {
		if e.Queue {
			queued++
		}
	}
	if queued != 1 {
		t.Fatalf("%d entries still queued, want 1", queued)
	}
}

func TestPullRestoresMissingWorkFromThePublishedRecord(t *testing.T) {
	// One host publishes; a second host with an empty database pulls.
	h1 := newHarness(t)
	rec := fullTask()
	h1.saveTask(rec)
	h1.issue("L-0A1B2C3D")
	h1.s.Publish(h1.ctx, rec.Key)
	h2 := newHarness(t)
	h2.mem = h1.mem
	h2.s.Backend = h1.mem
	h2.pull()
	got, _ := h2.s.taskFor(h2.ctx, localissues.Key("L-0A1B2C3D", ""))
	if got == nil {
		t.Fatal("the task was not restored")
	}
	if got.State != task.Discovered || got.Delegation != nil || got.Reset == nil || got.Reset.PR != 7 || got.Imported == nil {
		t.Fatalf("restored task must be idle with a reset mark and imported plan: %+v", got)
	}
	if li, err := localissues.GetIn(h2.s.DB, "L-0A1B2C3D"); err != nil || li.Title == "" {
		t.Fatalf("issue = %+v, %v", li, err)
	}
}

func TestPullKeepsTheOldestOfTwoEntriesWithOneForemanID(t *testing.T) {
	h := newHarness(t)
	a := h.mem.UserCreate(TaskEntry{ID: "L-0A1B2C3D", Name: "first"})
	time.Sleep(5 * time.Millisecond)
	h.mem.UserCreate(TaskEntry{ID: "L-0A1B2C3D", Name: "second"})
	h.mem.mu.Lock()
	h.mem.find(a).CreatedAt = t0
	h.mem.mu.Unlock()
	h.pull()
	es := h.entries()
	if len(es) != 1 || es[0].Name != "first" {
		t.Fatalf("entries = %+v", es)
	}
}

func snapshotOf(repo, text string) knowledge.Snapshot {
	return knowledge.Snapshot{Format: knowledge.FormatVersion, Repo: repo, NextID: 2, Evaluations: []knowledge.Evaluation{},
		Learnings: []knowledge.Learning{{ID: "K1", Kind: knowledge.Convention, Text: text, Status: knowledge.Active,
			Evidence: []knowledge.Evidence{{Task: "t1", Source: knowledge.Maintainer}}}}}
}

func TestPullAdoptsAChangedKnowledgeEntry(t *testing.T) {
	h := newHarness(t)
	if err := h.know.Replace("o/r", snapshotOf("o/r", "Use tabs.")); err != nil {
		t.Fatal(err)
	}
	h.pull() // publishes the host's copy
	if es, _ := h.mem.ListKnowledge(h.ctx); len(es) != 1 {
		t.Fatalf("knowledge entries = %d", len(es))
	}
	h.mem.WriteKnowledge(h.ctx, KnowledgePublish{Repo: "o/r", Snapshot: snapshotOf("o/r", "Use spaces.")})
	h.pull()
	got, _ := h.know.Snapshot("o/r")
	if len(got.Learnings) != 1 || got.Learnings[0].Text != "Use spaces." {
		t.Fatalf("the edited entry was not adopted: %+v", got.Learnings)
	}
}

func TestPullReportsAnUnparseableKnowledgeEntryAndPausesPublishing(t *testing.T) {
	h := newHarness(t)
	h.know.Replace("o/r", snapshotOf("o/r", "Use tabs."))
	h.pull()
	h.mem.UserCorruptKnowledge("o/r", errors.New("broken"))
	h.pull()
	got, _ := h.know.Snapshot("o/r")
	if got.Learnings[0].Text != "Use tabs." {
		t.Fatal("a broken entry must not be adopted")
	}
	if probs := h.s.Problems(); len(probs) == 0 || !strings.Contains(strings.Join(probs, "\n"), "o/r") {
		t.Fatalf("problems = %v", probs)
	}
	// A new local change must not overwrite the broken entry.
	h.know.Replace("o/r", snapshotOf("o/r", "Use tabs, always."))
	w := h.mem.Writes
	if err := h.s.PublishKnowledge(h.ctx, "o/r"); err != nil || h.mem.Writes != w {
		t.Fatalf("publishing must pause: err %v, writes %d -> %d", err, w, h.mem.Writes)
	}
	// Fixed: publishing resumes.
	h.mem.WriteKnowledge(h.ctx, KnowledgePublish{Repo: "o/r", Snapshot: snapshotOf("o/r", "Use tabs.")})
	h.pull()
	if err := h.s.PublishKnowledge(h.ctx, "o/r"); err != nil {
		t.Fatal(err)
	}
}

func TestPublishesOfOneTaskFromManyCallersAreSerialized(t *testing.T) {
	h := newHarness(t)
	rec := fullTask()
	h.saveTask(rec)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- h.s.Publish(h.ctx, rec.Key)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if h.mem.Writes != 1 || len(h.entries()) != 1 {
		t.Fatalf("writes = %d, entries = %d; concurrent publishes must write the entry once", h.mem.Writes, len(h.entries()))
	}
}

func TestAKnowledgeEntryWithNoPayloadIsPublishedAgainNotPaused(t *testing.T) {
	h := newHarness(t)
	if err := h.know.Replace("o/r", snapshotOf("o/r", "Use tabs.")); err != nil {
		t.Fatal(err)
	}
	// A first publish created the page and then failed before the payload landed.
	h.mem.UserCorruptKnowledge("o/r", ErrNoPayload)
	h.pull()
	ks, _ := h.mem.ListKnowledge(h.ctx)
	if len(ks) != 1 || ks[0].Snapshot == nil {
		t.Fatalf("the snapshot was not published again: %+v", ks)
	}
	if row, _ := h.s.row("knowledge", "o/r"); row.AnswerDigest == pausedMark {
		t.Fatal("an unfinished publish must not pause the repository")
	}
}

func TestAHalfWrittenEntryIsNotReadBackAsAnEditAndTheNextPublishCompletesIt(t *testing.T) {
	h := newHarness(t)
	li := localissues.Issue{ID: "L-0A1B2C3D", Title: "Add storage", Description: "first half and second half of a long description", Status: localissues.Open, CreatedAt: t0, UpdatedAt: t0}
	if err := localissues.PutIn(h.s.DB, li); err != nil {
		t.Fatal(err)
	}
	h.mem.PartialOnWrite = 1
	if err := h.s.Publish(h.ctx, localissues.Key(li.ID, "")); err == nil {
		t.Fatal("the dropped write must be reported")
	}
	if got := h.entries()[0].Description; got == li.Description {
		t.Fatal("the test needs a half written entry")
	}
	h.mem.PartialOnWrite = 0
	h.s.LockWait = time.Second
	// The pull reads the partial description first; it must not replace the host's.
	h.s.Backend = &noRepublish{h.mem}
	h.pull()
	got, _ := localissues.GetIn(h.s.DB, li.ID)
	if got.Description != li.Description {
		t.Fatalf("the host's description became %q", got.Description)
	}
	h.s.Backend = h.mem
	if err := h.s.Publish(h.ctx, localissues.Key(li.ID, "")); err != nil {
		t.Fatal(err)
	}
	if d := h.entries()[0].Description; d != li.Description {
		t.Fatalf("the entry holds %q, want the full description", d)
	}
}

// noRepublish is a backend whose writes do nothing, so a pull shows what it reads
// before any republish could repair the entry.
type noRepublish struct{ *Memory }

func (noRepublish) UpsertTask(context.Context, TaskPublish) (string, error) {
	return "", errors.New("no writes during this pull")
}

func TestAnAnswerTypedForAQuestionAnUntrackedRowAlreadyPublishedIsStillApplied(t *testing.T) {
	h := newHarness(t)
	rec := fullTask()
	h.saveTask(rec)
	h.s.Publish(h.ctx, rec.Key)
	// A row written before questions were tracked, for the record the store holds now.
	if _, err := h.s.DB.Exec(`UPDATE workspace_entries SET question = ''`); err != nil {
		t.Fatal(err)
	}
	h.mem.UserEdit(h.entries()[0].Ref, func(e *TaskEntry) { e.Answer = "B" })
	p := h.pull()
	if len(p.Answers) != 1 || p.Answers[0].Text != "B" {
		t.Fatalf("answers = %+v: the reply to a question the store already showed was dropped", p.Answers)
	}
	if h.entries()[0].Answer != "B" {
		t.Fatal("the republish cleared a valid answer")
	}
}
