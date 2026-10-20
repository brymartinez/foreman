package worktree

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// ChangedPaths lists every path that differs between from and to, sorted. A rename
// lists both its old and new path. It refuses a from that is not to or an ancestor
// of it, because a full result on another line of history says nothing about to. It
// also refuses a from that is not a commit SHA: a name such as HEAD or a branch moves,
// so a result stored under it would later name another commit.
func (Git) ChangedPaths(ctx context.Context, dir, from, to string) ([]string, error) {
	sha, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", from+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("%s is not a commit", from)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(sha)), strings.ToLower(from)) {
		return nil, fmt.Errorf("%s names a ref, not a commit SHA", from)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "merge-base", "--is-ancestor", from, to)
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return nil, fmt.Errorf("%s is not an ancestor of %s", from, to)
		}
		return nil, fmt.Errorf("check that %s is an ancestor of %s: %w", from, to, err)
	}
	out, err := git(ctx, dir, "diff", "--name-only", "--no-renames", "-z", from, to)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths, nil
}
