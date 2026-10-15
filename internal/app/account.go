package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

// AccountError is a gh or SSH account that differs from the task's pinned account.
// Host is set when the SSH key of that host is the one that differs.
type AccountError struct {
	Pinned   string
	Observed string
	Host     string
}

func (e *AccountError) Error() string {
	if e.Host != "" {
		return fmt.Sprintf("task is pinned to GitHub account %s, but SSH host %s pushes as %s. Point the %s entry in ~/.ssh/config at %s's key, then restart the task",
			e.Pinned, e.Host, e.Observed, hostName(e.Host), e.Pinned)
	}
	return fmt.Sprintf("task is pinned to GitHub account %s, but gh is using %s. Run `gh auth switch --user %s`, then restart the task",
		e.Pinned, e.Observed, e.Pinned)
}

func hostName(target string) string {
	if _, host, ok := strings.Cut(target, "@"); ok {
		return host
	}
	return target
}

// checkAccount runs before every launch and every implementer turn, because the
// developer can switch gh or SSH accounts at any time and the agent pushes and opens
// PRs as whoever is active. A task without a pin, new or started before pinning,
// pins the active gh account. Then the worktree's commit identity is set to the pin
// and the SSH push account must match it. rec is updated with a new pin.
func (a App) checkAccount(ctx context.Context, rec *task.Record) error {
	if a.Accounts == nil {
		return nil
	}
	d := rec.Delegation
	active, err := a.Accounts.ActiveAccount(ctx)
	if err != nil {
		return fmt.Errorf("check the GitHub account: %w", err)
	}
	if d.Account == "" {
		updated, err := a.update(ctx, rec.Key, existing(func(cur *task.Record) {
			if cur.Delegation.Account == "" {
				cur.Delegation.Account, cur.Delegation.AccountEmail = active.Login, active.Email()
			}
		}))
		if err != nil {
			return fmt.Errorf("pin the GitHub account: %w", err)
		}
		d.Account, d.AccountEmail = updated.Delegation.Account, updated.Delegation.AccountEmail
	}
	// GitHub logins are case-insensitive.
	if !strings.EqualFold(active.Login, d.Account) {
		return &AccountError{Pinned: d.Account, Observed: active.Login}
	}
	if err := a.Worktrees.ConfigureIdentity(ctx, d.Worktree, d.Account, d.AccountEmail); err != nil {
		return fmt.Errorf("set the commit identity: %w", err)
	}
	if a.RemoteAuth == nil {
		return nil
	}
	pushes, err := a.RemoteAuth.PushAccounts(ctx, d.Worktree)
	if err != nil {
		return fmt.Errorf("check the SSH push account: %w", err)
	}
	for _, push := range pushes {
		if !strings.EqualFold(push.Login, d.Account) {
			return &AccountError{Pinned: d.Account, Observed: push.Login, Host: push.Host}
		}
	}
	return nil
}
