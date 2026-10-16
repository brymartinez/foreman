package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	wantRm := WorktreeRemoval{Path: d.Worktree, Branch: d.Branch, DeleteBranch: true, Force: true, RepoDir: "/src/armada-backend"}
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

func TestStartAfterResetRefusesAndKeepsAWorktreeAtTheOpenPRPath(t *testing.T) {
	f := newFixture()
	f.app.WorktreesDir = t.TempDir()
	resetTask(t, f, "open")
	f.wt.removed = nil
	path := filepath.Join(f.app.WorktreesDir, "ridezoomo-armada-backend", "feat", "825-provider-retries")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(path, "notes.txt"), []byte("someone's work"), 0o644)
	if _, err := f.app.Start(context.Background(), startReq(t)); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v", err)
	}
	if len(f.wt.removed) != 0 || len(f.wt.reqs) != 0 {
		t.Errorf("removed %+v, made %+v", f.wt.removed, f.wt.reqs)
	}
	if _, err := os.Stat(filepath.Join(path, "notes.txt")); err != nil {
		t.Errorf("the worktree's work is gone: %v", err)
	}
	if _, err := f.app.Start(context.Background(), startReq(t)); err == nil || strings.Contains(err.Error(), "being started") {
		t.Errorf("a refused start must release its claim: %v", err)
	}
}

// claimed puts a reset issue under another command's claim from process pid.
func claimed(t *testing.T, f fixture, pid int, since time.Time) {
	t.Helper()
	rec := resetTask(t, f, "open")
	f.store.recs[rec.Key].Reset.Cleanup = &task.ResetCleanup{Since: since, Owner: "other", PID: pid}
	f.wt.removed, f.wt.reqs = nil, nil
}

func TestAClaimHeldLongByARunningProcessStillBlocksAStart(t *testing.T) {
	f := newFixture()
	claimed(t, f, os.Getpid(), now.Add(-time.Hour))
	if _, err := f.app.Start(context.Background(), startReq(t)); err == nil || !strings.Contains(err.Error(), "being started") {
		t.Fatalf("err = %v", err)
	}
	if len(f.wt.removed) != 0 || len(f.wt.reqs) != 0 {
		t.Errorf("removed %+v, made %+v", f.wt.removed, f.wt.reqs)
	}
}

