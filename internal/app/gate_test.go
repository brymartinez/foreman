package app

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

var failingCI = task.CIStatus{State: task.CIFailing, Failing: []task.CheckRun{{Name: "test", Link: "https://ci/1"}}}

// reviewed returns a task whose runner finished with a clean review of PR #912.
func reviewed(t *testing.T, f fixture) task.Key {
	t.Helper()
	key := started(t, f, startReq(t))
	postedPlan(f, f.store.recs[key])
	f.claude.turn = prTurn("sid-1")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.WaitingForCI || got.Delegation.RunnerPID != 0 {
		t.Fatalf("setup: state %s pid %d", got.State, got.Delegation.RunnerPID)
	}
	return key
}

func setCI(f fixture, st task.CIStatus) { f.gh.ci = map[int]task.CIStatus{912: st} }

func setPR(f fixture, state string) {
	pr := f.gh.prs[912]
	pr.State = state
	f.gh.prs[912] = pr
}

func TestGateGreenOrNoChecksIsReady(t *testing.T) {
	for _, st := range []task.CIState{task.CIPassing, task.CINone} {
		f := newFixture()
		key := reviewed(t, f)
		setCI(f, task.CIStatus{State: st})
		recs, _, err := f.app.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		got := f.store.recs[key]
		if got.State != task.ReadyForHuman || got.CI.State != st || !got.CI.CheckedAt.Equal(now) || recs[0].State != task.ReadyForHuman {
			t.Errorf("%s: state %s ci %+v", st, got.State, got.CI)
		}
	}
}

func TestGatePendingWaits(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	setCI(f, task.CIStatus{State: task.CIPending})
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.WaitingForCI || got.CI.State != task.CIPending || len(f.runners.spawned) != 1 {
		t.Errorf("state %s ci %+v spawned %d", got.State, got.CI, len(f.runners.spawned))
	}
}

func TestGateSkipsTasksARunnerIsDriving(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	f.store.recs[key].Delegation.RunnerPID = 77
	f.runners.alive[77] = true
	setCI(f, task.CIStatus{State: task.CIPassing})
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.WaitingForCI || got.CI != nil {
		t.Errorf("state %s ci %+v", got.State, got.CI)
	}
}

func TestGateHandsFailingCIToTheDeveloper(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	setCI(f, failingCI)
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.ReadyForHuman || got.CI.State != task.CIFailing || len(f.runners.spawned) != 1 || len(f.claude.resumed) != 0 {
		t.Fatalf("state %s ci %+v spawned %d resumed %d", got.State, got.CI, len(f.runners.spawned), len(f.claude.resumed))
	}
	if !strings.Contains(got.LastOutcome.Summary, "CI failed (test)") {
		t.Errorf("outcome %+v", got.LastOutcome)
	}
}

func TestGateRefreshesCIOnReadyTasks(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	setCI(f, failingCI)
	f.gh.items = []InboxItem{item(825, "open", t0, task.Assigned)}
	if _, err := f.app.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	setCI(f, task.CIStatus{State: task.CIPassing})
	report, err := f.app.Inbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.ReadyForHuman || got.CI.State != task.CIPassing || len(report.Ready) != 1 {
		t.Errorf("state %s ci %+v ready %d", got.State, got.CI, len(report.Ready))
	}
	if !slices.Contains(report.WaitingChanges[key], "CI now passing") || len(report.Changed) != 0 {
		t.Fatalf("CI transition missing from ready task: %+v", report)
	}
}

