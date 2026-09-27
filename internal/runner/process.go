// Package runner launches the detached per-task process (`foreman __run owner/repo#N`)
// and checks whether one is still alive. There is no daemon (D5).
package runner

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/brymartinez/foreman/internal/logs"
	"github.com/brymartinez/foreman/internal/task"
)

// RunCommand is the hidden CLI verb the runner process executes.
const RunCommand = "__run"

type Processes struct {
	LogDir string
	// StopWait is how long Stop waits for the runner to exit; zero means 30 seconds.
	StopWait time.Duration
}

func (p Processes) logPath(key task.Key) string {
	return filepath.Join(p.LogDir, fmt.Sprintf("%s-%d.log", strings.ReplaceAll(string(key.Repo), "/", "-"), key.Number))
}

// Spawn starts the runner in its own session so closing the terminal does not kill it.
// Its stdout and stderr go to an append-only log per task; the runner timestamps its own lines.
func (p Processes) Spawn(key task.Key) (int, string, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, "", err
	}
	if err := os.MkdirAll(p.LogDir, 0o755); err != nil {
		return 0, "", err
	}
	logPath := p.logPath(key)
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, "", err
	}
	defer log.Close()
	cmd := exec.Command(exe, RunCommand, fmt.Sprintf("%s#%d", key.Repo, key.Number))
	// The log must be the file itself. Any other writer makes this process copy
	// the output through a pipe, and the runner dies once this process exits.
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, "", err
	}
	pid := cmd.Process.Pid
	return pid, logPath, cmd.Process.Release()
}

// Alive reports whether pid is a running foreman runner. Checking the command line
// guards against the pid having been reused by an unrelated process.
func (Processes) Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil && err != syscall.EPERM {
		return false
	}
	out, err := exec.Command("ps", "-p", fmt.Sprint(pid), "-o", "command=").Output()
	return err == nil && bytes.Contains(out, []byte(" "+RunCommand+" "))
}

// Stop sends the runner SIGTERM, which cancels its context and so stops the agent's
// process group (D59). It notes the request in the task log, then waits for the
// runner to exit. false means it is still running when the wait ends.
func (p Processes) Stop(key task.Key, pid int) (bool, error) {
	if !p.Alive(pid) {
		return true, nil
	}
	if f, err := os.OpenFile(p.logPath(key), os.O_WRONLY|os.O_APPEND, 0); err == nil {
		fmt.Fprintf(&logs.Writer{W: f}, "foreman: cancel requested; stopping runner %d\n", pid)
		f.Close()
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		if err == syscall.ESRCH {
			return true, nil
		}
		return false, err
	}
	wait := p.StopWait
	if wait == 0 {
		wait = 30 * time.Second
	}
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if !p.Alive(pid) {
			return true, nil
		}
	}
	return !p.Alive(pid), nil
}
