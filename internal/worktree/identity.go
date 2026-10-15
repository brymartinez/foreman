package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
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

// PushAccount asks GitHub which account the SSH key for origin's push URL belongs
// to. ssh resolves the remote's host through ~/.ssh/config, so a host alias uses its
// own IdentityFile, as git push does.
func (Git) PushAccount(ctx context.Context, dir string) (app.SSHAccount, error) {
	out, err := git(ctx, dir, "remote", "get-url", "--push", "origin")
	if err != nil {
		return app.SSHAccount{}, err
	}
	target, port, ok := sshTarget(strings.TrimSpace(string(out)))
	if !ok {
		return app.SSHAccount{}, nil
	}
	args := []string{"-T", "-o", "BatchMode=yes"}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, target)
	var combined bytes.Buffer
	cmd := exec.CommandContext(ctx, "ssh", args...)
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
