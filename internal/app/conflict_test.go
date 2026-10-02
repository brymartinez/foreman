package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// conflicting has GitHub report PR #912 with mergeability m against main at base.
func conflicting(f fixture, m, base string) {
	pr := f.gh.prs[912]
	pr.Mergeable = m
	f.gh.prs[912] = pr
	f.gh.baseHeads = map[string]string{"main": base}
}

// pushHead plays the agent pushing head to PR #912.
func pushHead(f fixture, head string) {
	pr := f.gh.prs[912]
	pr.HeadSHA = head
	f.gh.prs[912] = pr
}

// repairing runs a READY_FOR_HUMAN task into a conflict repair the gate has started.
func repairing(t *testing.T, f fixture) task.Key {
	t.Helper()
	key := ready(t, f)
	conflicting(f, Conflicting, "ba5e001")
	refresh(t, f)
	if got := f.store.recs[key]; got.State != task.Working || !got.Delegation.ConflictPending {
		t.Fatalf("setup: state %s pending %v", got.State, got.Delegation.ConflictPending)
	}
	return key
}

func TestGateStartsRepairOnConflict(t *testing.T) {
	f := newFixture()
	key := ready(t, f)
	spawned := len(f.runners.spawned)
	conflicting(f, Conflicting, "ba5e001")
	refresh(t, f)
	got := f.store.recs[key]
	d := got.Delegation
	if got.State != task.Working || !d.ConflictPending || d.LastConflict() != (task.ConflictAttempt{Head: "c0ffee1", Base: "ba5e001"}) || got.CI != nil {
		t.Fatalf("state %s delegation %+v ci %+v", got.State, d, got.CI)
	}
	if got.LastOutcome.Status != "conflict_repair" || got.LastOutcome.Summary != "PR #912 conflicts with main; claude is repairing it" {
		t.Errorf("outcome = %+v", got.LastOutcome)
	}
	if len(f.runners.spawned) != spawned+1 || d.RunnerPID != 4242 {
		t.Errorf("spawned %d (was %d) pid %d", len(f.runners.spawned), spawned, d.RunnerPID)
	}
}

func TestGateWaitsOnUnknownMergeability(t *testing.T) {
	for _, m := range []string{"unknown", Mergeable} {
		f := newFixture()
		key := ready(t, f)
		spawned := len(f.runners.spawned)
		conflicting(f, m, "ba5e001")
		f.gh.baseHeadErr = errors.New("must not read the base head")
		_, warnings, err := f.app.Status(context.Background())
		if err != nil || len(warnings) != 0 {
			t.Fatalf("%s: %v %v", m, warnings, err)
		}
		if got := f.store.recs[key]; got.State != task.ReadyForHuman || got.Delegation.ConflictAttempts != nil || len(f.runners.spawned) != spawned {
			t.Errorf("%s: state %s delegation %+v", m, got.State, got.Delegation)
		}
	}
}

func TestGateSkipsNonReadyStates(t *testing.T) {
	cases := map[string]func(f fixture, rec *task.Record){
		"comments waiting": func(f fixture, rec *task.Record) {
			f.gh.addPRComment(912, prComment(1, "Cap the retries."))
		},
		"live runner": func(f fixture, rec *task.Record) {
			rec.Delegation.RunnerPID = 77
			f.runners.alive[77] = true
		},
		"needs decision": func(f fixture, rec *task.Record) { rec.State = task.NeedsDecision },
		"failed":         func(f fixture, rec *task.Record) { rec.State = task.Failed },
		"ci pending": func(f fixture, rec *task.Record) {
			rec.State = task.Reviewing
			setCI(f, task.CIStatus{State: task.CIPending})
		},
	}
	for name, setup := range cases {
		f := newFixture()
		key := ready(t, f)
		spawned := len(f.runners.spawned)
		conflicting(f, Conflicting, "ba5e001")
		setup(f, f.store.recs[key])
		refresh(t, f)
		got := f.store.recs[key]
		if got.State == task.Working || got.Delegation.ConflictAttempts != nil || len(f.runners.spawned) != spawned {
			t.Errorf("%s: state %s delegation %+v spawned %d", name, got.State, got.Delegation, len(f.runners.spawned)-spawned)
		}
	}
}

