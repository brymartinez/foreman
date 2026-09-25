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

	"github.com/brymartinez/foreman/internal/logs"
	"github.com/brymartinez/foreman/internal/task"
)

// RunCommand is the hidden CLI verb the runner process executes.
const RunCommand = "__run"

type Processes struct {
	LogDir string
}

// Spawn starts the runner in its own session so closing the terminal does not kill it.
// Its stdout and stderr go to an append-only log per task.
func (p Processes) Spawn(key task.Key) (int, string, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, "", err
	}
	if err := os.MkdirAll(p.LogDir, 0o755); err != nil {
		return 0, "", err
	}
	logPath := filepath.Join(p.LogDir, fmt.Sprintf("%s-%d.log", strings.ReplaceAll(string(key.Repo), "/", "-"), key.Number))
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, "", err
	}
	defer log.Close()
	cmd := exec.Command(exe, RunCommand, fmt.Sprintf("%s#%d", key.Repo, key.Number))
	stamped := &logs.Writer{W: log}
	cmd.Stdout, cmd.Stderr = stamped, stamped
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
