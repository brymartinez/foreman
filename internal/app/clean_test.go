package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

// refreshDone moves the task to DONE with PR #912 in prState and runs one inbox
// refresh with CleanOnDone set, the way the dashboard does.
func refreshDone(t *testing.T, f fixture, prState string) (task.Key, InboxReport) {
	t.Helper()
	f.app.CleanOnDone = true
	key := reviewed(t, f)
	setCI(f, task.CIStatus{State: task.CIPassing})
	setPR(f, prState)
	f.gh.items = []InboxItem{item(825, "open", t0, task.Assigned)}
	report, err := f.app.Inbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return key, report
}

func TestInboxCleansDoneTasksWhenEnabled(t *testing.T) {
	f := newFixture()
	f.app.DeleteMergedBranches = true
	key, report := refreshDone(t, f, "merged")
	if len(report.Changed) != 1 || !slices.Equal(report.Changed[0].Changes[len(report.Changed[0].Changes)-2:], []string{"PR #912 merged", "worktree and merged branch removed"}) {
		t.Fatalf("changed %+v", report.Changed)
	}
	want := WorktreeRemoval{Path: "/wt/ridezoomo-armada-backend/825-provider-retries", Branch: "feat/825-provider-retries", DeleteBranch: true}
	if !slices.Equal(f.wt.removed, []WorktreeRemoval{want}) || !f.store.recs[key].Delegation.WorktreeRemovedAt.Equal(now) {
		t.Fatalf("removed %+v, rec %+v", f.wt.removed, f.store.recs[key].Delegation)
	}
	if _, err := f.app.Inbox(context.Background()); err != nil || len(f.wt.removed) != 1 {
		t.Errorf("a removed worktree must not be cleaned again: %v %+v", err, f.wt.removed)
	}
}

func TestInboxCleanupKeepsTheBranchWhenTheSettingIsOff(t *testing.T) {
	f := newFixture()
	refreshDone(t, f, "merged")
	if len(f.wt.removed) != 1 || f.wt.removed[0].DeleteBranch {
		t.Errorf("removed %+v", f.wt.removed)
	}
}

func TestInboxCleansAPRClosedWorktreeAndKeepsItsBranchWhileTheIssueIsOpen(t *testing.T) {
	f := newFixture()
	f.app.DeleteMergedBranches = true
	key, report := refreshDone(t, f, "closed")
	got := f.store.recs[key]
	want := WorktreeRemoval{Path: "/wt/ridezoomo-armada-backend/825-provider-retries", Branch: "feat/825-provider-retries"}
	if got.State != task.PRClosed || !slices.Equal(f.wt.removed, []WorktreeRemoval{want}) || !got.Delegation.WorktreeRemovedAt.Equal(now) {
		t.Fatalf("state %s, removed %+v", got.State, f.wt.removed)
	}
	if changes := report.Changed[0].Changes; changes[len(changes)-1] != "worktree removed" {
		t.Errorf("changes = %v", changes)
	}
}

func TestInboxRetriesADirtyPRClosedWorktreeOnTheNextRefresh(t *testing.T) {
	f := newFixture()
	f.wt.removeErr = ErrWorktreeDirty
	key, _ := refreshDone(t, f, "closed")
	if got := f.store.recs[key]; got.State != task.PRClosed || !got.Delegation.WorktreeRemovedAt.IsZero() {
		t.Fatalf("state %s, removed at %v", got.State, got.Delegation.WorktreeRemovedAt)
	}
	f.wt.removeErr, f.app.CleanOnDone = nil, true
	if _, err := f.app.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.wt.removed) != 1 || f.store.recs[key].Delegation.WorktreeRemovedAt.IsZero() {
		t.Errorf("removed %+v", f.wt.removed)
	}
}

func TestInboxClosedIssueCompletesAFailedTaskAndCleansIt(t *testing.T) {
	f := newFixture()
	f.app.CleanOnDone = true
	key := reviewed(t, f)
	f.store.recs[key].State = task.Failed
	// No search returns the issue, so the refresh fetches it and finds it closed.
	if _, err := f.app.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Done || len(f.wt.removed) != 1 || f.wt.removed[0].DeleteBranch {
		t.Errorf("state %s, removed %+v", got.State, f.wt.removed)
	}
}

func TestInboxClosedIssueCompletesAStoppedReviewingTask(t *testing.T) {
	f := newFixture()
	f.app.CleanOnDone = true
	key := reviewed(t, f)
	// A clean review waiting on CI stays REVIEWING with no runner.
	setCI(f, task.CIStatus{State: task.CIPending})
	if _, err := f.app.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.Done || len(f.wt.removed) != 1 {
		t.Errorf("state %s, removed %+v", got.State, f.wt.removed)
	}
}

func TestInboxClosedIssueCompletesAWorkingTaskWithoutARunner(t *testing.T) {
	f := newFixture()
	f.app.CleanOnDone = true
	key := reviewed(t, f)
	// A start that exited before launch recorded a runner pid.
	f.store.recs[key].State = task.Working
	if _, err := f.app.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.Done || len(f.wt.removed) != 1 {
		t.Errorf("state %s, removed %+v", got.State, f.wt.removed)
	}
}

// failPR fails the PR lookup numbered fail, counting from 1, the way a transient gh
// error would.
type failPR struct {
	*fakeGitHub
	fail, calls int
}

