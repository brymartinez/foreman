package worktree

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// SyncDefaultBranch fast-forwards the checkout's local default branch to origin, so
// a branch proposal reads current guidance. It never resets or overwrites local
// work: a dirty tree, a diverged branch, or another checked-out branch is an error.
func (Git) SyncDefaultBranch(ctx context.Context, dir, branch string) error {
	if _, err := git(ctx, dir, "fetch", "origin", branch); err != nil {
		return err
	}
	remote := "origin/" + branch
	if err := exec.CommandContext(ctx, "git", "-C", dir, "merge-base", "--is-ancestor", branch, remote).Run(); err != nil {
		return fmt.Errorf("local %s has diverged from %s", branch, remote)
	}
	out, _ := git(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if current := strings.TrimSpace(string(out)); current != branch {
		// The ref can still advance without touching the working tree, but the
		// guidance is read from the working tree, so the start stops either way.
		if _, err := git(ctx, dir, "fetch", "origin", branch+":"+branch); err != nil {
			return err
		}
		if current == "" {
			current = "a detached HEAD"
		}
		return fmt.Errorf("managed checkout %s is on %s, not %s", dir, current, branch)
	}
	status, err := git(ctx, dir, "status", "--porcelain=v1", "--untracked-files=no")
	if err != nil {
		return err
	}
	if len(status) > 0 {
		return fmt.Errorf("managed checkout %s has uncommitted changes on %s", dir, branch)
	}
	_, err = git(ctx, dir, "merge", "--ff-only", "--quiet", remote)
	return err
}