func TestGateMergedIsDoneAndClosedIsPRClosed(t *testing.T) {
	for _, tc := range []struct {
		pr    string
		state task.State
		note  string
	}{{"merged", task.Done, "PR #912 merged"}, {"closed", task.PRClosed, prClosedNote}} {
		f := newFixture()
		key := reviewed(t, f)
		setCI(f, task.CIStatus{State: task.CIPassing})
		if _, _, err := f.app.Status(context.Background()); err != nil {
			t.Fatal(err)
		}
		setPR(f, tc.pr)
		f.gh.items = []InboxItem{item(825, "open", t0, task.Assigned)}
		report, err := f.app.Inbox(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		got := f.store.recs[key]
		if got.State != tc.state || len(report.Changed) != 1 || !slices.Contains(report.Changed[0].Changes, tc.note) || len(report.Ready) != 0 {
			t.Errorf("%s: state %s changed %+v ready %d", tc.pr, got.State, report.Changed, len(report.Ready))
		}
	}
}

func TestGateMergedPRCompletesAFailedTask(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	f.store.recs[key].State = task.Failed
	setPR(f, "merged")
	f.gh.items = []InboxItem{item(825, "open", t0, task.Assigned)}
	report, err := f.app.Inbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Done || got.LastOutcome.Status != "merged" || len(report.Changed) != 1 || !slices.Contains(report.Changed[0].Changes, "PR #912 merged") {
		t.Errorf("state %s outcome %+v changed %+v", got.State, got.LastOutcome, report.Changed)
	}
}

func TestGateMergedPRWaitsForTheRunnerToStop(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	f.store.recs[key].State = task.Failed
	f.store.recs[key].Delegation.RunnerPID = 77
	f.runners.alive[77] = true
	setPR(f, "merged")
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.Failed || got.Delegation.RunnerPID != 77 {
		t.Fatalf("state %s pid %d", got.State, got.Delegation.RunnerPID)
	}
	if len(f.runners.stopped) != 0 {
		t.Errorf("stopped runner for merged PR: %v", f.runners.stopped)
	}
	f.store.recs[key].Delegation.RunnerPID = 0
	delete(f.runners.alive, 77)
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.Done {
		t.Errorf("state after runner stopped = %s", got.State)
	}
}

func TestGateMergedPRCompletesAFailedTaskWhoseRunnerExitedBeforeClearingItsPID(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	f.store.recs[key].State = task.Failed
	f.store.recs[key].Delegation.RunnerPID = 77
	setPR(f, "merged")
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.Done || got.Delegation.RunnerPID != 0 {
		t.Errorf("state %s pid %d", got.State, got.Delegation.RunnerPID)
	}
}

const prClosedNote = "PR #912 closed without merging; the branch is kept"

func TestGateClosedPRIsPRClosedNotFailed(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	setPR(f, "closed")
	f.gh.items = []InboxItem{item(825, "open", t0, task.Assigned)}
	report, err := f.app.Inbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.PRClosed || got.LastOutcome.Status != "pr_closed" || got.LastOutcome.Summary != prClosedNote {
		t.Fatalf("state %s outcome %+v", got.State, got.LastOutcome)
	}
	if strings.Contains(got.LastOutcome.Summary, "failed") {
		t.Errorf("a closed PR reads as a failure: %q", got.LastOutcome.Summary)
	}
	if len(f.notes.sent) != 0 {
		t.Errorf("a closed PR notified the developer: %v", f.notes.sent)
	}
	if len(report.Changed) != 1 || !slices.Contains(report.Changed[0].Changes, prClosedNote) {
		t.Errorf("changed = %+v", report.Changed)
	}
}

func TestGateClosedPRStopsARunningAgent(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	f.store.recs[key].Delegation.RunnerPID = 77
	f.runners.alive[77] = true
	setPR(f, "closed")
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if !slices.Equal(f.runners.stopped, []int{77}) {
		t.Errorf("stopped = %v, want the running runner", f.runners.stopped)
	}
	if got.State != task.PRClosed || got.Delegation.RunnerPID != 0 {
		t.Errorf("state %s pid %d", got.State, got.Delegation.RunnerPID)
	}
}

func TestGateClosedPRWithAStuckRunnerWarnsAndRetries(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	f.store.recs[key].Delegation.RunnerPID = 77
	f.runners.alive[77] = true
	f.runners.stuck = true
	setPR(f, "closed")
	_, warnings, err := f.app.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.PRClosed || got.Delegation.RunnerPID != 77 {
		t.Errorf("a runner that would not stop must keep its pid for the retry: state %s pid %d", got.State, got.Delegation.RunnerPID)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "has not exited yet") {
		t.Errorf("warnings = %v", warnings)
	}
	f.runners.stuck = false
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.Delegation.RunnerPID != 0 || !slices.Equal(f.runners.stopped, []int{77, 77}) {
		t.Errorf("the next refresh did not stop the runner again: pid %d stopped %v", got.Delegation.RunnerPID, f.runners.stopped)
	}
}

func TestGateClosedPRWithALiveRunnerNeverReportsAFailure(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	f.store.recs[key].Delegation.RunnerPID = 77
	f.runners.alive[77] = true
	// The real runner records its own stop as it exits. That write must not move the
	// task out of PR_CLOSED, and must not tell the developer a run failed (R8).
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	f.runners.onStop = func(pid int) {
		_, runnerErr := f.app.update(context.Background(), key, existing(func(cur *task.Record) {
			applyStop(stopped, cur, "implementation", io.Discard, now)
			cur.Delegation.RunnerPID = 0
		}))
		if !errors.Is(runnerErr, errPRClosed) {
			t.Errorf("the runner was allowed to record its stop: %v", runnerErr)
		}
	}
	f.notes.sent = nil
	setPR(f, "closed")
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.PRClosed || got.LastOutcome.Status != "pr_closed" || got.Delegation.RunnerPID != 0 {
		t.Errorf("state %s outcome %+v pid %d", got.State, got.LastOutcome, got.Delegation.RunnerPID)
	}
	if len(f.notes.sent) != 0 {
		t.Errorf("stopping the run notified the developer: %v", f.notes.sent)
	}
}

func TestGateClosedPRWithADeadRunnerNeverReportsAFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pr    string
		want  task.State
		notes int
	}{
		// The runner died on its own before the refresh. Reaping it first would tell
		// the developer the run failed, which closing a PR must never do (R8).
		{"PR closed", "closed", task.PRClosed, 0},
		{"PR still open", "open", task.Failed, 1},
	} {
		f := newFixture()
		key := reviewed(t, f)
		f.store.recs[key].State = task.Implementing
		f.store.recs[key].Delegation.RunnerPID = 77 // never added to f.runners.alive
		setCI(f, task.CIStatus{State: task.CIPassing})
		setPR(f, tc.pr)
		f.notes.sent = nil
		if _, err := f.app.Tasks(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := f.store.recs[key]; got.State != task.Implementing {
			t.Fatalf("%s: local reread changed the state to %s before GitHub refresh", tc.name, got.State)
		}
		if _, _, err := f.app.Status(context.Background()); err != nil {
			t.Fatal(err)
		}
		got := f.store.recs[key]
		if got.State != tc.want || got.Delegation.RunnerPID != 0 {
			t.Errorf("%s: state %s pid %d", tc.name, got.State, got.Delegation.RunnerPID)
		}
		if len(f.notes.sent) != tc.notes {
			t.Errorf("%s: notifications %v", tc.name, f.notes.sent)
		}
	}
}

