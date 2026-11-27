package workspace

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/localissues"
	"github.com/brymartinez/foreman/internal/task"
)

func (h *harness) mover() Move {
	h.s.LockWait = 50 * 1e6 // 50ms: a held move lock must fail fast
	return Move{Sync: h.s, Storage: notionCfg()}
}

// seedWorkspace fills the database with a local task, a GitHub task, a local issue
// with no task, confirmed triage, and knowledge for one repository.
func (h *harness) seedWorkspace() {
	h.t.Helper()
	local := fullTask()
	local.Delegation.RunnerPID, local.Delegation.Launch = 0, nil
	gh := fullTask()
	gh.Key = task.Key{Repo: "o/r", Number: 5}
	gh.Identifier = "o/r#5"
	gh.Delegation.RunnerPID, gh.Delegation.Launch = 0, nil
	gh.State = task.Implementing
	h.saveTask(local)
	h.saveTask(gh)
	h.issue("L-0A1B2C3D")
	h.issue("L-0B0B0B0B")
	if _, err := h.s.DB.Exec(`INSERT INTO triage (id, decisions, confirmed_at) VALUES ('local/l-0b0b0b0b', 'Do the thing.', 'x')`); err != nil {
		h.t.Fatal(err)
	}
	if err := h.know.Replace("o/r", snapshotOf("o/r", "Use tabs.")); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) marker() *Marker {
	h.t.Helper()
	m, err := ReadMarker(h.s.DB)
	if err != nil {
		h.t.Fatal(err)
	}
	return m
}

