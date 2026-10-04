package app

import (
	"context"
	"slices"
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

func TestInboxCleanupLeavesAClosedUnmergedPRAlone(t *testing.T) {
	f := newFixture()
	f.app.DeleteMergedBranches = true
	key, _ := refreshDone(t, f, "closed")
	// Only DONE is cleaned automatically; a PR closed without merging is the developer's call.
	if f.store.recs[key].State == task.Done || len(f.wt.removed) != 0 {
		t.Errorf("state %s, removed %+v", f.store.recs[key].State, f.wt.removed)
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
