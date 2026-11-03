package worktree

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/brymartinez/foreman/internal/app"
)

func (Git) ReviewChanges(ctx context.Context, dir, baseRef, head, previous string) ([]app.ReviewFile, error) {
	from, operator := baseRef, "..."
	if previous != "" {
		from, operator = previous, ".."
	}
	start, err := git(ctx, dir, "rev-parse", "--verify", "--end-of-options", from+"^{commit}")
	if err != nil {
		return nil, err
	}
	end, err := git(ctx, dir, "rev-parse", "--verify", "--end-of-options", head+"^{commit}")
	if err != nil {
		return nil, err
	}
	out, err := git(ctx, dir, "diff", "--numstat", "--no-renames", "-z", strings.TrimSpace(string(start))+operator+strings.TrimSpace(string(end)), "--")
	if err != nil {
		return nil, err
	}
	var files []app.ReviewFile
	for _, entry := range strings.Split(string(out), "\x00") {
		if entry == "" {
			continue
		}
		fields := strings.SplitN(entry, "\t", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("invalid review numstat entry")
		}
		lines := 0
		for _, count := range fields[:2] {
			if count == "-" {
				continue
			}
			n, err := strconv.Atoi(count)
			if err != nil {
				return nil, fmt.Errorf("invalid review line count: %w", err)
			}
			lines += n
		}
		files = append(files, app.ReviewFile{Path: fields[2], ChangedLines: lines})
	}
	return files, nil
}
