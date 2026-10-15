package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/brymartinez/foreman/internal/app"
)

// ConfigureIdentity sets the commit author and committer in the worktree's own config.
// Linked worktrees share .git/config, so plain git config would also change the main
// checkout and every other task. extensions.worktreeConfig gives each worktree its own
// config.worktree file.
func (Git) ConfigureIdentity(ctx context.Context, dir, name, email string) error {
	if _, err := git(ctx, dir, "config", "extensions.worktreeConfig", "true"); err != nil {
		return err
	}
	if _, err := git(ctx, dir, "config", "--worktree", "user.name", name); err != nil {
		return err
	}
	_, err := git(ctx, dir, "config", "--worktree", "user.email", email)
	return err
}

var greeting = regexp.MustCompile(`Hi ([A-Za-z0-9-]+)!`)

// PushAccounts asks GitHub which account each of origin's SSH push URLs
// authenticates as. git push sends to every push URL, so each one is probed. The
// probe uses the SSH command git push would use, and ssh resolves the host through
// ~/.ssh/config, so a host alias uses its own IdentityFile.
func (Git) PushAccounts(ctx context.Context, dir string) ([]app.SSHAccount, error) {
	out, err := git(ctx, dir, "remote", "get-url", "--push", "--all", "origin")
	if err != nil {
		return nil, err
	}
	var accts []app.SSHAccount
	var sshCmd []string
	for _, remote := range strings.Fields(string(out)) {
		target, port, ok := sshTarget(remote)
		if !ok {
			continue
		}
		if sshCmd == nil {
			if sshCmd, err = sshCommand(ctx, dir); err != nil {
				return nil, err
			}
		}
		acct, err := probe(ctx, sshCmd, target, port)
		if err != nil {
			return nil, err
		}
		accts = append(accts, acct)
	}
	return accts, nil
}

// sshCommand is the program and leading arguments git push runs for SSH, in git's
// order: GIT_SSH_COMMAND, core.sshCommand, GIT_SSH, then ssh. Git runs the first two
// through the shell, so they may carry options such as -i.
func sshCommand(ctx context.Context, dir string) ([]string, error) {
	shell := func(c string) []string { return []string{"sh", "-c", c + ` "$@"`, c} }
	if c := os.Getenv("GIT_SSH_COMMAND"); c != "" {
		return shell(c), nil
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "config", "--get", "core.sshCommand")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		if c := strings.TrimSpace(stdout.String()); c != "" {
			return shell(c), nil
		}
	case !errors.As(err, &exit) || exit.ExitCode() != 1:
		return nil, fmt.Errorf("git config --get core.sshCommand: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if c := os.Getenv("GIT_SSH"); c != "" {
		return []string{c}, nil
	}
	return []string{"ssh"}, nil
}

func probe(ctx context.Context, sshCmd []string, target, port string) (app.SSHAccount, error) {
	args := append([]string{}, sshCmd[1:]...)
	args = append(args, "-T", "-o", "BatchMode=yes")
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, target)
	var combined bytes.Buffer
	cmd := exec.CommandContext(ctx, sshCmd[0], args...)
	cmd.Stdout, cmd.Stderr = &combined, &combined
	// GitHub exits 1 after a successful greeting because it offers no shell, so the
	// greeting, not the exit status, decides.
	runErr := cmd.Run()
	if m := greeting.FindStringSubmatch(combined.String()); m != nil {
		return app.SSHAccount{Host: target, Login: m[1]}, nil
	}
	if runErr == nil {
		runErr = errors.New("no greeting")
	}
	return app.SSHAccount{}, fmt.Errorf("ssh -T %s did not name a GitHub account: %w: %s", target, runErr, strings.TrimSpace(combined.String()))
}

// sshTarget returns the [user@]host and port that git would connect to for an SSH
// remote URL, or ok false for HTTPS, file, and local path remotes.
func sshTarget(remote string) (target, port string, ok bool) {
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil || (u.Scheme != "ssh" && u.Scheme != "git+ssh") || u.Host == "" {
			return "", "", false
		}
		target = u.Hostname()
		if u.User != nil {
			target = u.User.Username() + "@" + target
		}
		return target, u.Port(), true
	}
	// git reads host:path as scp-like SSH only when no slash comes before the colon.
	host, _, found := strings.Cut(remote, ":")
	if !found || host == "" || strings.Contains(host, "/") {
		return "", "", false
	}
	return host, "", true
}