func TestGateUnreadablePRLeavesADeadRunnerAlone(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	f.store.recs[key].State = task.Implementing
	f.store.recs[key].Delegation.RunnerPID = 77 // never added to f.runners.alive
	setCI(f, task.CIStatus{State: task.CIPassing})
	closed := f.gh.prs[912]
	closed.State = "closed"
	delete(f.gh.prs, 912) // the PR lookup now fails
	f.notes.sent = nil

	// The runner died and GitHub did not answer, so foreman cannot yet tell a crash
	// from a PR the developer closed. Failing it here would notify about a failure
	// that may not have happened, and no later refresh can take that back.
	_, warnings, err := f.app.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "PR:") {
		t.Errorf("warnings = %v", warnings)
	}
	got := f.store.recs[key]
	if got.State != task.Implementing || got.Delegation.RunnerPID != 77 {
		t.Fatalf("the refresh judged an unread PR: state %s pid %d", got.State, got.Delegation.RunnerPID)
	}
	if len(f.notes.sent) != 0 {
		t.Fatalf("notifications %v", f.notes.sent)
	}

	// GitHub answers on the next refresh, and the close ends the task.
	f.gh.prs[912] = closed
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.PRClosed || got.Delegation.RunnerPID != 0 {
		t.Errorf("state %s pid %d", got.State, got.Delegation.RunnerPID)
	}
	if len(f.notes.sent) != 0 {
		t.Errorf("notifications %v", f.notes.sent)
	}
}