func TestGateWarnsWhenTheBaseHeadCannotBeRead(t *testing.T) {
	f := newFixture()
	key := ready(t, f)
	conflicting(f, Conflicting, "ba5e001")
	f.gh.baseHeadErr = errors.New("HTTP 502")
	_, warnings, err := f.app.Status(context.Background())
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "base main") {
		t.Fatalf("warnings %v err %v", warnings, err)
	}
	if got := f.store.recs[key]; got.State != task.ReadyForHuman || got.Delegation.ConflictAttempts != nil {
		t.Errorf("state %s delegation %+v", got.State, got.Delegation)
	}
}

// handBack plays a spent repair ending with the task back in the developer's hands.
func handBack(f fixture, key task.Key) {
	rec := f.store.recs[key]
	rec.State = task.ReadyForHuman
	rec.Delegation.ConflictPending = false
	rec.Delegation.RunnerPID = 0
}

func TestGateDoesNotDoubleLaunch(t *testing.T) {
	f := newFixture()
	key := repairing(t, f)
	spawned := len(f.runners.spawned)
	f.runners.alive[4242] = true
	refresh(t, f)
	refresh(t, f)
	if got := f.store.recs[key]; got.State != task.Working || len(f.runners.spawned) != spawned {
		t.Errorf("state %s spawned %d more", got.State, len(f.runners.spawned)-spawned)
	}
}

func TestGateRepairsOncePerHeadPair(t *testing.T) {
	f := newFixture()
	key := repairing(t, f)
	spawned := len(f.runners.spawned)
	handBack(f, key)
	refresh(t, f)
	if got := f.store.recs[key]; got.State != task.ReadyForHuman || len(f.runners.spawned) != spawned {
		t.Errorf("state %s spawned %d more", got.State, len(f.runners.spawned)-spawned)
	}
}

func TestGateRepairsAgainAfterBaseMoves(t *testing.T) {
	f := newFixture()
	key := repairing(t, f)
	spawned := len(f.runners.spawned)
	handBack(f, key)
	conflicting(f, Conflicting, "ba5e002")
	refresh(t, f)
	got := f.store.recs[key]
	if got.State != task.Working || got.Delegation.LastConflict().Base != "ba5e002" || len(f.runners.spawned) != spawned+1 {
		t.Fatalf("new base: state %s delegation %+v", got.State, got.Delegation)
	}

	handBack(f, key)
	pushHead(f, "c0ffee2")
	refresh(t, f)
	got = f.store.recs[key]
	if got.State != task.Working || got.Delegation.LastConflict().Head != "c0ffee2" || len(f.runners.spawned) != spawned+2 {
		t.Fatalf("new head: state %s delegation %+v", got.State, got.Delegation)
	}
}

func TestGateNeverRepeatsAnEarlierPair(t *testing.T) {
	f := newFixture()
	key := repairing(t, f)
	handBack(f, key)
	conflicting(f, Conflicting, "ba5e002")
	refresh(t, f)
	spawned := len(f.runners.spawned)
	// The base goes back to the commit the first attempt was for.
	handBack(f, key)
	conflicting(f, Conflicting, "ba5e001")
	refresh(t, f)
	got := f.store.recs[key]
	if got.State != task.ReadyForHuman || len(f.runners.spawned) != spawned || len(got.Delegation.ConflictAttempts) != 2 {
		t.Errorf("state %s spawned %d more attempts %+v", got.State, len(f.runners.spawned)-spawned, got.Delegation.ConflictAttempts)
	}
}

func TestStartKeepsTheRepair(t *testing.T) {
	for _, pending := range []bool{false, true} {
		f := newFixture()
		key := repairing(t, f)
		rec := f.store.recs[key]
		rec.State, rec.Delegation.RunnerPID, rec.Delegation.ConflictPending = task.Failed, 0, pending
		if _, err := f.app.Start(context.Background(), startReq(t)); err != nil {
			t.Fatal(err)
		}
		d := f.store.recs[key].Delegation
		if !d.ConflictAttempted("c0ffee1", "ba5e001") || d.ConflictPending != pending {
			t.Errorf("pending %v: delegation = %+v", pending, d)
		}
	}
}