func (f *failPR) GetPullRequest(ctx context.Context, k task.Key) (PullRequest, error) {
	if f.calls++; f.calls == f.fail {
		return PullRequest{}, errors.New("gh: timeout")
	}
	return f.fakeGitHub.GetPullRequest(ctx, k)
}

func TestInboxDeletesTheMergedBranchOfAClosedIssueTheGateCouldNotRead(t *testing.T) {
	f := newFixture()
	f.app.CleanOnDone, f.app.DeleteMergedBranches = true, true
	key := reviewed(t, f)
	f.store.recs[key].State = task.Failed
	setPR(f, "merged")
	f.app.GitHub = &failPR{fakeGitHub: f.gh, fail: 1}
	if _, err := f.app.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.Done || len(f.wt.removed) != 1 || !f.wt.removed[0].DeleteBranch {
		t.Errorf("state %s, removed %+v", got.State, f.wt.removed)
	}
}

func TestInboxKeepsTheWorktreeUntilCleanupCanCheckTheMerge(t *testing.T) {
	f := newFixture()
	f.app.CleanOnDone, f.app.DeleteMergedBranches = true, true
	key := reviewed(t, f)
	setPR(f, "merged")
	f.gh.items = []InboxItem{item(825, "open", t0, task.Assigned)}
	// The gate reads the merge, and cleanup's own lookup fails.
	f.app.GitHub = &failPR{fakeGitHub: f.gh, fail: 2}
	report, err := f.app.Inbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.Done || len(f.wt.removed) != 0 || !got.Delegation.WorktreeRemovedAt.IsZero() {
		t.Fatalf("state %s, removed %+v", got.State, f.wt.removed)
	}
	if changes := report.Changed[0].Changes; !strings.HasPrefix(changes[len(changes)-1], "worktree kept: ") {
		t.Errorf("changes = %v", changes)
	}
	if _, err := f.app.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.wt.removed) != 1 || !f.wt.removed[0].DeleteBranch {
		t.Errorf("removed %+v", f.wt.removed)
	}
}

func TestInboxClosedIssueWaitsForTheRunnerToStop(t *testing.T) {
	f := newFixture()
	f.app.CleanOnDone = true
	key := reviewed(t, f)
	f.store.recs[key].State = task.Failed
	f.store.recs[key].Delegation.RunnerPID = 77
	f.runners.alive[77] = true
	if _, err := f.app.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.Failed || len(f.wt.removed) != 0 {
		t.Fatalf("state %s, removed %+v", got.State, f.wt.removed)
	}
	f.store.recs[key].Delegation.RunnerPID = 0
	delete(f.runners.alive, 77)
	if _, err := f.app.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.Done || len(f.wt.removed) != 1 {
		t.Errorf("after the runner stopped: state %s, removed %+v", got.State, f.wt.removed)
	}
}

func TestInboxClosedIssueCompletesTheTaskWithoutCleanupWhenDisabled(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	f.store.recs[key].State = task.Failed
	if _, err := f.app.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.Done || len(f.wt.removed) != 0 || !got.Delegation.WorktreeRemovedAt.IsZero() {
		t.Errorf("state %s, removed %+v", got.State, f.wt.removed)
	}
}

func TestInboxLeavesADoneTaskDoneWhenItsIssueReopens(t *testing.T) {
	f := newFixture()
	key, _ := refreshDone(t, f, "merged")
	f.gh.items = []InboxItem{item(825, "open", t1, task.Assigned)}
	if _, err := f.app.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.Done {
		t.Errorf("state %s", got.State)
	}
}

func TestReopenedPRRecreatesACleanedWorktreeWhenWorkResumes(t *testing.T) {
	f := newFixture()
	key, _ := refreshDone(t, f, "closed")
	if got := f.store.recs[key]; got.State != task.PRClosed || got.Delegation.WorktreeRemovedAt.IsZero() {
		t.Fatalf("setup: state %s, removed at %v", got.State, got.Delegation.WorktreeRemovedAt)
	}
	setPR(f, "open")
	setHead(f, "beef002")
	ensured := len(f.wt.reqs)
	// The first refresh reopens the task under review. The second finds commits since
	// the clean review and launches a runner, which needs the worktree back.
	for range 2 {
		if _, err := f.app.Inbox(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	got := f.store.recs[key]
	if got.State != task.Working || got.Delegation.RunnerPID == 0 {
		t.Fatalf("state %s pid %d", got.State, got.Delegation.RunnerPID)
	}
	want := WorktreeRequest{RepoDir: "/repos/ridezoomo-armada-backend", Path: got.Delegation.Worktree, Branch: got.Delegation.Branch, BaseRef: got.Delegation.BaseRef}
	if len(f.wt.reqs) != ensured+1 || f.wt.reqs[ensured] != want || !got.Delegation.WorktreeRemovedAt.IsZero() {
		t.Errorf("ensured %+v, removed at %v", f.wt.reqs[ensured:], got.Delegation.WorktreeRemovedAt)
	}
}

func TestInboxCleanupKeepsADirtyWorktreeAndSaysSo(t *testing.T) {
	f := newFixture()
	f.wt.removeErr = ErrWorktreeDirty
	key, report := refreshDone(t, f, "merged")
	if !f.store.recs[key].Delegation.WorktreeRemovedAt.IsZero() {
		t.Error("a kept worktree must not be marked removed")
	}
	changes := report.Changed[0].Changes
	if note := changes[len(changes)-1]; note != "worktree kept: "+ErrWorktreeDirty.Error()+"; commit or discard them, and a later refresh removes it" {
		t.Errorf("note = %q", note)
	}
}
