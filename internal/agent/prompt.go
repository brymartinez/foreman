package agent

import (
	"fmt"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

// ReplyMarker opens every reply the agent posts on a pull request. The agent answers
// through the developer's own gh login, so the author cannot tell its replies from the
// developer's comments, and a comment the developer writes has to count. Markdown
// hides the marker, and foreman drops any comment that carries it.
const ReplyMarker = "<!-- foreman:reply -->"

type TaskBrief struct {
	Key       task.Key
	Title     string
	URL       string
	IssueBody string
	Context   string // contents of the --context Markdown file, if any
	// StageInstructions is the developer's instructions for the stage this prompt is
	// for (implement, review), global then repo. Callers set only that stage's.
	StageInstructions string
	// PRInstructions is the developer's pr stage, for writing the PR description. The
	// implementer writes the PR in the same turn, so it rides along.
	PRInstructions string
	Branch         string
	BaseBranch     string
	Worktree       string
	// Decisions the developer already answered; a fresh turn must honor them.
	Decisions []task.Decision
	// OpenFindings are the latest review's findings when a fresh turn replaces a
	// session that could not be resumed.
	OpenFindings []task.Finding
	// Planning makes the turn write the planning documents instead of code.
	// PlanInstructions is the developer's plan stage for it.
	Planning         bool
	PlanInstructions string
	// Plan is the planning documents already posted on the issue.
	Plan []task.PlanComment
	// PRD is the posted PRD's text, for the reviewer, who has no network.
	PRD string
	// PR is the task's pull request, and Feedback the comments on it still waiting for
	// an answer when a fresh turn replaces a session that could not be resumed.
	PR       int
	Feedback []FeedbackItem
	// Conflict is set when the turn repairs a merge conflict on the PR.
	Conflict *Conflict
	// Merge is set when the PR conflicts with its base and a fresh turn has to merge it.
	Merge *MergeBrief
}

// MergeBrief is the base branch a PR conflicts with and the files that conflict.
type MergeBrief struct {
	Base  string
	Files []string
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

	stageSection(&s, b.StageInstructions)
	if !b.Planning && b.Conflict == nil && b.Merge == nil {
		testFirstSection(&s)
	}
	if b.Planning {
		planningSection(&s, b.PlanInstructions)
	} else if len(b.Plan) > 0 {
		s.WriteString("## The plan\n\nYour plan for this issue is posted on it:\n\n")
		writePlanLinks(&s, b.Plan)
		s.WriteString("\n")
	}

	if ctx := strings.TrimSpace(b.Context); ctx != "" {
		s.WriteString("## Implementation context\n\nThis context comes from the developer and is authoritative. Where it conflicts with the issue text, follow it.\n\n")
		s.WriteString(ctx + "\n\n")
	}

	if answered := answeredDecisions(b.Decisions); len(answered) > 0 {
		s.WriteString("## Decisions already made\n\nThe developer answered these earlier.\n\n")
		for _, d := range answered {
			fmt.Fprintf(&s, "- Q: %s\n  A: %s\n", d.Question.Issue, d.Answer)
		}
		s.WriteString("\n")
	}

	if len(b.Feedback) > 0 {
		fmt.Fprintf(&s, "## Pull request feedback\n\nThese comments on pull request #%d are waiting for an answer.\n\n", b.PR)
		feedbackRules(&s, b.Key.Repo, b.PR)
		writeFeedback(&s, b.Key.Repo, b.PR, b.Feedback)
	}

	if c := b.Conflict; c != nil {
		fmt.Fprintf(&s, "## Merge conflict\n\nYour pull request #%d is open and was handed to the developer. It now has merge conflicts with %s. This turn only repairs them.\n\n", c.PR, c.Base)
		conflictRules(&s, *c)
		s.WriteString("\n")
	}

	if b.Merge != nil {
		s.WriteString("## Merge conflicts\n\n")
		mergeRules(&s, b.Branch, b.Merge.Base, b.Merge.Files)
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

Do not push or open a pull request when you are blocked.

## Final response

Reply with JSON matching the provided schema:
%s- "pr_opened" once the pull request exists: set prNumber, prUrl, and validation (each command you ran and whether it passed).
- "blocked" when you need a decision, as described above.
- "failed" when you cannot complete the task: say why in summary.
Set fields that do not apply to null, or [] for validation and questions. %s
`, b.Worktree, b.Branch, b.BaseBranch, b.Branch, b.Branch, b.BaseBranch, b.Branch, b.Branch, b.Key.Number, prBodyRule(b.PRInstructions), prSection(b.PRInstructions), plannedStatus(b.Planning), documentsRule(b.Planning))
	return s.String()
}

// planningSection turns the first turn into planning only: foreman posts the
// documents on the issue, then starts a fresh session to implement them.
func planningSection(s *strings.Builder, instructions string) {
	s.WriteString(`## Plan first

This turn is for planning only. Read the issue, its comments, and the code, then write four Markdown documents: analysis, prd, design, and plan (the implementation plan). foreman posts each one on the issue as its own comment under its own heading, so start each document with its content, not a title. Then foreman starts a fresh session to implement the plan. The sections below on the workspace and the pull request apply to that next turn.

Do not change, commit, or push anything in this turn. If writing the documents needs a decision from the developer, report "blocked" with the questions instead.

`)
	if text := strings.TrimSpace(instructions); text != "" {
		s.WriteString("The developer's instructions for the documents:\n\n" + text + "\n\n")
	}
	planningTestRule(s)
}

// planningTestRule keeps the plan test-first. The plan names behavior, not tests, so it
// stays short and the implementer picks the test names.
func planningTestRule(s *strings.Builder) {
	s.WriteString("For new or changed behavior, the implementation plan describes the behavior to verify, without naming every test. The implementer writes tests for that behavior first and sees them fail before writing the code.\n\n")
}

// testFirstSection is foreman's own test rule. It lives in the prompt, not in the
// stage files, because those are user-owned and absent from many repositories.
// A conflict repair or merge turn adds no behavior, so it does not get the section.
func testFirstSection(s *strings.Builder) {
	s.WriteString(`## Tests

These rules apply to every behavior change, in any repository. Run tests the way the repository's own instructions say.

1. For new or changed behavior, write the test that checks it before you write the code.
2. Run the test and check that it fails for the expected reason: the behavior is missing, not a compile error or a typo in the test.
3. Implement the change, then rerun the relevant tests until they pass.
4. Commit a test only once it passes. Never commit a failing test.
5. Name each test and subtest after the condition and the expected behavior, in plain language, so a reviewer can read the tests as a description of the change. The name must match what the assertions check.

A change with no new behavior, such as a rename or a docs edit, needs no new test. Do not rewrite existing tests only to meet these rules.

`)
}

func plannedStatus(planning bool) string {
	if !planning {
		return ""
	}
	return `- "planned" once the four documents are written: set documents, with prNumber and prUrl null and validation and questions [].
`
}

func documentsRule(planning bool) string {
	if planning {
		return `documents is null unless the status is "planned".`
	}
	return "Set documents to null."
}

func writePlanLinks(s *strings.Builder, plan []task.PlanComment) {
	for _, c := range plan {
		fmt.Fprintf(s, "- %s: %s\n", c.Doc.Title(), c.URL)
	}
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
// open is the latest review when it requested changes, so the answer can say how to
// handle findings the implementer may not have seen. planning is set when the
// questions came from the planning turn, which still ends with the documents.
func ResumeMessage(ds []task.Decision, open *task.Review, planning bool) string {
	var s strings.Builder
	if len(ds) == 1 {
		s.WriteString("The developer answered your question.\n\n")
	} else {
		s.WriteString("The developer answered your questions.\n\n")
	}
	for _, d := range ds {
		fmt.Fprintf(&s, "Question: %s\nDecision: %s\n\n", d.Question.Issue, d.Answer)
	}
	if open != nil {
		fmt.Fprintf(&s, "Review round %d of PR #%d requested these changes. Handle them as the decisions say.\n\n", open.Round, open.PR)
		writeFindings(&s, open.Findings)
		s.WriteString("\n")
	}
	outcomes := "pr_opened, blocked, or failed"
	if planning {
		outcomes = "planned, blocked, or failed"
	}
	fmt.Fprintf(&s, "Continue the task with these decisions under the same instructions as before. End with the same JSON outcome: %s.\n", outcomes)
	return s.String()
}

// MergeMessage resumes the implementer's session when its PR conflicts with the
// latest base branch. The merge is reviewed like any other change.
func MergeMessage(branch, base string, files []string) string {
	var s strings.Builder
	mergeRules(&s, branch, base, files)
	s.WriteString("End with the same JSON outcome: pr_opened, blocked, or failed.\n")
	return s.String()
}

// mergeRules tells the implementer how to bring the base into an open PR. A rebase or
// force-push would rewrite a branch the developer and reviewers already see, and an
// aborted merge would lose the state the developer needs to finish it by hand.
func mergeRules(s *strings.Builder, branch, base string, files []string) {
	fmt.Fprintf(s, "Your pull request from %s conflicts with the latest origin/%s in these files:\n\n", branch, base)
	for _, f := range files {
		fmt.Fprintf(s, "- %s\n", f)
	}
	fmt.Fprintf(s, `
Resolve the conflicts before anything else. A reviewer checks the result.

- Run `+"`git fetch origin %s`"+`, then `+"`git merge origin/%s`"+` on %s. If a merge is already in progress (`+"`git status`"+` shows it), finish that one instead.
- Never rebase, and never force-push. The pull request is open, so its history stays as it is.
- Resolve each file so both sides' changes keep working. Run the project's checks, commit the merge, and push with `+"`git push origin %s`"+`.
- End with "pr_opened" and the pull request number.
- If you can't resolve a file safely, do not run `+"`git merge --abort`"+`. Leave the merge in progress in the worktree and end with "blocked". The question names every unresolved file, says the merge is in progress in the worktree, and offers the option "I resolved the files; verify, commit, push".
- If the developer answers that they resolved the files, check that no conflict markers remain, run the checks, commit the merge, and push.

`, base, base, branch, branch)
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
	return "\n## Pull request description\n\nThe developer's instructions for opening the pull request and writing its title and body. Follow them when you open the pull request and whenever you update its description (`gh pr edit --body`). They apply only to the pull request, not to the code.\n\n" + text + "\n"
}