func TestRepairResumesImplementer(t *testing.T) {
	f := newFixture()
	key := repairing(t, f)
	f.claude.onResume = func() { pushHead(f, "merged1") }
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(f.claude.resumed) != 1 || f.claude.starts != 1 {
		t.Fatalf("resumed %d starts %d", len(f.claude.resumed), f.claude.starts)
	}
	msg := f.claude.resumed[0]
	for _, want := range []string{"sid-1: ", "Pull request #912 now has merge conflicts with main.", "git merge origin/main", "ba5e001"} {
		if !strings.Contains(msg, want) {
			t.Errorf("resume message lacks %q:\n%s", want, msg)
		}
	}
}

func TestRepairFallsBackToFreshTurn(t *testing.T) {
	f := newFixture()
	key := repairing(t, f)
	f.claude.resumeErr = errors.Join(errors.New("gone"), agent.ErrNoSession)
	f.claude.during = func() { pushHead(f, "merged1") }
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.claude.starts != 2 || !strings.Contains(f.claude.prompt, "## Merge conflict") || !strings.Contains(f.claude.prompt, "git merge origin/main") {
		t.Fatalf("starts %d prompt:\n%s", f.claude.starts, f.claude.prompt)
	}
	if got := f.store.recs[key]; got.State != task.Reviewing {
		t.Errorf("state %s", got.State)
	}
}

func TestRepairSuccessGoesToReviewThenReady(t *testing.T) {
	f := newFixture()
	key := repairing(t, f)
	reviews := len(f.codex.reviewed)
	f.claude.onResume = func() { pushHead(f, "merged1") }
	var contains []string
	f.wt.contains = func(dir, ancestor, descendant string) (bool, error) {
		contains = append(contains, dir+" "+ancestor+" "+descendant)
		return true, nil
	}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	d := got.Delegation
	if got.State != task.Reviewing || d.PRHead != "merged1" || d.ConflictPending || d.RunnerPID != 0 {
		t.Fatalf("state %s delegation %+v", got.State, d)
	}
	if len(contains) != 1 || contains[0] != d.Worktree+" ba5e001 merged1" {
		t.Errorf("contains calls = %q", contains)
	}
	if len(f.codex.reviewed) != reviews+1 || got.LatestReview().Head != "merged1" || !got.LatestReview().Clean {
		t.Fatalf("reviews %d latest %+v", len(f.codex.reviewed)-reviews, got.LatestReview())
	}
	// The gate applies CI as for any clean review, and the merge left no conflict.
	conflicting(f, Mergeable, "ba5e001")
	refresh(t, f)
	if got := f.store.recs[key]; got.State != task.ReadyForHuman {
		t.Errorf("after refresh: state %s", got.State)
	}
}

func assertNeedsDecision(t *testing.T, f fixture, key task.Key, want string) {
	t.Helper()
	got := f.store.recs[key]
	d := got.Delegation
	if got.State != task.NeedsDecision || d.ConflictPending || d.RunnerPID != 0 || !d.ConflictAttempted("c0ffee1", "ba5e001") {
		t.Fatalf("state %s delegation %+v", got.State, d)
	}
	if !strings.Contains(got.LastOutcome.Summary, want) || got.Pending() == nil {
		t.Fatalf("outcome %+v pending %+v", got.LastOutcome, got.Pending())
	}
	if len(f.wt.removed) != 0 {
		t.Errorf("worktree removed: %+v", f.wt.removed)
	}
	// A refresh does not try the same heads again.
	spawned := len(f.runners.spawned)
	refresh(t, f)
	if len(f.runners.spawned) != spawned {
		t.Errorf("a refresh relaunched the spent repair")
	}
}

func TestRepairBlockedNeedsDecision(t *testing.T) {
	f := newFixture()
	key := repairing(t, f)
	f.claude.turn = agent.Turn{Session: task.Session{Agent: task.Claude, ID: "sid-1"}, Outcome: agent.Outcome{
		Status: agent.Blocked, Summary: "both sides rename Retry",
		Questions: []task.Question{{Issue: "Keep which name?", Options: []task.Option{{Label: "A", Description: "Retry"}, {Label: "B", Description: "Backoff"}}, Recommendation: "A"}},
	}}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	assertNeedsDecision(t, f, key, "merge conflict on PR #912 needs a decision: both sides rename Retry")
	if got := f.store.recs[key]; got.Pending().Question.Issue != "Keep which name?" {
		t.Errorf("question = %+v", got.Pending().Question)
	}
}

