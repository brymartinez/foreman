package app

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

// holdSpawn makes the next spawn wait at the moment it would start a runner. It
// returns once launch is held there; call the returned func to let it go on.
func holdSpawn(t *testing.T, f fixture, launch func() (*task.Record, error)) (resume func() error) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	f.runners.entered, f.runners.release = entered, release
	done := make(chan error, 1)
	go func() {
		_, err := launch()
		done <- err
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("the first launch ended before it spawned: %v", err)
	}
	return func() error {
		close(release)
		return <-done
	}
}

// deadPID is the PID of a process that has exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestASecondStartWhileTheFirstIsSpawningIsRefusedAndOneRunnerStarts(t *testing.T) {
	f := newFixture()
	req := startReq(t)
	resume := holdSpawn(t, f, func() (*task.Record, error) { return f.app.Start(context.Background(), req) })
	key := task.Key{Repo: "ridezoomo/armada-backend", Number: 825}
	claim := f.store.recs[key].Delegation.Launch
	if claim == nil || claim.Token == "" || claim.PID != os.Getpid() {
		t.Fatalf("the first start spawned without a launch claim: %+v", claim)
	}

	_, err := f.app.Start(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "already starting") {
		t.Fatalf("second start err = %v", err)
	}
	if got := f.store.recs[key].Delegation.Launch; got == nil || *got != *claim {
		t.Fatalf("the second start changed the claim: %+v, want %+v", got, claim)
	}
	if err := resume(); err != nil {
		t.Fatal(err)
	}

	if len(f.runners.spawned) != 1 || f.runners.tokens[0] != claim.Token {
		t.Fatalf("spawned %d runners with tokens %q", len(f.runners.spawned), f.runners.tokens)
	}
	d := f.store.recs[key].Delegation
	if d.RunnerPID != 4242 || d.Launch == nil || d.Launch.Token != claim.Token {
		t.Fatalf("the first launch lost its runner: pid %d, claim %+v", d.RunnerPID, d.Launch)
	}
	if err := f.app.RunTask(context.Background(), key, 4242, claim.Token, io.Discard); err != nil {
		t.Fatalf("the spawned runner could not take the task: %v", err)
	}
}

func TestAStartWhileAResumeIsSpawningIsRefused(t *testing.T) {
	f := newFixture()
	key := waiting(t, f)
	spawned := len(f.runners.spawned)
	resume := holdSpawn(t, f, func() (*task.Record, error) { return f.app.Resume(context.Background(), key) })
	claim := f.store.recs[key].Delegation.Launch
	if claim == nil {
		t.Fatal("the resume spawned without a launch claim")
	}

	if _, err := f.app.Start(context.Background(), startReq(t)); err == nil || !strings.Contains(err.Error(), "already starting") {
		t.Fatalf("start err = %v", err)
	}
	if err := resume(); err != nil {
		t.Fatal(err)
	}
	if got := len(f.runners.spawned) - spawned; got != 1 {
		t.Fatalf("spawned %d runners", got)
	}
	if d := f.store.recs[key].Delegation; d.Launch == nil || d.Launch.Token != claim.Token || d.RunnerPID != 4242 {
		t.Fatalf("the resume lost its runner: pid %d, claim %+v", d.RunnerPID, d.Launch)
	}
}

func TestARunnerWithoutTheLaunchTokenDoesNotTakeTheTask(t *testing.T) {
	for _, tc := range []struct{ name, token string }{
		{"another launch's token", "other"},
		{"no token", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()
			rec, err := f.app.Start(context.Background(), startReq(t))
			if err != nil {
				t.Fatal(err)
			}
			before := *f.store.recs[rec.Key].Delegation

			err = f.app.RunTask(context.Background(), rec.Key, 4243, tc.token, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "launch") {
				t.Fatalf("err = %v", err)
			}
			d := f.store.recs[rec.Key].Delegation
			if d.RunnerPID != before.RunnerPID || d.Launch != before.Launch || f.store.recs[rec.Key].State != task.Working {
				t.Fatalf("the refused runner changed the task: pid %d, claim %+v", d.RunnerPID, d.Launch)
			}
			if f.claude.starts != 0 {
				t.Fatalf("the refused runner ran the agent %d times", f.claude.starts)
			}
		})
	}
}

