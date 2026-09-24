package agent

import (
	"fmt"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

type TaskBrief struct {
	Key        task.Key
	Title      string
	URL        string
	IssueBody  string
	Context    string // contents of the --context Markdown file, if any
	Branch     string
	BaseBranch string
	Worktree   string
}

// ImplementationPrompt is the execution contract handed to the implementation agent.
// It carries everything durable; the agent never depends on a prior conversation.
func ImplementationPrompt(b TaskBrief) string {
	var s strings.Builder
	fmt.Fprintf(&s, "You are the implementation agent for GitHub issue %s#%d. You are working unattended; nobody will answer questions during this turn.\n\n", b.Key.Repo, b.Key.Number)

	fmt.Fprintf(&s, "## Issue\n\n%s\n%s\n\n", b.Title, b.URL)
	body := strings.TrimSpace(b.IssueBody)
	if body == "" {
		body = "(The issue has no description.)"
	}
	s.WriteString(body + "\n\n")
	fmt.Fprintf(&s, "Read the issue comments before planning: `gh issue view %d --repo %s --comments`.\n\n", b.Key.Number, b.Key.Repo)

	if ctx := strings.TrimSpace(b.Context); ctx != "" {
		s.WriteString("## Implementation context\n\nThis context comes from the developer and is authoritative. Where it conflicts with the issue text, follow it.\n\n")
		s.WriteString(ctx + "\n\n")
	}

	fmt.Fprintf(&s, `## Workspace

You are in a dedicated git worktree at %s on branch %s, created from origin/%s.
Only change files inside this worktree. Commit your work to %s. Do not switch branches.

## How to work

- Follow the repository's AGENTS.md, CLAUDE.md, and any relevant skills.
- Plan internally, inspect the code, then implement. Resolve routine ambiguity yourself: if it can be inferred, tested, or fixed safely, do it.
- Validate with the checks the repository supports: tests, typecheck, lint, build, and behavioral verification where practical.
- Review your own diff for correctness, regressions, missing tests, and unnecessary complexity before you finish.
- When the change is review-ready: push with `+"`git push -u origin %s`"+`, then open a pull request with `+"`gh pr create --base %s --head %s`"+`. If a pull request for this branch already exists (`+"`gh pr view %s`"+`), push to it instead of opening another. The pull request body must include "Closes #%d", a summary of the change, the validation you ran, and any decisions you made.

## When you need a decision

Stop and ask only when the choice materially affects product behavior, public API semantics, persistent schema semantics, security or authorization, destructive or irreversible data changes, an important architecture tradeoff, conflicting acceptance criteria, or a requirement that cannot be safely inferred.

To ask, end your turn with status "blocked" and fill "question":
- issue: the decision needed, as one question
- context: what the code does today and what the issue says
- options: two or more, each with a short label (A, B, ...) and what it means
- recommendation: the label of the option you recommend, and nothing else
- impactIfWrong: what goes wrong if the recommendation is wrong

Do not push or open a pull request when you are blocked. Commit work in progress to the branch first.

## Final response

Reply with JSON matching the provided schema:
- "pr_opened" once the pull request exists: set prNumber, prUrl, and validation (each command you ran and whether it passed).
- "blocked" when you need a decision, as described above.
- "failed" when you cannot complete the task: say why in summary.
Set fields that do not apply to null, or [] for validation.
`, b.Worktree, b.Branch, b.BaseBranch, b.Branch, b.Branch, b.BaseBranch, b.Branch, b.Branch, b.Key.Number)
	return s.String()
}
