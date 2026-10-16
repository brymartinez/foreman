package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

// The test binary plays three parts: the test, a launcher that spawns a runner
// and exits at once (as the dashboard does), and the runner itself.
func TestMain(m *testing.M) {
	switch {
	case len(os.Args) > 1 && os.Args[1] == RunCommand && os.Getenv("FOREMAN_RUNNER_TEST") == "wait":
		// A runner mid-turn: it stops only when signalled.
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer stop()
		select {
		case <-ctx.Done():
			fmt.Println("runner stopped:", context.Cause(ctx))
		case <-time.After(10 * time.Second):
		}
		os.Exit(0)
	case len(os.Args) > 2 && os.Args[1] == LearnCommand:
		fmt.Println("learner for", os.Args[2])
		os.Exit(0)
	case len(os.Args) > 1 && os.Args[1] == RunCommand:
		time.Sleep(300 * time.Millisecond)
		fmt.Println("written after the launcher exited")
		fmt.Fprintln(os.Stderr, "stderr after the launcher exited")
		_ = os.WriteFile(os.Getenv("FOREMAN_RUNNER_MARKER"), []byte(os.Getenv(LaunchEnv)), 0o644)
		os.Exit(0)
	case os.Getenv("FOREMAN_RUNNER_TEST") == "launch":
		if _, _, err := (Processes{LogDir: os.Getenv("FOREMAN_RUNNER_LOGS")}).Spawn(task.Key{Repo: "o/r", Number: 7}, "5eed"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestSpawnedRunnerOutlivesItsLauncher(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	launch := exec.Command(os.Args[0])
	launch.Env = append(os.Environ(),
		"FOREMAN_RUNNER_TEST=launch",
		"FOREMAN_RUNNER_LOGS="+dir,
		"FOREMAN_RUNNER_MARKER="+marker,
	)
	if out, err := launch.CombinedOutput(); err != nil {
		t.Fatalf("launcher: %v %s", err, out)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if got, err := os.ReadFile(marker); err == nil && len(got) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the runner died after its launcher exited")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got, _ := os.ReadFile(marker); string(got) != "5eed" {
		t.Errorf("the runner got launch token %q, want the one Spawn was given", got)
	}
	log, err := os.ReadFile(filepath.Join(dir, "o-r-7.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"written after the launcher exited", "stderr after the launcher exited"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log is missing %q:\n%s", want, log)
		}
	}
}

func TestStopSignalsTheRunnerWhenProcessListingIsUnavailable(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ps"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FOREMAN_RUNNER_TEST", "wait")
	p := Processes{LogDir: t.TempDir(), StopWait: 5 * time.Second}
	key := task.Key{Repo: "o/r", Number: 8}
	pid, logPath, err := p.Spawn(key, "5eed")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGTERM) })
	for deadline := time.Now().Add(5 * time.Second); !p.Alive(pid); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("runner never came up")
		}
	}
	// Let the runner install its signal handler.
	time.Sleep(200 * time.Millisecond)
	stopped, err := p.Stop(key, pid)
	if err != nil || !stopped {
		t.Fatalf("Stop = %v, %v", stopped, err)
	}
	if p.Alive(pid) {
		t.Error("runner still alive after Stop")
	}
	log, _ := os.ReadFile(logPath)
	for _, want := range []string{fmt.Sprintf("foreman: cancel requested; stopping runner %d", pid), "runner stopped: terminated signal received"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}
	if stopped, err := p.Stop(key, pid); err != nil || !stopped {
		t.Errorf("stopping an exited runner = %v, %v", stopped, err)
	}
}

func TestSpawnLearnRunsTheLearnerWithItsOutputInLearningLog(t *testing.T) {
	dir := t.TempDir()
	p := Processes{LogDir: dir}
	if err := p.SpawnLearn(task.Key{Repo: "o/r", Number: 7}); err != nil {
		t.Fatal(err)
	}
	if err := p.SpawnLearn(task.Key{Repo: "o/r", Source: task.Linear, ExternalID: "ENG-3"}); err != nil {
		t.Fatal(err)
	}
	var out string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		raw, _ := os.ReadFile(filepath.Join(dir, "learning.log"))
		if out = string(raw); strings.Count(out, "learner for") == 2 {
			break
		}
	}
	if !strings.Contains(out, "learner for o/r#7\n") || !strings.Contains(out, "learner for o/r#linear:ENG-3\n") {
		t.Fatalf("learning.log = %q", out)
	}
}
