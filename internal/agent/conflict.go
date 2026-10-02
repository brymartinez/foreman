package agent

import (
	"fmt"
	"strings"
)

// Conflict is a merge conflict between the task's pull request and its base branch,
// which the implementer repairs after foreman handed the PR over (D130).
type Conflict struct {
	PR       int
	Base     string // the base branch name, e.g. main
	BaseHead string // the base commit the merge must include
}

// ConflictMessage resumes the session that wrote the pull request so it merges the
// base branch into it.
func ConflictMessage(c Conflict) string {
	var s strings.Builder
	fmt.Fprintf(&s, "Pull request #%d now has merge conflicts with %s.\n\n", c.PR, c.Base)
	conflictRules(&s, c)
	return s.String()
}

// conflictRules are the same for a resumed turn and a fresh one.
func conflictRules(s *strings.Builder, c Conflict) {
	fmt.Fprintf(s, `Repair it on the existing branch:

1. Run `+"`git fetch origin %s`"+`, then `+"`git merge origin/%s`"+`. The merge must include commit %s.
2. Resolve every conflict. Keep the intent of both sides, and keep the behavior this pull request adds.
3. Run the repository's checks, and fix what the merge broke.
4. Commit the merge and push it with `+"`git push`"+`. Do not rebase, and do not force push.

If you cannot finish, run `+"`git merge --abort`"+` so the worktree is clean, and end with "blocked" and the questions, or "failed" and the reason. Otherwise end with "pr_opened" for PR #%d and the validation you ran. Change nothing the merge does not need.
`, c.Base, c.Base, c.BaseHead, c.PR)
}