func TestInboxUnreadablePRLeavesADeadRunnerAlone(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	f.store.recs[key].State = task.Implementing
	f.store.recs[key].Delegation.RunnerPID = 77
	delete(f.gh.prs, 912)
	f.gh.items = []InboxItem{item(825, "open", t0, task.Assigned)}
	f.notes.sent = nil
	if _, err := f.app.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Implementing || got.Delegation.RunnerPID != 77 {
		t.Errorf("state %s pid %d", got.State, got.Delegation.RunnerPID)
	}
	if len(f.notes.sent) != 0 {
		t.Errorf("notifications %v", f.notes.sent)
	}
}

func TestReapStillFailsADeadRunnerWithNoPR(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	f.store.recs[key].State = task.Implementing
	f.store.recs[key].Delegation.PR = 0 // nothing pushed yet, so nothing to read
	f.store.recs[key].Delegation.RunnerPID = 77
	f.notes.sent = nil
	// No PR means no GitHub call stands between the dead runner and the verdict, so
	// the one second reread still fails it.
	if _, err := f.app.Tasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || got.Delegation.RunnerPID != 0 {
		t.Errorf("state %s pid %d", got.State, got.Delegation.RunnerPID)
	}
	if len(f.notes.sent) != 1 {
		t.Errorf("notifications %v", f.notes.sent)
	}
}

func TestGateReopenedPRStopsARunnerThatWouldNotStop(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	f.store.recs[key].Delegation.RunnerPID = 77
	f.runners.alive[77] = true
	f.runners.stuck = true
	setPR(f, "closed")
	setCI(f, task.CIStatus{State: task.CIPassing})
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.PRClosed || got.Delegation.RunnerPID != 77 {
		t.Fatalf("setup: state %s pid %d", got.State, got.Delegation.RunnerPID)
	}
	// The PR reopens while that runner is still going. PR_CLOSED is what keeps it
	// from writing, so the task must not come back until the stop takes.
	setPR(f, "open")
	_, warnings, err := f.app.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.PRClosed || got.Delegation.RunnerPID != 77 {
		t.Fatalf("the task came back under a live runner: state %s pid %d", got.State, got.Delegation.RunnerPID)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "before PR #912 reopens") {
		t.Errorf("warnings = %v", warnings)
	}
	f.runners.stuck = false
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.ReadyForHuman || got.Delegation.RunnerPID != 0 {
		t.Errorf("the task did not come back once the runner stopped: state %s pid %d", got.State, got.Delegation.RunnerPID)
	}
}

func TestGateReopenedPRClearsAnExitedRunner(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	f.store.recs[key].State = task.PRClosed
	f.store.recs[key].Delegation.RunnerPID = 77
	setPR(f, "open")
	setCI(f, task.CIStatus{State: task.CIPassing})
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.ReadyForHuman || got.Delegation.RunnerPID != 0 {
		t.Errorf("state %s pid %d", got.State, got.Delegation.RunnerPID)
	}
}