func TestTheRunnerWithTheLaunchTokenTakesTheTaskOver(t *testing.T) {
	f := newFixture()
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	var took *task.Delegation
	f.claude.during = func() {
		d := *f.store.recs[rec.Key].Delegation
		took = &d
	}
	if err := f.app.RunTask(context.Background(), rec.Key, 4242, f.runners.tokens[0], io.Discard); err != nil {
		t.Fatal(err)
	}
	if took == nil || took.Launch != nil || took.RunnerPID != 4242 {
		t.Fatalf("while running: %+v", took)
	}
}

func TestAFailedSpawnReleasesTheClaimSoTheTaskCanStartAgain(t *testing.T) {
	f := newFixture()
	f.runners.spawnErr = errors.New("fork: resource unavailable")
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != task.Failed || rec.Delegation.Launch != nil {
		t.Fatalf("state %s, claim %+v", rec.State, rec.Delegation.Launch)
	}
	f.runners.spawnErr = nil
	if _, err := f.app.Start(context.Background(), startReq(t)); err != nil {
		t.Fatalf("restart: %v", err)
	}
}

func TestAClaimItsLauncherLeftBehindIsReplacedByTheNextStart(t *testing.T) {
	f := newFixture()
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	// The launcher died before it spawned: no runner was recorded.
	old := "stale"
	f.store.recs[rec.Key].Delegation.Launch = &task.Launch{Token: old, PID: deadPID(t), Since: now}
	f.store.recs[rec.Key].Delegation.RunnerPID = 0

	if _, err := f.app.Start(context.Background(), startReq(t)); err != nil {
		t.Fatalf("a dead launcher's claim still blocks: %v", err)
	}
	claim := f.store.recs[rec.Key].Delegation.Launch
	if claim == nil || claim.Token == old || len(f.runners.tokens) != 2 || claim.Token != f.runners.tokens[1] {
		t.Fatalf("claim = %+v", claim)
	}
	if err := f.app.RunTask(context.Background(), rec.Key, 4243, old, io.Discard); err == nil {
		t.Fatal("a runner of the replaced launch took the task")
	}
}

func TestARunnerThatDiedBeforeTakingTheTaskDoesNotBlockARestartWhileTheLauncherRuns(t *testing.T) {
	f := newFixture()
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	// This process, like a dashboard, outlives the launch. The runner died before it
	// took the task, and the next refresh recorded its exit.
	if d := f.store.recs[rec.Key].Delegation; d.Launch == nil || d.Launch.PID != 0 {
		t.Fatalf("the launch still holds its claim after it recorded the runner: %+v", d.Launch)
	}
	refresh(t, f)
	if got := f.store.recs[rec.Key]; got.State != task.Failed || got.Delegation.RunnerPID != 0 {
		t.Fatalf("setup: state %s pid %d", got.State, got.Delegation.RunnerPID)
	}
	if _, err := f.app.Start(context.Background(), startReq(t)); err != nil {
		t.Fatalf("restart: %v", err)
	}
}

func TestARunnerThatTakesTheTaskBeforeItsLaunchFinishesKeepsItsRecordAndLog(t *testing.T) {
	f := newFixture()
	f.claude.turn = prTurn("sid-1")
	req := startReq(t)
	resume := holdSpawn(t, f, func() (*task.Record, error) { return f.app.Start(context.Background(), req) })
	key := task.Key{Repo: "ridezoomo/armada-backend", Number: 825}
	if err := f.app.RunTask(context.Background(), key, 4242, f.runners.tokens[0], io.Discard); err != nil {
		t.Fatal(err)
	}
	ran := *f.store.recs[key]
	if err := resume(); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != ran.State || got.Delegation.RunnerPID != 0 || got.Delegation.Launch != nil {
		t.Fatalf("the launch overwrote the finished run: state %s pid %d claim %+v", got.State, got.Delegation.RunnerPID, got.Delegation.Launch)
	}
	if got.Delegation.RunnerLog == "" {
		t.Fatal("the runner log was not recorded")
	}
}
