package agent

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Run with FOREMAN_CODEX_SANDBOX_PROBE=1 outside another sandbox after upgrading
// Codex. This uses codex sandbox, not a model call, to check the real Git policy.
func TestCodexGitProfileStagesCommitsAndPushesInALinkedWorktree(t *testing.T) {
	if os.Getenv("FOREMAN_CODEX_SANDBOX_PROBE") != "1" {
		t.Skip("set FOREMAN_CODEX_SANDBOX_PROBE=1 to test the installed Codex sandbox")
	}
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(root, ".codex-git-probe-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	clone, wt, origin := filepath.Join(dir, "clone"), filepath.Join(dir, "worktree"), filepath.Join(dir, "origin.git")
	for _, args := range [][]string{
		{"init", "-q", clone},
		{"init", "--bare", "-q", origin},
		{"-C", clone, "remote", "add", "origin", origin},
		{"-C", clone, "-c", "user.name=probe", "-c", "user.email=probe@example.test", "commit", "-q", "--allow-empty", "-m", "init"},
		{"-C", clone, "worktree", "add", "-q", "-b", "probe", wt},
	} {
		if out, err := exec.Command(git, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	for _, name := range []string{"FETCH_HEAD", "ORIG_HEAD"} {
		if err := os.WriteFile(filepath.Join(clone, ".git", name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(wt, "test.txt"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	preview := exec.Command(codex, append(append([]string{"debug", "prompt-input"}, gitPermissionArgs(wt)...), "probe")...)
	preview.Dir = wt
	out, err := preview.CombinedOutput()
	gitGranted := bytes.Contains(out, []byte(filepath.Join(clone, ".git")))
	networkGranted := bytes.Contains(out, []byte("Network access is enabled"))
	if err != nil || !gitGranted || !networkGranted {
		t.Fatalf("effective Codex permissions: error %v, Git write %t, network %t", err, gitGranted, networkGranted)
	}
	for _, command := range [][]string{
		{git, "add", "test.txt"},
		{git, "-c", "user.name=probe", "-c", "user.email=probe@example.test", "commit", "-q", "-m", "test"},
		{git, "push", "-u", "origin", "probe"},
	} {
		args := gitPermissionArgs(wt)
		// A local test remote needs write access. Real PR pushes use the network.
		for i, arg := range args {
			if strings.HasPrefix(arg, "permissions.foreman-git.filesystem=") {
				args[i] = strings.TrimSuffix(arg, "}") + "," + fmt.Sprintf("%q=\"write\"", origin) + "}"
			}
		}
		args = append(args, "sandbox", "-P", "foreman-git", "-C", wt, "--")
		args = append(args, command...)
		cmd := exec.Command(codex, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("codex sandbox %s: %v %s", strings.Join(command, " "), err, out)
		}
	}
	out, err = exec.Command(git, "-C", wt, "log", "-1", "--format=%s").Output()
	if err != nil || strings.TrimSpace(string(out)) != "test" {
		t.Fatalf("committed subject = %q, err = %v", out, err)
	}
	out, err = exec.Command(git, "-C", wt, "config", "--get", "branch.probe.remote").Output()
	if err != nil || strings.TrimSpace(string(out)) != "origin" {
		t.Fatalf("tracking remote = %q, err = %v", out, err)
	}
}
