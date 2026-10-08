// Package runner launches the detached per-task process (`foreman __run owner/repo#N`)
// and checks whether one is still alive. There is no daemon.
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

func (p Processes) lockPath(pid int) string {
	return filepath.Join(p.LogDir, fmt.Sprintf(".runner-%d.lock", pid))
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
	lock, err := os.CreateTemp(p.LogDir, ".runner-*.lock")
	if err != nil {
		return 0, "", err
	}
	defer lock.Close()
	defer os.Remove(lock.Name())
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return 0, "", err
	}
	cmd := exec.Command(exe, RunCommand, fmt.Sprintf("%s#%d", key.Repo, key.Number))
	// The log must be the file itself. Any other writer makes this process copy
	// the output through a pipe, and the runner dies once this process exits.
	cmd.Stdout, cmd.Stderr = log, log
	// The runner inherits this locked descriptor and holds it until it exits.
	cmd.ExtraFiles = []*os.File{lock}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, "", err
	}
	pid := cmd.Process.Pid
	if err := os.Rename(lock.Name(), p.lockPath(pid)); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Process.Release()
		return 0, "", err
	}
	return pid, logPath, cmd.Process.Release()
}

// Alive reports whether pid is a running foreman runner. The inherited lock
// distinguishes it from an unrelated process that later reuses the pid.
func (p Processes) Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil && err != syscall.EPERM {
		return false
	}
	lock, err := os.OpenFile(p.lockPath(pid), os.O_RDWR, 0)
	if err == nil {
		defer lock.Close()
		return syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == syscall.EWOULDBLOCK
	}
	if !os.IsNotExist(err) {
		return false
	}
	// A runner launched before lock files were introduced still needs the old check.
	out, err := exec.Command("ps", "-p", fmt.Sprint(pid), "-o", "command=").Output()
	return err == nil && bytes.Contains(out, []byte(" "+RunCommand+" "))
}

// Stop sends the runner SIGTERM, which cancels its context and so stops the agent's
// process group. It notes the request in the task log, then waits for the
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
