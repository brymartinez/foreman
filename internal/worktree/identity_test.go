package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/app"
)

func TestSSHTargetClassifiesRemotes(t *testing.T) {
	for _, c := range []struct {
		url, target, port string
		ssh               bool
	}{
		{"git@github.com:a/b.git", "git@github.com", "", true},
		{"git@github-work:a/b.git", "git@github-work", "", true},
		{"github-work:a/b", "github-work", "", true},
		{"ssh://git@github-work/a/b.git", "git@github-work", "", true},
		{"ssh://git@ssh.github.com:443/a/b.git", "git@ssh.github.com", "443", true},
		{"git+ssh://git@github.com/a/b.git", "git@github.com", "", true},
		{"https://github.com/a/b.git", "", "", false},
		{"/tmp/origin.git", "", "", false},
		{"./origin.git", "", "", false},
		{"file:///tmp/origin.git", "", "", false},
	} {
		target, port, ok := sshTarget(c.url)
		if target != c.target || port != c.port || ok != c.ssh {
			t.Errorf("sshTarget(%q) = %q, %q, %v", c.url, target, port, ok)
		}
	}
}

// stubSSH puts an ssh on PATH that records its arguments and prints out on stderr,
// exiting 1 as GitHub does for a successful ssh -T.
func stubSSH(t *testing.T, out string) (argsFile string) {
	t.Helper()
	bin := t.TempDir()
	argsFile = filepath.Join(bin, "args")
	outFile := filepath.Join(bin, "out")
	if err := os.WriteFile(outFile, []byte(out+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$@\" > " + argsFile + "\ncat " + outFile + " >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

func cloneWithOrigin(t *testing.T, url string) string {
	clone, _ := repoWithOrigin(t)
	run(t, clone, "remote", "set-url", "origin", url)
	return clone
}

func TestPushAccountReadsTheLoginGitHubGreetsOverSSH(t *testing.T) {
	args := stubSSH(t, "Hi octo-work! You've successfully authenticated, but GitHub does not provide shell access.")
	dir := cloneWithOrigin(t, "ssh://git@github-work:2222/a/b.git")
	login, checked, err := Git{}.PushAccount(context.Background(), dir)
	if err != nil || !checked || login != "octo-work" {
		t.Fatalf("push account = %q, %v, %v", login, checked, err)
	}
	raw, _ := os.ReadFile(args)
	if got := strings.TrimSpace(string(raw)); got != "-T -o BatchMode=yes -p 2222 git@github-work" {
		t.Errorf("ssh args = %q", got)
	}
}

func TestPushAccountReportsAnSSHFailure(t *testing.T) {
	stubSSH(t, "git@github.com: Permission denied (publickey).")
	dir := cloneWithOrigin(t, "git@github.com:a/b.git")
	_, _, err := Git{}.PushAccount(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "Permission denied") || !strings.Contains(err.Error(), "git@github.com") {
		t.Errorf("err = %v", err)
	}
}

func TestPushAccountSkipsANonSSHRemote(t *testing.T) {
	args := stubSSH(t, "Hi someone!")
	dir := cloneWithOrigin(t, "https://github.com/a/b.git")
	login, checked, err := Git{}.PushAccount(context.Background(), dir)
	if err != nil || checked || login != "" {
		t.Fatalf("push account = %q, %v, %v", login, checked, err)
	}
	if _, err := os.Stat(args); err == nil {
		t.Error("ssh ran for an HTTPS remote")
	}
}

// commitAs commits without the GIT_AUTHOR_* overrides run uses, so config decides.
func commitAs(t *testing.T, dir string) (author, committer string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "commit", "-q", "--allow-empty", "-m", "pinned")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	out, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%an <%ae>|%cn <%ce>").Output()
	if err != nil {
		t.Fatal(err)
	}
	author, committer, _ = strings.Cut(strings.TrimSpace(string(out)), "|")
	return author, committer
}

func TestConfigureIdentitySetsTheWorktreeAuthorOnly(t *testing.T) {
	home := t.TempDir()
	global := filepath.Join(home, ".gitconfig")
	globalBody := "[user]\n\tname = global\n\temail = global@example.com\n"
	if err := os.WriteFile(global, []byte(globalBody), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	clone, _ := repoWithOrigin(t)
	ctx := context.Background()
	wts := t.TempDir()
	pinned, other := filepath.Join(wts, "pinned"), filepath.Join(wts, "other")
	for i, p := range []string{pinned, other} {
		req := app.WorktreeRequest{RepoDir: clone, Path: p, Branch: []string{"pinned", "other"}[i], BaseRef: "origin/main"}
		if err := (Git{}).Ensure(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	email := "42+octo-work@users.noreply.github.com"
	if err := (Git{}).ConfigureIdentity(ctx, pinned, "octo-work", email); err != nil {
		t.Fatal(err)
	}
	// A second call on resume must succeed and change nothing.
	if err := (Git{}).ConfigureIdentity(ctx, pinned, "octo-work", email); err != nil {
		t.Fatal(err)
	}
	want := "octo-work <" + email + ">"
	if author, committer := commitAs(t, pinned); author != want || committer != want {
		t.Errorf("pinned worktree commit = %q / %q, want %q", author, committer, want)
	}
	for _, dir := range []string{clone, other} {
		if author, _ := commitAs(t, dir); author != "global <global@example.com>" {
			t.Errorf("%s commit author = %q, want the global identity", dir, author)
		}
	}
	if raw, _ := os.ReadFile(global); string(raw) != globalBody {
		t.Errorf("global config changed:\n%s", raw)
	}
}