func TestAClaimOfAnExitedProcessLetsAStartThrough(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	f := newFixture()
	claimed(t, f, cmd.Process.Pid, now)
	if _, err := f.app.Start(context.Background(), startReq(t)); err != nil {
		t.Fatalf("a dead owner's claim still blocks: %v", err)
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

func TestASecondStartAfterResetCannotRemoveTheFirstStartsWorktree(t *testing.T) {
	f := newFixture()
	resetTask(t, f, "open")
	f.wt.removed = nil
	var second error
	// The second start reads the reset record, then pauses until the first start
	// has made its worktree.
	f.wt.onEnsure = func() {
		f.wt.onEnsure = nil
		_, second = f.app.Start(context.Background(), startReq(t))
	}
	first, err := f.app.Start(context.Background(), startReq(t))
	if second == nil || !strings.Contains(second.Error(), "being started") {
		t.Fatalf("second start: %v", second)
	}
	if err != nil || first.Delegation == nil || first.Reset != nil {
		t.Fatalf("first start: %+v, %v", first, err)
	}
	if len(f.wt.removed) != 0 {
		t.Errorf("removed = %+v; a start after a finished reset removes nothing", f.wt.removed)
	}
}

func TestAStartThatFailsAfterClaimingAResetLetsTheNextStartThrough(t *testing.T) {
	f := newFixture()
	resetTask(t, f, "open")
	f.wt.ensureErr = errors.New("disk full")
	if _, err := f.app.Start(context.Background(), startReq(t)); err == nil {
		t.Fatal("want the Ensure error")
	}
	f.wt.ensureErr = nil
	if _, err := f.app.Start(context.Background(), startReq(t)); err != nil {
		t.Fatalf("a failed start kept its claim: %v", err)
	}
}

func TestResetRefusesARunOtherThanTheOneConfirmed(t *testing.T) {
	cases := map[string]func(d *task.Delegation){
		"another start":      func(d *task.Delegation) { d.StartedAt = d.StartedAt.Add(time.Minute) },
		"another worktree":   func(d *task.Delegation) { d.Worktree = "/wt/other" },
		"another branch":     func(d *task.Delegation) { d.Branch = "feat/other" },
		"another runner log": func(d *task.Delegation) { d.RunnerLog = "/logs/other.log" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			rec := failedTask(t, f, 912)
			shown := *rec
			d := *rec.Delegation
			change(&d)
			shown.Delegation = &d
			if _, err := f.app.Reset(context.Background(), &shown); err == nil || !strings.Contains(err.Error(), "not the run you confirmed") {
				t.Fatalf("err = %v", err)
			}
			unchanged(t, f, rec)
		})
	}
}

func TestAStartRetriesOnlyTheCleanupThatFailed(t *testing.T) {
	f := newFixture()
	rec := failedTask(t, f, 912)
	// A directory where the log was cannot be removed with os.Remove.
	if err := os.Remove(rec.Delegation.RunnerLog); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(rec.Delegation.RunnerLog, "x"), 0o755)
	if _, err := f.app.Reset(context.Background(), rec); err == nil || !strings.Contains(err.Error(), "runner log") {
		t.Fatalf("err = %v", err)
	}
	c := f.store.recs[rec.Key].Reset.Cleanup
	if c == nil || c.Worktree != "" || c.Branch != "" || c.RunnerLog != rec.Delegation.RunnerLog {
		t.Fatalf("cleanup = %+v; want only the runner log left", c)
	}
	os.RemoveAll(rec.Delegation.RunnerLog)
	f.wt.removed = nil
	if _, err := f.app.Start(context.Background(), startReq(t)); err != nil {
		t.Fatal(err)
	}
	if len(f.wt.removed) != 0 {
		t.Errorf("start removed %+v; the worktree was already gone after the reset", f.wt.removed)
	}
}

func TestAResetThatKeepsOnlyTheBranchListsOnlyTheBranch(t *testing.T) {
	f := newFixture()
	rec := failedTask(t, f, 912)
	f.wt.removeErr = fmt.Errorf("%w: branch is checked out elsewhere", ErrBranchNotDeleted)
	if _, err := f.app.Reset(context.Background(), rec); err == nil {
		t.Fatal("want the branch error")
	}
	c := f.store.recs[rec.Key].Reset.Cleanup
	if c == nil || c.Worktree != "" || c.Branch != rec.Delegation.Branch {
		t.Fatalf("cleanup = %+v; the worktree is gone, so only the branch is left", c)
	}
}

func TestAStartLeavesAWorktreeTheResetCouldNotRemove(t *testing.T) {
	f := newFixture()
	f.app.WorktreesDir = t.TempDir()
	rec := failedTask(t, f, 912)
	path := rec.Delegation.Worktree
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(path, "notes.txt"), []byte("work"), 0o644)
	f.wt.removeErr = errors.New("permission denied")
	f.app.Reset(context.Background(), rec)
	f.wt.removeErr, f.wt.removed = nil, nil
	if _, err := f.app.Start(context.Background(), startReq(t)); err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "by hand") {
		t.Fatalf("err = %v", err)
	}
	if len(f.wt.removed) != 0 {
		t.Errorf("removed %+v; a start removes no worktree", f.wt.removed)
	}
	if _, err := os.Stat(filepath.Join(path, "notes.txt")); err != nil {
		t.Errorf("the worktree's files are gone: %v", err)
	}
	os.RemoveAll(path)
	got, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatalf("start after the developer removed the leftovers: %v", err)
	}
	if got.Reset != nil || len(f.wt.removed) != 0 {
		t.Errorf("reset %+v removed %+v", got.Reset, f.wt.removed)
	}
}

