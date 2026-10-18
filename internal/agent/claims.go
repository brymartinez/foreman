package agent

import (
	"fmt"
	"strings"
)

// claimRules tell the implementer what a claim set holds and that it stays private.
// Foreman keeps claims in the task file and hands them to the reviewers alone.
const claimRules = `- Cover every acceptance criterion in the PRD posted on the issue. Group criteria in one claim only when the same evidence supports them.
- criteria: the acceptance criteria the claim covers, as the PRD words them or with labels that identify them.
- behavior: the behavior the change delivers, in one or two sentences.
- evidence: the code and tests that deliver and check the behavior, as file paths with symbols or line numbers.
- check: the check you ran and its result, or why you did not run one.
- After every fix, return the full updated set, not only the claims that changed.
- Claims are internal. Keep them, their evidence, and any verification results out of the pull request title and description, pull request and issue comments, commit messages, and files in the worktree.
`

// claimsSection asks a fresh implementation turn for its claim set.
func claimsSection(s *strings.Builder) {
	s.WriteString(`## Claims

With "pr_opened" or "review_ready", return a full claim set in "claims", one claim per acceptance criterion. Foreman stores it locally, and both reviewers test each claim against the PRD, the code, and the evidence you cite. Set claims to null for every other status.

`)
	s.WriteString(claimRules + "\n")
}

// ClaimsMessage ends a resume message for a task with claim verification, so a fix,
// an answer, or a merge comes back with its claims.
const ClaimsMessage = "\nWith \"pr_opened\" or \"review_ready\", return the full updated set of claims in \"claims\", one per acceptance criterion in the PRD. Reviewers test each claim against the PRD, the code, and its evidence.\n\n" + claimRules

// MissingClaimsMessage resumes a turn that finished a change without its claims.
func MissingClaimsMessage(status Status) string {
	return fmt.Sprintf("Your %q outcome had no claims. Foreman does not review a change without them. Do not change the code, commit, push, or edit the pull request. End with the same %q outcome and the same other fields, and add the full claim set in \"claims\":\n\n%s", status, status, claimRules)
}
