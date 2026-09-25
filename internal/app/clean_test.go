package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

// done returns a task whose PR #912 is in prState, after the gate has run.
func done(t *testing.T, f fixture, prState string) task.Key {
	t.Helper()
	key := reviewed(t, f)
	setCI(f, task.CIStatus{State: task.CIPassing})
	setPR(f, prState)
	if _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestCleanRemovesDoneWorktreesAndOnlyMergedBranches(t *testing.T) {
	f := newFixture()
	f.app.DeleteMergedBranches = true
	key := done(t, f, "merged")
	res, err := f.app.Clean(context.Background(), CleanRequest{DryRun: true})
	if err != nil || len(res) != 1 || !res[0].BranchDeleted || len(f.wt.removed) != 0 {
		t.Fatalf("dry run: %+v %v removed %d", res, err, len(f.wt.removed))
	}
	if res, err = f.app.Clean(context.Background(), CleanRequest{}); err != nil || len(res) != 1 || res[0].Err != nil {
		t.Fatalf("%+v %v", res, err)
	}
	want := WorktreeRemoval{Path: "/wt/ridezoomo-armada-backend/825-provider-retries", Branch: "feat/825-provider-retries", DeleteBranch: true}
	if !slices.Equal(f.wt.removed, []WorktreeRemoval{want}) || !f.store.recs[key].Delegation.WorktreeRemovedAt.Equal(now) {
		t.Fatalf("removed %+v, rec %+v", f.wt.removed, f.store.recs[key].Delegation)
	}
	if res, _ := f.app.Clean(context.Background(), CleanRequest{}); len(res) != 0 {
		t.Errorf("a removed worktree must not be cleaned again: %+v", res)
	}
}

func TestCleanKeepsBranchUnlessMergedAndEnabled(t *testing.T) {
	for _, tc := range []struct {
		name   string
		enable bool
		pr     string
		issue  int
	}{{"setting off", false, "merged", 0}, {"closed PR", true, "closed", 825}} {
		f := newFixture()
		f.app.DeleteMergedBranches = tc.enable
		done(t, f, tc.pr)
		res, err := f.app.Clean(context.Background(), CleanRequest{Issue: tc.issue})
		if err != nil || len(res) != 1 || res[0].BranchDeleted || f.wt.removed[0].DeleteBranch {
			t.Errorf("%s: %+v %v %+v", tc.name, res, err, f.wt.removed)
		}
	}
}

func TestCleanRefusesActiveTasksAndReportsDirtyWorktrees(t *testing.T) {
	f := newFixture()
	key := reviewed(t, f)
	if _, err := f.app.Clean(context.Background(), CleanRequest{Issue: 825}); err == nil || !strings.Contains(err.Error(), "is REVIEWING") {
		t.Fatalf("got %v", err)
	}
	setCI(f, task.CIStatus{State: task.CIPassing})
	setPR(f, "merged")
	f.wt.removeErr = ErrWorktreeDirty
	if _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if res, err := f.app.Clean(context.Background(), CleanRequest{}); err != nil || len(res) != 1 || !errors.Is(res[0].Err, ErrWorktreeDirty) {
		t.Fatalf("%+v %v", res, err)
	}
	if !f.store.recs[key].Delegation.WorktreeRemovedAt.IsZero() {
		t.Error("a kept worktree must not be marked removed")
	}
}

func TestInboxCleansDoneTasksWhenEnabled(t *testing.T) {
	f := newFixture()
	f.app.CleanOnDone, f.app.DeleteMergedBranches = true, true
	key := reviewed(t, f)
	setCI(f, task.CIStatus{State: task.CIPassing})
	setPR(f, "merged")
	f.gh.items = []InboxItem{item(825, "open", t0, task.Assigned)}
	report, err := f.app.Inbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Changed) != 1 || !slices.Equal(report.Changed[0].Changes[len(report.Changed[0].Changes)-2:], []string{"PR #912 merged", "worktree and merged branch removed"}) {
		t.Fatalf("changed %+v", report.Changed)
	}
	if f.store.recs[key].Delegation.WorktreeRemovedAt.IsZero() || len(f.wt.removed) != 1 {
		t.Errorf("not cleaned: %+v", f.wt.removed)
	}
}
