package app

import (
	"context"
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
	f.claude.turn = prTurn("sid-1")
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.Reviewing || got.Delegation.RunnerPID != 0 {
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
	if got := f.store.recs[key]; got.State != task.Reviewing || got.CI.State != task.CIPending || len(f.runners.spawned) != 1 {
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
	if got := f.store.recs[key]; got.State != task.Reviewing || got.CI != nil {
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

const prClosedNote = "PR #912 closed without merging; the branch and worktree are kept"

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

func TestGateClosedPRWithAStuckRunnerWarnsAndWaits(t *testing.T) {
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
	if got.State != task.Reviewing || got.Delegation.RunnerPID != 77 {
		t.Errorf("a runner that would not stop lost its task: state %s pid %d", got.State, got.Delegation.RunnerPID)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "has not exited yet") {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestGateReopenedPRReturnsToTheActiveList(t *testing.T) {
	for _, tc := range []struct {
		name  string
		clean bool
		want  task.State
	}{{"clean review and finished CI", true, task.ReadyForHuman}, {"review requested changes", false, task.Reviewing}} {
		f := newFixture()
		key := reviewed(t, f)
		if !tc.clean {
			f.store.recs[key].Reviews[0].Clean = false
		}
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
		if got.LastOutcome.Status != "pr_reopened" || !strings.Contains(got.LastOutcome.Summary, "PR #912 reopened") {
			t.Errorf("%s: outcome %+v", tc.name, got.LastOutcome)
		}
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
	working := seen(8, task.Working, "open", t0)
	items := []InboxItem{item(7, "closed", t1, task.Tracked), item(8, "closed", t1, task.Tracked)}
	Reconcile(items, []*task.Record{blocked, working}, nil, true, now)
	if blocked.State != task.Done || working.State != task.Working {
		t.Errorf("blocked %s working %s", blocked.State, working.State)
	}
}
