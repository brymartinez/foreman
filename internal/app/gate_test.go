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
		recs, err := f.app.Status(context.Background())
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
	if _, err := f.app.Status(context.Background()); err != nil {
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
	if _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.Reviewing || got.CI != nil {
		t.Errorf("state %s ci %+v", got.State, got.CI)
	}
}

func TestGateFailingCISendsImplementerBackThenGivesUp(t *testing.T) {
	f := newFixture()
	f.app.ReviewRounds = 1 // clean re-reviews after a CI fix must not use up the review budget
	key := reviewed(t, f)
	setCI(f, failingCI)
	for fix := 1; fix <= MaxCIFixes; fix++ {
		if _, err := f.app.Status(context.Background()); err != nil {
			t.Fatal(err)
		}
		got := f.store.recs[key]
		if got.State != task.Working || got.CIFixes != fix || len(f.runners.spawned) != fix+1 || got.Delegation.RunnerPID != 4242 {
			t.Fatalf("fix %d: state %s fixes %d spawned %d pid %d", fix, got.State, got.CIFixes, len(f.runners.spawned), got.Delegation.RunnerPID)
		}
		if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
			t.Fatal(err)
		}
		resumed := f.claude.resumed[len(f.claude.resumed)-1]
		if !strings.Contains(resumed, "sid-1: CI failed on PR #912") || !strings.Contains(resumed, "- test: https://ci/1") {
			t.Fatalf("implementer not resumed with the CI failure: %q", resumed)
		}
		if got := f.store.recs[key]; got.State != task.Reviewing || got.CI != nil {
			t.Fatalf("after fix %d: state %s ci %+v", fix, got.State, got.CI)
		}
	}
	if _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "CI still failing (test) after 2 fixes") || len(f.runners.spawned) != MaxCIFixes+1 {
		t.Errorf("state %s outcome %+v spawned %d", got.State, got.LastOutcome, len(f.runners.spawned))
	}
}

func TestFreshTurnAfterCIFailureCarriesFailingChecks(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	setCI(f, failingCI)
	if _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.store.recs[key].Delegation.Implementer.ID = ""
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.claude.prompt, "## Failing CI") || !strings.Contains(f.claude.prompt, "- test: https://ci/1") {
		t.Errorf("fresh prompt lacks failing CI:\n%s", f.claude.prompt)
	}
}

func TestGateMergedIsDoneAndClosedIsFailed(t *testing.T) {
	for _, tc := range []struct {
		pr    string
		state task.State
		note  string
	}{{"merged", task.Done, "PR #912 merged"}, {"closed", task.Failed, "PR #912 closed without merging"}} {
		f := newFixture()
		key := reviewed(t, f)
		setCI(f, task.CIStatus{State: task.CIPassing})
		if _, err := f.app.Status(context.Background()); err != nil {
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
	Reconcile(items, []*task.Record{blocked, working}, nil, now)
	if blocked.State != task.Done || working.State != task.Working {
		t.Errorf("blocked %s working %s", blocked.State, working.State)
	}
}
