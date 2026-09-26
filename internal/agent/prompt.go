package agent

import (
	"fmt"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

type TaskBrief struct {
	Key       task.Key
	Title     string
	URL       string
	IssueBody string
	Context   string // contents of the --context Markdown file, if any
	// StageInstructions is the developer's instructions for the stage this prompt is
	// for (implement, review), global then repo. Callers set only that stage's (D47).
	StageInstructions string
	// PRInstructions is the developer's pr stage, for writing the PR description. The
	// implementer writes the PR in the same turn, so it rides along (D49).
	PRInstructions string
	Branch         string
	BaseBranch     string
	Worktree       string
	// Decisions the developer already answered; a fresh turn must honor them.
	Decisions []task.Decision
	// OpenFindings are the latest review's findings when a fresh turn replaces a
	// session that could not be resumed.
	OpenFindings []task.Finding
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

	stageSection(&s, b.StageInstructions)

	if ctx := strings.TrimSpace(b.Context); ctx != "" {
		s.WriteString("## Implementation context\n\nThis context comes from the developer and is authoritative. Where it conflicts with the issue text, follow it.\n\n")
		s.WriteString(ctx + "\n\n")
	}

	if answered := answeredDecisions(b.Decisions); len(answered) > 0 {
		s.WriteString("## Decisions already made\n\nThe developer answered these earlier.\n\n")
		for _, d := range answered {
			fmt.Fprintf(&s, "- Q: %s\n  A: %s\n", d.Question.Issue, d.Answer)
		}
		s.WriteString("\nWork already committed on the branch may be from an earlier turn.\n\n")
	}

	if len(b.OpenFindings) > 0 {
		s.WriteString("## Outstanding review findings\n\nA reviewer requested these changes on your pull request.\n\n")
		for _, f := range b.OpenFindings {
			fmt.Fprintf(&s, "- [%s] %s", f.Severity, f.Summary)
			if f.File != "" {
				fmt.Fprintf(&s, " (%s)", f.File)
			}
			s.WriteString("\n")
		}
		s.WriteString("\n")
	}

	fmt.Fprintf(&s, `## Workspace

You are in a dedicated git worktree at %s on branch %s, created from origin/%s.
Only change files inside this worktree. Commit your work to %s. Do not switch branches.

## Opening the pull request

- When the change is review-ready: commit everything (the worktree must be clean), push with `+"`git push -u origin %s`"+`, then open a pull request with `+"`gh pr create --base %s --head %s`"+`. If a pull request for this branch already exists (`+"`gh pr view %s`"+`), push to it instead of opening another. The pull request body must include "Closes #%d"%s.
- Take prNumber and prUrl from `+"`gh pr view --json number,url`"+` after pushing; do not guess them.
%s
## When you need a decision

To ask, end your turn with status "blocked" and fill "questions", one entry per decision. Each entry has:
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
Set fields that do not apply to null, or [] for validation and questions.
`, b.Worktree, b.Branch, b.BaseBranch, b.Branch, b.Branch, b.BaseBranch, b.Branch, b.Branch, b.Key.Number, prBodyRule(b.PRInstructions), prSection(b.PRInstructions))
	return s.String()
}

func answeredDecisions(ds []task.Decision) []task.Decision {
	var out []task.Decision
	for _, d := range ds {
		if d.Answer != "" {
			out = append(out, d)
		}
	}
	return out
}

// ResumeMessage continues the session that asked the questions, with every answer.
func ResumeMessage(ds []task.Decision) string {
	var s strings.Builder
	if len(ds) == 1 {
		s.WriteString("The developer answered your question.\n\n")
	} else {
		s.WriteString("The developer answered your questions.\n\n")
	}
	for _, d := range ds {
		fmt.Fprintf(&s, "Question: %s\nDecision: %s\n\n", d.Question.Issue, d.Answer)
	}
	s.WriteString("Continue the task with these decisions under the same instructions as before. End with the same JSON outcome: pr_opened, blocked, or failed.\n")
	return s.String()
}

// stageSection passes the developer's stage instructions through as written. The
// agent's own CLAUDE.md or AGENTS.md still load on their own; these add to them.
func stageSection(s *strings.Builder, text string) {
	if text = strings.TrimSpace(text); text == "" {
		return
	}
	s.WriteString("## Developer workflow instructions\n\nThe developer's instructions for this stage of the work. Where they conflict with the task context or decisions below, the task context and decisions win.\n\n")
	s.WriteString(text + "\n\n")
}

// prBodyRule completes the PR bullet with a pointer to the developer's PR
// instructions. "Closes #N" is required regardless, so GitHub links the issue and
// closes it on merge.
func prBodyRule(pr string) string {
	if strings.TrimSpace(pr) == "" {
		return ""
	}
	return `; otherwise write it as the "Pull request description" section below says`
}

func prSection(text string) string {
	if text = strings.TrimSpace(text); text == "" {
		return ""
	}
	return "\n## Pull request description\n\nThe developer's instructions for the pull request body. Follow them when you open the pull request and whenever you update its description (`gh pr edit --body`). They apply only to the pull request, not to the code.\n\n" + text + "\n"
}