func TestAStartLeavesABranchTheResetKept(t *testing.T) {
	f := newFixture()
	rec := failedTask(t, f, 912)
	branch := rec.Delegation.Branch
	f.wt.removeErr = fmt.Errorf("%w: checked out elsewhere", ErrBranchNotDeleted)
	f.app.Reset(context.Background(), rec)
	f.wt.removeErr, f.wt.removed = nil, nil
	f.wt.branches = map[string]bool{branch: true}
	if _, err := f.app.Start(context.Background(), startReq(t)); err == nil || !strings.Contains(err.Error(), "branch "+branch) {
		t.Fatalf("err = %v", err)
	}
	if len(f.wt.removed) != 0 {
		t.Errorf("removed %+v; a start deletes no branch", f.wt.removed)
	}
	f.wt.branches = nil
	if _, err := f.app.Start(context.Background(), startReq(t)); err != nil {
		t.Fatalf("start after the developer deleted the branch: %v", err)
	}
}

func TestStartRecordsTheCheckoutThatHoldsTheBranch(t *testing.T) {
	f := newFixture()
	rec := failedTask(t, f, 912)
	if rec.Delegation.RepoDir != "/src/armada-backend" {
		t.Fatalf("repo dir = %q", rec.Delegation.RepoDir)
	}
}

func TestResetFindsTheCheckoutOfAnOlderTaskThroughItsWorktree(t *testing.T) {
	f := newFixture()
	f.app.WorktreesDir = t.TempDir()
	rec := failedTask(t, f, 912)
	rec.Delegation.RepoDir = ""
	if err := os.MkdirAll(rec.Delegation.Worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	f.wt.common = "/elsewhere/.git"
	if _, err := f.app.Reset(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if len(f.wt.removed) != 1 || f.wt.removed[0].RepoDir != "/elsewhere/.git" {
		t.Errorf("removed = %+v", f.wt.removed)
	}
}

func TestResetOfAnOlderTaskDeletesNothingWhenItsWorktreePathHoldsAnotherRepository(t *testing.T) {
	f := newFixture()
	f.app.WorktreesDir = t.TempDir()
	rec := failedTask(t, f, 912)
	rec.Delegation.RepoDir = ""
	if err := os.MkdirAll(rec.Delegation.Worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	f.wt.common = "/elsewhere/.git"
	f.gh.repo.Repo = "someone/other"
	got, err := f.app.Reset(context.Background(), rec)
	if err == nil || !strings.Contains(err.Error(), "someone/other") || !strings.Contains(err.Error(), "nothing was deleted") {
		t.Fatalf("err = %v; a worktree of another repository at the path is not the run's", err)
	}
	if got != nil {
		t.Errorf("record = %+v", got)
	}
	unchanged(t, f, rec)
}

func TestResetOfAnOlderTaskWithNoWorktreeDeletesNothing(t *testing.T) {
	cases := map[string]map[string]bool{
		"branch not in the managed checkout": nil,
		// A branch of the same name there may belong to other work, since the run may
		// have started from another checkout.
		"branch of the same name in the managed checkout": {"feat/825-provider-retries": true},
	}
	for name, branches := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			rec := failedTask(t, f, 912)
			rec.Delegation.RepoDir = ""
			f.wt.branches = branches
			got, err := f.app.Reset(context.Background(), rec)
			if err == nil || !strings.Contains(err.Error(), "branch "+rec.Delegation.Branch) || !strings.Contains(err.Error(), "nothing was deleted") {
				t.Fatalf("err = %v; the checkout that holds the branch is unknown, so the reset must refuse", err)
			}
			if got != nil {
				t.Errorf("record = %+v", got)
			}
			unchanged(t, f, rec)
		})
	}
}
