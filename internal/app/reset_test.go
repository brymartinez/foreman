package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// failedTask starts #825 and fails its run with a log on disk. The fixture's PR
// #912 is open on the task's branch.
func failedTask(t *testing.T, f fixture, pr int) *task.Record {
	t.Helper()
	key := started(t, f, startReq(t))
	rec := f.store.recs[key]
	rec.State = task.Failed
	rec.Delegation.RunnerPID = 0
	rec.Delegation.PR = pr
	rec.Delegation.Implementer.ID = "sid-old"
	rec.Delegation.RunnerLog = filepath.Join(t.TempDir(), "825.log")
	if err := os.WriteFile(rec.Delegation.RunnerLog, []byte("old run\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return rec
}

// unchanged checks that a refused reset deleted nothing.
func unchanged(t *testing.T, f fixture, before *task.Record) {
	t.Helper()
	got := f.store.recs[before.Key]
	if got.Delegation == nil || got.State != before.State || got.Reset != nil {
		t.Errorf("record changed: %s %+v reset %+v", got.State, got.Delegation, got.Reset)
	}
	if len(f.wt.removed) != 0 {
		t.Errorf("worktree removed: %+v", f.wt.removed)
	}
	if _, err := os.Stat(before.Delegation.RunnerLog); err != nil {
		t.Errorf("runner log removed: %v", err)
	}
}

func TestResetDeletesTheRunAndKeepsTheStoredPR(t *testing.T) {
	f := newFixture()
	rec := failedTask(t, f, 912)
	d := *rec.Delegation
	got, err := f.app.Reset(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	want := &task.ResetMark{At: now, PR: 912, PRURL: "https://github.com/ridezoomo/armada-backend/pull/912", Branch: "feat/825-provider-retries", PRState: "open"}
	if got.Delegation != nil || got.State != task.Discovered || !reflect.DeepEqual(got.Reset, want) {
		t.Fatalf("record = %s %+v reset %+v", got.State, got.Delegation, got.Reset)
	}
	if got.Attempts != nil || got.Plan != nil || got.Reviews != nil || got.LastOutcome != nil || got.GitHub == nil {
		t.Errorf("run state kept: %+v", got)
	}
	if f.store.recs[rec.Key] != got {
		t.Error("reset record not saved")
	}
	wantRm := WorktreeRemoval{Path: d.Worktree, Branch: d.Branch, DeleteBranch: true, Force: true, RepoDir: "/repos/ridezoomo-armada-backend"}
	if !reflect.DeepEqual(f.wt.removed, []WorktreeRemoval{wantRm}) {
		t.Errorf("removed %+v, want %+v", f.wt.removed, wantRm)
	}
	if _, err := os.Stat(d.RunnerLog); !os.IsNotExist(err) {
		t.Errorf("runner log kept: %v", err)
	}
	if len(f.gh.comments) != 0 {
		t.Errorf("reset wrote to GitHub: %+v", f.gh.comments)
	}
}

func TestResetFindsTheBranchPRWhenTheRunRecordedNone(t *testing.T) {
	f := newFixture()
	rec := failedTask(t, f, 0)
	got, err := f.app.Reset(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	if got.Reset == nil || got.Reset.PR != 912 || got.Reset.Branch != "feat/825-provider-retries" {
		t.Fatalf("reset = %+v", got.Reset)
	}
}

func TestResetWithNoPRKeepsOnlyTheMark(t *testing.T) {
	f := newFixture()
	delete(f.gh.prs, 912)
	rec := failedTask(t, f, 0)
	got, err := f.app.Reset(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	if got.Reset == nil || got.Reset.PR != 0 || got.Reset.Branch != "" || !got.Reset.At.Equal(now) {
		t.Fatalf("reset = %+v", got.Reset)
	}
}

func TestResetRefusesATaskThatIsNotFailed(t *testing.T) {
	f := newFixture()
	rec := failedTask(t, f, 912)
	rec.State = task.ReadyForHuman
	if _, err := f.app.Reset(context.Background(), rec); err == nil || !strings.Contains(err.Error(), "only a FAILED task") {
		t.Fatalf("err = %v", err)
	}
	unchanged(t, f, rec)
}

func TestResetRefusesAFailedTaskWithALiveRunner(t *testing.T) {
	f := newFixture()
	rec := failedTask(t, f, 912)
	rec.Delegation.RunnerPID = 4242
	f.runners.alive[4242] = true
	if _, err := f.app.Reset(context.Background(), rec); err == nil || !strings.Contains(err.Error(), "pid 4242") {
		t.Fatalf("err = %v", err)
	}
	unchanged(t, f, rec)
}

func TestResetDeletesNothingWhenGitHubCannotBeRead(t *testing.T) {
	f := newFixture()
	rec := failedTask(t, f, 0)
	f.gh.branchPRErr = errors.New("HTTP 502")
	if _, err := f.app.Reset(context.Background(), rec); err == nil || !strings.Contains(err.Error(), "HTTP 502") || !strings.Contains(err.Error(), "nothing was deleted") {
		t.Fatalf("err = %v", err)
	}
	unchanged(t, f, rec)
}

func TestResetDeletesNothingWhenTheStoredPRCannotBeRead(t *testing.T) {
	f := newFixture()
	rec := failedTask(t, f, 77)
	if _, err := f.app.Reset(context.Background(), rec); err == nil || !strings.Contains(err.Error(), "PR #77") {
		t.Fatalf("err = %v", err)
	}
	unchanged(t, f, rec)
}

func TestResetDeletesNothingWhenTheStoredPRIsOnAnotherBranch(t *testing.T) {
	f := newFixture()
	pr := f.gh.prs[912]
	pr.HeadRefName = "feat/other"
	f.gh.prs[912] = pr
	rec := failedTask(t, f, 912)
	if _, err := f.app.Reset(context.Background(), rec); err == nil || !strings.Contains(err.Error(), "feat/other") {
		t.Fatalf("err = %v", err)
	}
	unchanged(t, f, rec)
}

func TestResetDeletesNothingWhenSeveralPRsShareTheBranch(t *testing.T) {
	f := newFixture()
	f.gh.prs[913] = PullRequest{Key: task.Key{Repo: "ridezoomo/armada-backend", Number: 913}, State: "closed", HeadRefName: "feat/825-provider-retries"}
	rec := failedTask(t, f, 0)
	if _, err := f.app.Reset(context.Background(), rec); err == nil || !strings.Contains(err.Error(), "#912, #913") {
		t.Fatalf("err = %v", err)
	}
	unchanged(t, f, rec)
}

func TestResetKeepsTheMarkWhenTheWorktreeCannotBeRemoved(t *testing.T) {
	f := newFixture()
	rec := failedTask(t, f, 912)
	f.wt.removeErr = errors.New("permission denied")
	got, err := f.app.Reset(context.Background(), rec)
	if err == nil || !strings.Contains(err.Error(), rec.Delegation.Worktree) || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v", err)
	}
	if got == nil || got.Reset == nil || got.Reset.PR != 912 || f.store.recs[rec.Key].Reset == nil {
		t.Fatalf("the PR link must be saved before anything is deleted: %+v", got)
	}
	c := f.store.recs[rec.Key].Reset.Cleanup
	if c == nil || c.Worktree != rec.Delegation.Worktree || c.Branch != rec.Delegation.Branch || !c.Since.IsZero() {
		t.Fatalf("the leftovers must stay recorded for the next start, with the reset finished: %+v", c)
	}
}

func TestResetMarksItsCleanupUntilTheRunIsRemoved(t *testing.T) {
	f := newFixture()
	rec := failedTask(t, f, 912)
	var during *task.ResetCleanup
	f.wt.onRemove = func() {
		if r := f.store.recs[rec.Key].Reset; r != nil {
			during = r.Cleanup
		}
	}
	got, err := f.app.Reset(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	if during == nil || !during.Since.Equal(now) {
		t.Errorf("cleanup while removing = %+v; want it marked as running", during)
	}
	if got.Reset.Cleanup != nil || f.store.recs[rec.Key].Reset.Cleanup != nil {
		t.Errorf("finished cleanup still recorded: %+v", got.Reset.Cleanup)
	}
}

func TestStartWaitsWhileAResetIsRemovingTheRun(t *testing.T) {
	f := newFixture()
	rec := failedTask(t, f, 912)
	var startErr error
	f.wt.onRemove = func() {
		f.wt.onRemove = nil
		_, startErr = f.app.Start(context.Background(), startReq(t))
	}
	f.wt.reqs = nil
	if _, err := f.app.Reset(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if startErr == nil || !strings.Contains(startErr.Error(), "being reset") {
		t.Fatalf("start during the reset's cleanup: %v", startErr)
	}
	if len(f.wt.reqs) != 0 {
		t.Errorf("start made a worktree the reset was removing: %+v", f.wt.reqs)
	}
}

func TestStartFinishesTheCleanupAResetCouldNotDo(t *testing.T) {
	f := newFixture()
	rec := failedTask(t, f, 912)
	f.wt.removeErr = errors.New("permission denied")
	if _, err := f.app.Reset(context.Background(), rec); err == nil {
		t.Fatal("want the removal error")
	}
	f.wt.removeErr, f.wt.reqs = nil, nil
	got, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.wt.removed) == 0 || f.wt.removed[0].Path != rec.Delegation.Worktree || !f.wt.removed[0].Force {
		t.Errorf("removed = %+v; want the reset's leftover worktree first", f.wt.removed)
	}
	if got.Reset != nil || len(f.wt.reqs) != 1 {
		t.Errorf("reset %+v worktrees %+v", got.Reset, f.wt.reqs)
	}
}

func TestStartRefusesATaskResetWhileItStarted(t *testing.T) {
	f := newFixture()
	rec := failedTask(t, f, 912)
	f.wt.onEnsure = func() {
		f.wt.onEnsure = nil
		if _, err := f.app.Reset(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.app.Start(context.Background(), startReq(t)); err == nil || !strings.Contains(err.Error(), "reset while it started") {
		t.Fatalf("err = %v", err)
	}
	if got := f.store.recs[rec.Key]; got.Delegation != nil || got.Reset == nil {
		t.Errorf("start overwrote the reset: %+v", got)
	}
}

func TestStartAfterResetOfAnOpenPRRemovesAWorktreeLeftAtItsPath(t *testing.T) {
	f := newFixture()
	resetTask(t, f, "open")
	f.wt.removed = nil
	if _, err := f.app.Start(context.Background(), startReq(t)); err != nil {
		t.Fatal(err)
	}
	want := WorktreeRemoval{Path: "/wt/ridezoomo-armada-backend/feat/825-provider-retries", Branch: "feat/825-provider-retries", DeleteBranch: true, Force: true, RepoDir: "/src/armada-backend"}
	if !reflect.DeepEqual(f.wt.removed, []WorktreeRemoval{want}) {
		t.Errorf("removed = %+v, want %+v", f.wt.removed, want)
	}
}

// resetTask resets a failed #825 whose run had PR #912 in state prState.
func resetTask(t *testing.T, f fixture, prState string) *task.Record {
	t.Helper()
	rec := failedTask(t, f, 912)
	pr := f.gh.prs[912]
	pr.State, pr.Title = prState, "Retry everything"
	f.gh.prs[912] = pr
	f.gh.prComments = map[int][]PRComment{912: {{Kind: task.IssueComment, Author: "dev", Body: "Retry only 5xx."}}}
	got, err := f.app.Reset(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	f.wt.reqs = nil
	return got
}

func TestAutoModeDoesNotStartAResetIssue(t *testing.T) {
	f := newFixture()
	f.gh.issue.Labels = []string{"triaged"}
	rec := resetTask(t, f, "open")
	req := startReq(t)
	req.NewOnly = true
	if _, err := f.app.Start(context.Background(), req); !errors.Is(err, ErrDelegated) || !strings.Contains(err.Error(), "reset") {
		t.Fatalf("err = %v", err)
	}
	if len(f.wt.reqs) != 0 || f.store.recs[rec.Key].Reset == nil {
		t.Errorf("auto start changed a reset issue: %+v", f.wt.reqs)
	}
}

func TestStartAfterResetContinuesAnOpenPRFromItsRemoteBranch(t *testing.T) {
	f := newFixture()
	resetTask(t, f, "open")
	got, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	want := WorktreeRequest{RepoDir: "/src/armada-backend", Path: "/wt/ridezoomo-armada-backend/feat/825-provider-retries", Branch: "feat/825-provider-retries", BaseRef: "origin/feat/825-provider-retries"}
	if len(f.wt.reqs) != 1 || f.wt.reqs[0] != want {
		t.Errorf("worktree = %+v", f.wt.reqs)
	}
	d := got.Delegation
	if d.PR != 912 || d.PRURL == "" || d.PriorPR != 912 || d.BaseRef != "origin/main" || d.Implementer.ID != "" || d.CheckpointNext != "" || got.Reset != nil {
		t.Errorf("delegation = %+v reset %+v", d, got.Reset)
	}
	if len(got.Attempts) != 1 || got.Attempts[0].LogStart != 0 {
		t.Errorf("attempts = %+v", got.Attempts)
	}
}

func TestStartAfterResetOfAClosedPRUsesANewBranchFromTheBase(t *testing.T) {
	for _, state := range []string{"closed", "merged"} {
		t.Run(state, func(t *testing.T) {
			f := newFixture()
			resetTask(t, f, state)
			req := startReq(t)
			req.BranchName = "fix/825-provider-retries-again"
			got, err := f.app.Start(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if len(f.wt.reqs) != 1 || f.wt.reqs[0].Branch != req.BranchName || f.wt.reqs[0].BaseRef != "origin/main" {
				t.Errorf("worktree = %+v", f.wt.reqs)
			}
			if d := got.Delegation; d.PR != 0 || d.PriorPR != 912 || got.Reset != nil {
				t.Errorf("delegation = %+v reset %+v", d, got.Reset)
			}
		})
	}
}

func TestStartAfterResetOfAClosedPRRefusesItsBranchName(t *testing.T) {
	f := newFixture()
	resetTask(t, f, "closed")
	if _, err := f.app.Start(context.Background(), startReq(t)); err == nil || !strings.Contains(err.Error(), "PR #912") {
		t.Fatalf("err = %v", err)
	}
	if len(f.wt.reqs) != 0 {
		t.Errorf("worktree made: %+v", f.wt.reqs)
	}
}

func TestStartAfterResetWithNoPRStartsFromTheIssue(t *testing.T) {
	f := newFixture()
	delete(f.gh.prs, 912)
	rec := failedTask(t, f, 0)
	if _, err := f.app.Reset(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	f.wt.reqs = nil
	got, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.wt.reqs) != 1 || f.wt.reqs[0].BaseRef != "origin/main" {
		t.Errorf("worktree = %+v", f.wt.reqs)
	}
	if d := got.Delegation; d.PR != 0 || d.PriorPR != 0 || got.Reset != nil {
		t.Errorf("delegation = %+v reset %+v", d, got.Reset)
	}
}

func TestRunAfterResetPutsThePriorPRInAFreshSession(t *testing.T) {
	f := newFixture()
	resetTask(t, f, "closed")
	req := startReq(t)
	req.BranchName = "fix/825-provider-retries-again"
	key := started(t, f, req)
	f.claude.turn = agent.Turn{Session: task.Session{Agent: task.Claude, ID: "sid-new"}, Outcome: agent.Outcome{Status: agent.FailedRun, Summary: "stop"}}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.claude.starts != 1 || len(f.claude.resumed) != 0 {
		t.Fatalf("starts %d resumed %v", f.claude.starts, f.claude.resumed)
	}
	for _, want := range []string{"## Prior pull request", "#912", "Retry everything", "closed", "> Retry only 5xx."} {
		if !strings.Contains(f.claude.prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestBranchProposalAfterResetOffersTheOpenPRBranch(t *testing.T) {
	f := newFixture()
	resetTask(t, f, "open")
	p, err := f.app.ProposeBranch(context.Background(), startReq(t))
	if err != nil || p.Name != "feat/825-provider-retries" || p.Uncertain || !strings.Contains(p.Reason, "#912") {
		t.Fatalf("proposal = %+v, %v", p, err)
	}
}

func TestBranchProposalAfterResetOfAClosedPRAsksForAnotherName(t *testing.T) {
	f := newFixture()
	resetTask(t, f, "closed")
	p, err := f.app.ProposeBranch(context.Background(), startReq(t))
	if err != nil || !p.Uncertain || !strings.Contains(p.Reason, "#912") {
		t.Fatalf("proposal = %+v, %v", p, err)
	}
}

func TestInboxMarksAResetIssueAndKeepsFetchingIt(t *testing.T) {
	stub := seen(5, task.Discovered, "open", t0)
	stub.Reset = &task.ResetMark{At: now}
	listed := seen(6, task.Discovered, "open", t0, task.Assigned)
	listed.Reset = &task.ResetMark{At: now}
	store := &fakeStore{recs: map[task.Key]*task.Record{key(5): stub, key(6): listed}}
	gh := &fakeGitHub{items: []InboxItem{item(6, "open", t0, task.Assigned)}}
	report, err := App{Store: store, GitHub: gh, Runners: &fakeRunners{}, Now: func() time.Time { return now }}.Inbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(gh.fetched, key(5)) {
		t.Errorf("fetched %v; a reset issue with no reasons left must still be read", gh.fetched)
	}
	var found bool
	for _, e := range report.Current {
		if e.Item.Key == key(6) {
			found = e.Reset && !e.Delegated
		}
	}
	if !found {
		t.Errorf("current = %+v; want #6 marked reset", report.Current)
	}
}