func TestMoveCopiesEveryEntryVerifiesItAndWritesTheMarker(t *testing.T) {
	h := newHarness(t)
	h.seedWorkspace()
	res, err := h.mover().Run(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Tasks != 2 || res.Issues != 1 || res.Repositories != 1 {
		t.Fatalf("result = %+v", res)
	}
	got := map[string]TaskEntry{}
	for _, e := range h.entries() {
		got[e.ID] = e
		if e.Queue {
			t.Errorf("%s: the move must not set Queue", e.ID)
		}
	}
	for _, id := range []string{"L-0A1B2C3D", "o/r#5", "L-0B0B0B0B"} {
		if got[id].Record == nil {
			t.Errorf("%s is missing from the store", id)
		}
	}
	if got["L-0B0B0B0B"].Record.Status != StatusInbox || got["L-0B0B0B0B"].Record.Triage != "Do the thing." {
		t.Errorf("issue without a task = %+v", got["L-0B0B0B0B"].Record)
	}
	if got["o/r#5"].Record.Status != string(task.Implementing) {
		t.Errorf("status = %q", got["o/r#5"].Record.Status)
	}
	if ks, _ := h.mem.ListKnowledge(h.ctx); len(ks) != 1 || ks[0].Digest != SnapshotDigest(mustSnapshot(t, h)) {
		t.Errorf("knowledge = %+v", ks)
	}
	m := h.marker()
	if m == nil || m.Backend != config.BackendNotion || m.Target != Target(notionCfg()) || m.Tasks != 2 || m.Issues != 1 || m.Repositories != 1 {
		t.Fatalf("marker = %+v", m)
	}
}

func mustSnapshot(t *testing.T, h *harness) (s knowledge.Snapshot) {
	t.Helper()
	s, err := h.know.Snapshot("o/r")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMoveRetryAfterAFailurePartwayMakesNoDuplicates(t *testing.T) {
	h := newHarness(t)
	h.seedWorkspace()
	h.mem.FailOnWrite = 2
	if _, err := h.mover().Run(h.ctx); err == nil {
		t.Fatal("the dropped write must fail the move")
	}
	if h.marker() != nil {
		t.Fatal("a failed move must leave no marker")
	}
	if _, err := h.mover().Run(h.ctx); err != nil {
		t.Fatalf("retry: %v", err)
	}
	seen := map[string]int{}
	for _, e := range h.entries() {
		seen[e.ID]++
	}
	if len(seen) != 3 {
		t.Fatalf("entries = %v", seen)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("%s has %d entries", id, n)
		}
	}
	if h.marker() == nil {
		t.Fatal("the retry must write the marker")
	}
}

func TestMoveFailsVerificationOnOneWrongFieldAndWritesNoMarker(t *testing.T) {
	h := newHarness(t)
	h.seedWorkspace()
	h.mem.Mangle = func(e *TaskEntry) {
		if e.ID == "o/r#5" {
			e.Record.Status = "DONE"
		}
	}
	_, err := h.mover().Run(h.ctx)
	if err == nil || !strings.Contains(err.Error(), "o/r#5") {
		t.Fatalf("err = %v, want one naming o/r#5", err)
	}
	if strings.Contains(err.Error(), "L-0A1B2C3D") {
		t.Fatalf("err names an entry that matches: %v", err)
	}
	if h.marker() != nil {
		t.Fatal("a failed verification must leave no marker")
	}
}

func TestMoveFailsVerificationWhenAnEntryIsMissing(t *testing.T) {
	h := newHarness(t)
	h.seedWorkspace()
	h.mem.Mangle = func(e *TaskEntry) {
		if e.ID == "L-0B0B0B0B" {
			e.ID = ""
		}
	}
	if _, err := h.mover().Run(h.ctx); err == nil || !strings.Contains(err.Error(), "L-0B0B0B0B") {
		t.Fatalf("err = %v", err)
	}
	if h.marker() != nil {
		t.Fatal("no marker expected")
	}
}

func TestMoveRefusesBeforeAnyWriteWhileWorkIsLive(t *testing.T) {
	for name, arrange := range map[string]func(*harness){
		"a runner is alive": func(h *harness) {
			rec := h.githubTask()
			rec.Delegation.RunnerPID = os.Getpid()
			h.saveTask(rec)
		},
		"a launch claim is live": func(h *harness) {
			rec := h.githubTask()
			rec.Delegation.Launch = &task.Launch{Token: "t", PID: os.Getpid()}
			h.saveTask(rec)
		},
		"a learner holds the knowledge lock": func(h *harness) {
			unlock, err := h.know.Lock("o/r")
			if err != nil {
				h.t.Fatal(err)
			}
			h.t.Cleanup(unlock)
		},
		"another move holds the move lock": func(h *harness) {
			if _, err := h.s.DB.Exec(`INSERT INTO locks (name, pid, token, since) VALUES (?, ?, 'x', 'x')`, MoveLock, os.Getpid()); err != nil {
				h.t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.seedWorkspace()
			arrange(h)
			if _, err := h.mover().Run(h.ctx); err == nil {
				t.Fatal("the move must refuse")
			}
			if h.mem.Writes != 0 || len(h.entries()) != 0 {
				t.Fatalf("writes = %d, entries = %d: the move changed the store", h.mem.Writes, len(h.entries()))
			}
			if h.marker() != nil {
				t.Fatal("no marker expected")
			}
		})
	}
}

func TestMoveStopsWhenTheCheckFails(t *testing.T) {
	h := newHarness(t)
	h.seedWorkspace()
	h.mem.Fail = os.ErrDeadlineExceeded
	if _, err := h.mover().Run(h.ctx); err == nil {
		t.Fatal("a failed check must stop the move")
	}
	if h.marker() != nil || h.mem.Writes != 0 {
		t.Fatal("the move changed something after a failed check")
	}
}

func TestMoveAfterTheCutoverChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.seedWorkspace()
	if _, err := h.mover().Run(h.ctx); err != nil {
		t.Fatal(err)
	}
	writes := h.mem.Writes
	res, err := h.mover().Run(h.ctx)
	if err != nil || !res.AlreadyMoved {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if h.mem.Writes != writes {
		t.Fatal("the second move wrote to the store")
	}
}

func TestMoveKeepsTheOldestOfTwoPagesWithOneForemanID(t *testing.T) {
	h := newHarness(t)
	h.seedWorkspace()
	oldest := h.mem.UserCreate(TaskEntry{ID: "o/r#5"})
	h.mem.UserCreate(TaskEntry{ID: "o/r#5"})
	if _, err := h.mover().Run(h.ctx); err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, e := range h.entries() {
		if e.ID == "o/r#5" {
			refs = append(refs, e.Ref)
		}
	}
	if len(refs) != 1 || refs[0] != oldest {
		t.Fatalf("refs = %v, want only %s", refs, oldest)
	}
}

func (h *harness) githubTask() *task.Record {
	h.t.Helper()
	recs, err := h.s.Tasks.List(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, r := range recs {
		if r.Source != task.Local {
			return r
		}
	}
	h.t.Fatal("no GitHub task saved")
	return nil
}

func TestMoveRetryToAnotherDestinationCopiesEverythingAgain(t *testing.T) {
	h := newHarness(t)
	h.seedWorkspace()
	h.mem.FailOnWrite = 3 // two tasks reach the first destination, then the move fails
	if _, err := h.mover().Run(h.ctx); err == nil {
		t.Fatal("the dropped write must fail the move")
	}
	elsewhere := &Memory{Now: h.mem.Now}
	h.s.Backend = elsewhere
	if _, err := h.mover().Run(h.ctx); err != nil {
		t.Fatalf("a retry against a new destination must copy what the first one already got: %v", err)
	}
	if es, _ := elsewhere.ListTasks(h.ctx); len(es) != 3 {
		t.Fatalf("the new destination holds %d entries, want 3", len(es))
	}
	if ks, _ := elsewhere.ListKnowledge(h.ctx); len(ks) != 1 {
		t.Fatalf("the new destination holds %d knowledge entries, want 1", len(ks))
	}
}

func TestMoveFailsWhenTheStoreCutsTheConfirmedScopeShort(t *testing.T) {
	h := newHarness(t)
	h.seedWorkspace()
	h.mem.Mangle = func(e *TaskEntry) {
		if e.ID == "L-0B0B0B0B" {
			e.ConfirmedTriage = "Do the"
		}
	}
	if _, err := h.mover().Run(h.ctx); err == nil || !strings.Contains(err.Error(), "L-0B0B0B0B") {
		t.Fatalf("err = %v", err)
	}
	if h.marker() != nil {
		t.Fatal("no marker expected")
	}
}

func TestMoveRetryToTheSameDestinationCarriesScopeChangedInSQLite(t *testing.T) {
	h := newHarness(t)
	h.seedWorkspace()
	h.mem.FailOnWrite = 4 // the two tasks and the issue copy; the knowledge write drops
	if _, err := h.mover().Run(h.ctx); err == nil {
		t.Fatal("the dropped write must fail the move")
	}
	if _, err := h.s.DB.Exec(`UPDATE triage SET decisions = 'Changed scope.' WHERE id = 'local/l-0b0b0b0b'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.mover().Run(h.ctx); err != nil {
		t.Fatalf("the retry must not fail on the scope changed since the first attempt: %v", err)
	}
	for _, e := range h.entries() {
		if e.ID == "L-0B0B0B0B" && e.ConfirmedTriage != "Changed scope." {
			t.Fatalf("triage = %q", e.ConfirmedTriage)
		}
	}
}

func TestAMoveRetriedAfterAHalfWrittenDescriptionCompletesItAndPullKeepsTheLocalText(t *testing.T) {
	h := newHarness(t)
	h.seedWorkspace()
	if _, err := h.s.DB.Exec(`UPDATE local_issues SET description = ? WHERE id = 'L-0B0B0B0B'`, "the full description of the issue"); err != nil {
		t.Fatal(err)
	}
	h.mem.PartialOnWrite = 3 // the issue without a task is the third entry copied
	if _, err := h.mover().Run(h.ctx); err == nil {
		t.Fatal("the first attempt must fail")
	}
	h.mem.PartialOnWrite = 0
	if _, err := h.mover().Run(h.ctx); err != nil {
		t.Fatalf("the retry must complete the description: %v", err)
	}
	for _, e := range h.entries() {
		if e.ID == "L-0B0B0B0B" && e.Description != "the full description of the issue" {
			t.Fatalf("the page holds %q after the move reported success", e.Description)
		}
	}
	h.pull()
	li, err := localissues.GetIn(h.s.DB, "L-0B0B0B0B")
	if err != nil || li.Description != "the full description of the issue" {
		t.Fatalf("the local issue is %+v, %v", li, err)
	}
}

func TestAMoveRefusesAnEntryWhoseDescriptionDiffersFromTheIssue(t *testing.T) {
	h := newHarness(t)
	h.seedWorkspace()
	h.mem.Mangle = func(e *TaskEntry) {
		if e.ID == "L-0B0B0B0B" {
			e.Description = "trunc"
		}
	}
	if _, err := h.mover().Run(h.ctx); err == nil || !strings.Contains(err.Error(), "description") {
		t.Fatalf("a truncated description passed verification: %v", err)
	}
}

func TestAMoveThatWaitedForTheLockStopsWhenAnotherMoveFinishedFirst(t *testing.T) {
	h := newHarness(t)
	h.seedWorkspace()
	m := h.mover()
	h.s.LockWait = 5 * time.Second
	unlock, err := h.s.lock(h.ctx, MoveLock)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := m.Run(h.ctx); done <- err }()
	time.Sleep(200 * time.Millisecond) // the second move has read an empty marker and waits
	if err := WriteMarker(h.s.DB, Marker{Backend: "obsidian", Target: "/elsewhere"}); err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "already moved") {
		t.Fatalf("err = %v, want a refusal because the workspace already moved", err)
	}
	if mk := h.marker(); mk.Backend != "obsidian" || mk.Target != "/elsewhere" {
		t.Fatalf("marker = %+v: the second move overwrote the first", mk)
	}
	if h.mem.Writes != 0 {
		t.Fatal("the second move wrote to its store")
	}
}

func TestMoveRetryUsesChangedIssueFieldsAndVerifiesTheScopeFlag(t *testing.T) {
	h := newHarness(t)
	h.seedWorkspace()
	h.mem.FailOnWrite = 4
	if _, err := h.mover().Run(h.ctx); err == nil {
		t.Fatal("the first copy must fail")
	}
	if _, err := h.s.DB.Exec(`UPDATE local_issues SET title = ?, description = ?, repo = ?, source_url = ?, tags = ? WHERE id = 'L-0B0B0B0B'`,
		"New title", "Rewritten description", "o/new", "https://example.com/issue", `["a,b"]`); err != nil {
		t.Fatal(err)
	}
	h.mem.Mangle = func(e *TaskEntry) {
		if e.ID == "L-0B0B0B0B" {
			e.ScopeConfirmed = false
		}
	}
	if _, err := h.mover().Run(h.ctx); err == nil || h.marker() != nil {
		t.Fatal("a missing confirmation must keep the workspace on SQLite")
	}
	h.mem.Mangle = nil
	if _, err := h.mover().Run(h.ctx); err != nil {
		t.Fatalf("retry failed to use the rewritten SQLite issue: %v", err)
	}
	h.pull()
	li, err := localissues.GetIn(h.s.DB, "L-0B0B0B0B")
	if err != nil || li.Title != "New title" || li.Description != "Rewritten description" || li.Repo != "o/new" ||
		li.SourceURL != "https://example.com/issue" || len(li.Tags) != 1 || li.Tags[0] != "a,b" {
		t.Fatalf("issue after first sync = %+v, %v", li, err)
	}
}