func TestRepairFailedNeedsDecision(t *testing.T) {
	cases := map[string]func(f fixture){
		"failed outcome": func(f fixture) {
			f.claude.turn = agent.Turn{Session: task.Session{Agent: task.Claude, ID: "sid-1"}, Outcome: agent.Outcome{Status: agent.FailedRun, Summary: "tests fail after the merge"}}
		},
		"runner error": func(f fixture) { f.claude.err = errors.New("claude exited 1") },
		"head did not move": func(f fixture) {
			f.claude.turn = prTurn("sid-1")
		},
		"validation failed": func(f fixture) {
			turn := prTurn("sid-1")
			turn.Outcome.Validation = []task.Check{{Command: "go vet ./...", Passed: true}, {Command: "go test ./...", Passed: false}}
			f.claude.turn = turn
			f.claude.onResume = func() { pushHead(f, "merged1") }
		},
	}
	want := map[string]string{
		"failed outcome":    "not repaired: tests fail after the merge",
		"runner error":      "not repaired: claude exited 1",
		"head did not move": "not repaired: PR #912 head is still c0ffee1",
		"validation failed": "not repaired: validation failed after the merge: go test ./...",
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			key := repairing(t, f)
			setup(f)
			if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
				t.Fatal(err)
			}
			assertNeedsDecision(t, f, key, want[name])
			q := f.store.recs[key].Pending().Question
			if err := q.Validate(); err != nil || !strings.Contains(q.Options[0].Description, "Merge origin/main") {
				t.Errorf("question %+v: %v", q, err)
			}
		})
	}
}

func TestRepairWithoutBaseMergeNeedsDecision(t *testing.T) {
	f := newFixture()
	key := repairing(t, f)
	reviews := len(f.codex.reviewed)
	f.claude.onResume = func() { pushHead(f, "merged1") }
	f.wt.contains = func(string, string, string) (bool, error) { return false, nil }
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	assertNeedsDecision(t, f, key, "does not include base commit ba5e001")
	if len(f.codex.reviewed) != reviews {
		t.Errorf("an unmerged head was reviewed")
	}
}

func TestRepairAnsweredResumesWithTheDecision(t *testing.T) {
	f := newFixture()
	key := repairing(t, f)
	f.claude.err = errors.New("claude exited 1")
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	f.claude.err = nil
	if _, err := f.app.Respond(context.Background(), key, ""); err != nil {
		t.Fatal(err)
	}
	f.claude.onResume = func() { pushHead(f, "merged1") }
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	last := f.claude.resumed[len(f.claude.resumed)-1]
	if !strings.Contains(last, "Decision: A. Merge origin/main") || strings.Contains(last, "now has merge conflicts") {
		t.Errorf("resume message:\n%s", last)
	}
	if got := f.store.recs[key]; got.State != task.Reviewing || got.Delegation.PRHead != "merged1" {
		t.Errorf("state %s head %s", got.State, got.Delegation.PRHead)
	}
}

func TestRepairRunnerThatCannotStartNeedsDecision(t *testing.T) {
	f := newFixture()
	key := ready(t, f)
	conflicting(f, Conflicting, "ba5e001")
	f.runners.spawnErr = errors.New("fork: resource temporarily unavailable")
	refresh(t, f)
	f.runners.spawnErr = nil
	assertNeedsDecision(t, f, key, "not repaired: could not launch runner: fork: resource temporarily unavailable")
}

func TestRepairRunnerThatDiesNeedsDecision(t *testing.T) {
	f := newFixture()
	key := repairing(t, f)
	// The runner holds pid 4242, which is not alive, so the next refresh reaps it.
	refresh(t, f)
	assertNeedsDecision(t, f, key, "not repaired: runner (pid 4242) exited without recording an outcome")
}

func TestRepairCancelledStaysFailed(t *testing.T) {
	f := newFixture()
	key := repairing(t, f)
	ctx, cancel := context.WithCancelCause(context.Background())
	f.claude.err = errors.New("claude printed no result (signal: terminated)")
	f.claude.onResume = func() { cancel(errors.New("terminated signal received")) }
	if err := f.app.RunTask(ctx, key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || got.LastOutcome.Status != Cancelled || got.Delegation.ConflictPending || got.Pending() != nil {
		t.Fatalf("state %s outcome %+v delegation %+v", got.State, got.LastOutcome, got.Delegation)
	}
}