func TestGateClosedPRLeavesARestartedRunnerAlone(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	f.store.recs[key].Delegation.RunnerPID = 77
	f.runners.alive[77] = true
	// The stopped runner exits and the developer restarts the task before the gate
	// clears the pid. The new runner is driving another PR, so it is not this one's
	// to stop, and its pid must survive.
	f.runners.onStop = func(pid int) {
		_, err := f.app.Store.Update(context.Background(), key, func(cur *task.Record) (*task.Record, error) {
			cur.State, cur.Delegation.RunnerPID, cur.Delegation.PR = task.Implementing, 99, 0
			return cur, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		f.runners.alive[99] = true
	}
	setPR(f, "closed")
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.Delegation.RunnerPID != 99 || got.State != task.Implementing {
		t.Errorf("the restarted run lost its runner: state %s pid %d", got.State, got.Delegation.RunnerPID)
	}
	if !slices.Equal(f.runners.stopped, []int{77}) || !f.runners.alive[99] {
		t.Errorf("stopped %v, 99 alive %t", f.runners.stopped, f.runners.alive[99])
	}
}

func TestGateReopenedPRReturnsToTheActiveList(t *testing.T) {
	for _, tc := range []struct {
		name   string
		review func(*task.Review)
		want   task.State
		rerun  bool // a runner starts to review the reopened PR again
	}{
		{"clean review of the head GitHub reports", func(*task.Review) {}, task.ReadyForHuman, false},
		{"review requested changes", func(r *task.Review) { r.Clean = false }, task.Implementing, false},
		// The self-review covers the PR's head and description; the competing review
		// read an earlier one, so only it runs again.
		{"clean review of an earlier head", func(r *task.Review) { r.Head = "older01" }, task.AdversarialReviewing, true},
		// A review from before heads were recorded cannot be shown to cover the
		// reopened PR, so it must not hand the task to the developer.
		{"clean review with no recorded head", func(r *task.Review) { r.Head = "" }, task.AdversarialReviewing, true},
		{"clean review survives an earlier description", func(r *task.Review) { r.Proposal = task.ProposalDigest("", "Retry everything.") }, task.ReadyForHuman, false},
	} {
		f := newFixture()
		key := reviewed(t, f)
		tc.review(f.store.recs[key].LatestCompeting())
		setPR(f, "closed")
		if _, _, err := f.app.Status(context.Background()); err != nil {
			t.Fatal(err)
		}
		if f.store.recs[key].State != task.PRClosed {
			t.Fatalf("%s: setup state %s", tc.name, f.store.recs[key].State)
		}
		setPR(f, "open")
		setCI(f, task.CIStatus{State: task.CIPassing})
		recs, _, err := f.app.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		got := f.store.recs[key]
		if got.State != tc.want || len(recs) != 1 || recs[0].Key != key {
			t.Errorf("%s: state %s, delegated work %d", tc.name, got.State, len(recs))
		}
		if rerun := len(f.runners.spawned) == 2; rerun != tc.rerun {
			t.Errorf("%s: runners spawned %d, want a review run %t", tc.name, len(f.runners.spawned), tc.rerun)
		}
		if got.LastOutcome.Status != "pr_reopened" || !strings.Contains(got.LastOutcome.Summary, "PR #912 reopened") {
			t.Errorf("%s: outcome %+v", tc.name, got.LastOutcome)
		}
	}
}

func TestGateClosedPRDoesNotStopARunStartedSinceItClosed(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	setPR(f, "closed")
	f.gh.prs[913] = PullRequest{Key: task.Key{Repo: key.Repo, Number: 913}, State: "open", HeadRefName: "feat/825-provider-retries", BaseRefName: "main"}
	// Between this refresh's list and its write, another refresh ends the task and
	// the developer starts a new run, which opens a PR of its own.
	f.gh.onPR = func() {
		if _, err := f.app.Store.Update(context.Background(), key, func(cur *task.Record) (*task.Record, error) {
			cur.State = task.PRClosed
			return cur, nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.app.Store.Update(context.Background(), key, func(cur *task.Record) (*task.Record, error) {
			cur.State, cur.Delegation.PR, cur.Delegation.RunnerPID = task.Implementing, 913, 99
			return cur, nil
		}); err != nil {
			t.Fatal(err)
		}
		f.runners.alive[99] = true
	}
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Implementing || got.Delegation.PR != 913 || got.Delegation.RunnerPID != 99 {
		t.Errorf("the new run was ended by the old PR: state %s pr %d pid %d", got.State, got.Delegation.PR, got.Delegation.RunnerPID)
	}
	if len(f.runners.stopped) != 0 {
		t.Errorf("stopped a runner that belongs to another PR: %v", f.runners.stopped)
	}
}

func TestGatePRClosedThenMergedIsDone(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	setPR(f, "closed")
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Reopened and merged between two refreshes: the gate sees only the merge.
	setPR(f, "merged")
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.Done || got.LastOutcome.Status != "merged" {
		t.Errorf("state %s outcome %+v", got.State, got.LastOutcome)
	}
}

func TestClosedIssueCompletesAPRClosedTaskAndKeepsItsBranch(t *testing.T) {
	f := newFixture()
	f.app.CleanOnDone, f.app.DeleteMergedBranches = true, true
	key := reviewed(t, f)
	setPR(f, "closed")
	// No search returns the task, so the same refresh re-fetches the issue, which the
	// developer closed along with the PR. The closed issue wins over PR_CLOSED.
	if _, err := f.app.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Done || got.GitHub.State != "closed" {
		t.Fatalf("state %s, issue %s", got.State, got.GitHub.State)
	}
	want := WorktreeRemoval{Path: "/wt/testorg-example/feat/825-provider-retries", Branch: "feat/825-provider-retries", RepoDir: "/src/example"}
	if !slices.Equal(f.wt.removed, []WorktreeRemoval{want}) || got.Delegation.WorktreeRemovedAt.IsZero() {
		t.Errorf("removed %+v, rec %+v", f.wt.removed, got.Delegation)
	}
}

func TestPRClosedLeavesDelegatedWork(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	setPR(f, "closed")
	recs, _, err := f.app.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Errorf("delegated work still holds %d tasks", len(recs))
	}
	d := f.store.recs[key].Delegation
	if d.Worktree == "" || d.Branch == "" || !d.WorktreeRemovedAt.IsZero() {
		t.Errorf("worktree or branch lost: %+v", d)
	}
}

func TestInboxListsReadyWithoutAChangedEntry(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	setCI(f, task.CIStatus{State: task.CIPassing})
	f.gh.items = []InboxItem{item(825, "open", t0, task.Assigned)}
	report, err := f.app.Inbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Ready) != 1 || report.Ready[0].Key != key || len(report.Changed) != 0 {
		t.Errorf("ready %d changed %+v", len(report.Ready), report.Changed)
	}
}

func TestReconcileClosedIssueEndsIdleDelegatedTasks(t *testing.T) {
	blocked := seen(7, task.NeedsDecision, "open", t0)
	working := seen(8, task.Implementing, "open", t0)
	working.Delegation = &task.Delegation{RunnerPID: 4242}
	items := []InboxItem{item(7, "closed", t1, task.Tracked), item(8, "closed", t1, task.Tracked)}
	Reconcile(items, []*task.Record{blocked, working}, nil, true, now)
	if blocked.State != task.Done || working.State != task.Implementing {
		t.Errorf("blocked %s working %s", blocked.State, working.State)
	}
}

func TestAReviewStageWithoutARunnerAfterACleanReviewIsReviewedAgain(t *testing.T) {
	// A task file from before the stages loads a REVIEWING task with new commits since
	// its clean review as SELF_REVIEW, with no runner.
	f := newFixture()
	key := reviewed(t, f)
	setHead(f, "beef002")
	rec := f.store.recs[key]
	rec.Delegation.PRHead, rec.State = "beef002", task.SelfReviewing
	setCI(f, task.CIStatus{State: task.CIPassing})
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.SelfReviewing || got.Delegation.RunnerPID == 0 || len(f.runners.spawned) != 2 {
		t.Fatalf("state %s pid %d spawned %d; want a runner for the self-review", got.State, got.Delegation.RunnerPID, len(f.runners.spawned))
	}
}

func TestARestartedReviewKeepsCleanCodeReviewsAfterATextEdit(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	pr := f.gh.prs[912]
	pr.Body = "Retry 5xx and 429.\n\nCloses #825"
	f.gh.prs[912] = pr
	setCI(f, task.CIStatus{State: task.CIPassing})
	f.store.recs[key].State = task.AdversarialReviewing
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.Delegation.PRDigest != task.ProposalDigest(pr.Title, pr.Body) || got.State != task.WaitingForCI || len(f.runners.spawned) != 1 {
		t.Fatalf("state %s spawned %d", got.State, len(f.runners.spawned))
	}
}

func TestARestartedReviewThatTheCleanReviewCoversWaitsForCI(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	setCI(f, task.CIStatus{State: task.CIPending})
	f.store.recs[key].State = task.AdversarialReviewing
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.WaitingForCI || len(f.runners.spawned) != 1 {
		t.Fatalf("state %s spawned %d; want a CI wait without a runner", got.State, len(f.runners.spawned))
	}
}
