package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

// ReplyMarker marks comments the agent posts on a pull request, including screenshot
// evidence. The agent uses the developer's gh login, so the author cannot distinguish
// these from developer feedback. Markdown hides the marker, and foreman ignores marked
// comments when looking for feedback or human comments for learning.
const ReplyMarker = "<!-- foreman:reply -->"

type TaskBrief struct {
	Key        task.Key
	Identifier string
	Title      string
	URL        string
	IssueBody  string
	// ConfirmedScope contains the ordered triage decisions and source URLs.
	ConfirmedScope string
	Context        string // contents of the --context Markdown file, if any
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
	// ExistingPlans are planning documents a planning turn finds already on the issue,
	// by document, without their marker and heading.
	ExistingPlans map[task.PlanDoc]string
	// Plan is the planning documents already posted on the issue.
	Plan []task.PlanComment
	// PriorPR is the pull request of a run the developer reset, or nil.
	PriorPR *PriorPR
	// A checkpoint starts a new implementation session from committed work.
	CheckpointHead string
	NextObjective  string
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
	// Knowledge is the repository's learnings selected for this issue, one Markdown
	// bullet each. Learned from earlier tasks, so it ranks below every other input.
	Knowledge string
	// LocalReview is set for a task reviewed before its PR opens and with no PR yet.
	// The turn commits and proposes the PR; foreman has it pushed after a clean review.
	LocalReview bool
	// Claims is set for a task with claim verification. A turn that finishes a change
	// returns its claims for the reviewers.
	Claims bool
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
	if b.Key.Source == task.Linear {
		fmt.Fprintf(&s, "You are the implementation agent for Linear issue %s in %s. You are working unattended; nobody will answer questions during this turn.\n\n", b.Identifier, b.Key.Repo)
	} else {
		fmt.Fprintf(&s, "You are the implementation agent for GitHub issue %s#%d. You are working unattended; nobody will answer questions during this turn.\n\n", b.Key.Repo, b.Key.Number)
	}

	fmt.Fprintf(&s, "## Issue\n\n%s\n%s\n\n", b.Title, b.URL)
	if b.NextObjective == "" {
		body := strings.TrimSpace(b.IssueBody)
		if body == "" {
			body = "(The issue has no description.)"
		}
		s.WriteString(body + "\n\n")
	}
	if scope := strings.TrimSpace(b.ConfirmedScope); scope != "" {
		s.WriteString("## Confirmed triage scope\n\nThis is the maintainer-confirmed scope. Planning documents, implementation, and PR description must agree with it. A later correction overrides an earlier decision on the same point.\n\n")
		s.WriteString(scope + "\n\n")
	}

	silentWorkSection(&s)
	stageSection(&s, b.StageInstructions)
	if !b.Planning && b.Conflict == nil && b.Merge == nil {
		testFirstSection(&s)
	}
	if b.Planning {
		planningSection(&s, b.PlanInstructions)
		existingPlansSection(&s, b.ExistingPlans)
	} else if len(b.Plan) > 0 {
		s.WriteString("## The plan\n\nYour plan for this issue is posted on it:\n\n")
		writePlanLinks(&s, b.Plan)
		s.WriteString("\n")
	}
	if b.NextObjective != "" {
		fmt.Fprintf(&s, "## Continue from checkpoint\n\nThe branch is clean at commit %s. This is a new session; inspect the committed code and issue only as needed. Your next objective is: %s\n\nDo not reconstruct or request the earlier session transcript.\n\n", b.CheckpointHead, b.NextObjective)
	}

	if b.Conflict == nil && b.Merge == nil {
		knowledgeSection(&s, b.Knowledge)
	}

	if ctx := strings.TrimSpace(b.Context); ctx != "" {
		s.WriteString("## Implementation context\n\nThis context comes from the developer and is authoritative. Where it conflicts with the issue text, follow it.\n\n")
		s.WriteString(ctx + "\n\n")
	}

	if b.PriorPR != nil {
		priorPRSection(&s, *b.PriorPR)
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
		mergeRules(&s, b.Branch, b.Merge.Base, b.Merge.Files, b.LocalReview)
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
	if !b.Planning && b.PR == 0 && b.Conflict == nil && b.Merge == nil {
		finish := `open the pull request and report "pr_opened" instead. A small change may go directly to "pr_opened"`
		if b.LocalReview {
			finish = `report "review_ready" instead after validating the committed change. The accepted checkpoint commit may be the final commit; do not make an empty commit just to report "review_ready". A small change may go directly to "review_ready"`
		}
		fmt.Fprintf(&s, `## Implementation checkpoints

For a multi-part change, complete one coherent, testable part at a time. When that part is committed and more work remains, end this session with status "checkpoint". Ensure the worktree is clean, report the validation you ran, and set nextObjective to one short, concrete objective for a fresh session. Do not push or open a pull request at a checkpoint. When the whole change is ready, %s.

The next session receives the commit and nextObjective, not this transcript. Keep nextObjective under 500 characters; the committed code, issue, and posted plan hold the details.

`, finish)
	}
	if b.Claims && !b.Planning {
		claimsSection(&s)
	}
	if b.Key.Source == task.Linear {
		s.WriteString("GitHub hosts the pull request. Put " + b.Identifier + " in the pull request title and " + b.URL + " in the pull request body. Do not add a GitHub Closes #N line. These issue link rules override conflicting developer PR instructions below.\n\n")
	}

	fmt.Fprintf(&s, `## Workspace

You are in a dedicated git worktree at %s on branch %s, created from origin/%s.
Only change files inside this worktree. Commit your work to %s. Do not switch branches.

%s%s
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
%s%s%s- "blocked" when you need a decision, as described above.
- "failed" when you cannot complete the task: say why in summary.
Set nextObjective to null except for "checkpoint". Set fields that do not apply to null, or [] for validation and questions. %s
`, b.Worktree, b.Branch, b.BaseBranch, b.Branch, openingSection(b), prSection(b.PRInstructions), plannedStatus(b.Planning), finishedStatus(b), checkpointStatus(b), documentsRule(b.Planning))
	return s.String()
}

// openingSection says how the finished change leaves the worktree: pushed with a PR,
// or, for a task reviewed before its PR, committed with a proposed PR and not pushed.
func openingSection(b TaskBrief) string {
	if b.LocalReview {
		return fmt.Sprintf(`## Proposing the pull request

Do not push the branch or open a pull request. Another agent reviews your local branch first, and foreman asks you to push and open the pull request once that review is clean.

- When the change is review-ready: commit everything (the worktree must be clean) and run the validation.
- Write the pull request you propose in prTitle and prBody. The description is the reviewer's only statement of what the change must do, so state the requirements it meets. %s
- Defer requested screenshots until the PR-opening turn, which posts them in a PR comment. Do not put screenshot placeholders in prBody or leave untracked images before review_ready.
`, prIssueRule(b))
	}
	return fmt.Sprintf(`## Opening the pull request

- When the change is review-ready: commit everything (the worktree must be clean), push with `+"`git push -u origin %s`"+`, then open a pull request with `+"`gh pr create --base %s --head %s`"+`. If a pull request for this branch already exists (`+"`gh pr view %s`"+`), push to it instead of opening another. %s
- Take prNumber and prUrl from `+"`gh pr view --json number,url`"+` after pushing; do not guess them.
%s`, b.Branch, b.BaseBranch, b.Branch, b.Branch, prIssueRule(b), screenshotCommentInstructions(b.PR == 0))
}

// Screenshot uploads belong in a PR comment so they cannot change the description
// that the reviewers judge. The marker keeps the agent's comment out of feedback.
func screenshotCommentInstructions(firstPR bool) string {
	if !firstPR {
		return ""
	}
	return fmt.Sprintf("- If the issue or repository asks for screenshots, capture them after the PR exists. Use the repository's documented local setup and seed, fixture, or demo data to show a representative state; do not invent user data or use production records. If you already captured screenshots for this change, use those. Attach available screenshots to one PR comment with `gh pr comment <number> --body '%s Screenshots' --attach '<path>#alt text'` (repeat --attach for more images). Before posting, read `gh pr view <number> --json comments` and skip the upload if a comment already contains `%s Screenshots`. Do not change the reviewed commit when capturing screenshots, and clean up only temporary files you created. Do not add screenshots to, or edit, the PR description. If there is no suitable sample data, or a requested screenshot cannot be captured or uploaded after the PR exists, still report pr_opened and name the missing evidence in the outcome summary so foreman records the PR.\n", ReplyMarker, ReplyMarker)
}

func finishedStatus(b TaskBrief) string {
	if b.LocalReview {
		return "- \"review_ready\" once the change is committed and validated, and not pushed: set prTitle, prBody, and validation (each command you ran and whether it passed). Every check must pass. Set prNumber and prUrl to null.\n"
	}
	return "- \"pr_opened\" once the pull request exists: set prNumber, prUrl, and validation (each command you ran and whether it passed).\n"
}

func prIssueRule(b TaskBrief) string {
	if b.Key.Source == task.Linear {
		return "The pull request title must include " + b.Identifier + ", and its body must link " + b.URL + ". Do not add a GitHub Closes #N line."
	}
	return fmt.Sprintf("The pull request body must include \"Closes #%d\"%s.", b.Key.Number, prBodyRule(b.PRInstructions))
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

// existingPlansSection gives a planning turn the documents foreman found on the issue.
// An empty document keeps its comment untouched, so a valid plan is not reposted.
func existingPlansSection(s *strings.Builder, existing map[task.PlanDoc]string) {
	if len(existing) == 0 {
		return
	}
	s.WriteString("## Documents already on the issue\n\nThese planning documents are already posted on the issue. Keep a document that is still valid by returning an empty string for it, and foreman leaves its comment as it is. Return the full revised text only for a document that needs revision; foreman edits its comment in place.")
	var missing []string
	for _, doc := range task.PlanDocs {
		if _, ok := existing[doc]; !ok {
			missing = append(missing, string(doc))
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(s, " Write every missing document: %s.", strings.Join(missing, ", "))
	}
	s.WriteString("\n\n")
	for _, doc := range task.PlanDocs {
		if text, ok := existing[doc]; ok {
			fmt.Fprintf(s, "### %s\n\n%s\n\n", doc.Title(), strings.TrimSpace(text))
		}
	}
}

// ExistingPlansMessage adds the documents on the issue to a resumed planning turn.
func ExistingPlansMessage(existing map[task.PlanDoc]string) string {
	var s strings.Builder
	if len(existing) > 0 {
		s.WriteString("\n")
	}
	existingPlansSection(&s, existing)
	return s.String()
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

// silentWorkSection stops progress narration between tool calls. Every sentence the
// agent writes is billed as output tokens, nobody reads the session live, and the log
// already records each tool call. Start and resume both send this prompt, and the
// reviewer gets it too, so one section covers every turn.
func silentWorkSection(s *strings.Builder) {
	s.WriteString(`## Working silently

Nobody reads this session while it runs, and every word you write costs output tokens. This overrides any default instruction to send preamble or progress messages. Do not send a preamble before tool calls, and do not write progress updates, plans for your next step, or summaries of what a tool call did. Go straight to the next tool call. Do not restate the contents of files or command output.

This applies only to text between tool calls. Keep full detail in the final response, the questions you ask, the planning documents, the pull request title and body, review findings, and any text a tool or command needs.

`)
}

// knowledgeSection passes on what earlier tasks in the repository taught. An
// evaluator inferred it, so it must never override a source the developer controls.
func knowledgeSection(s *strings.Builder, text string) {
	if text = strings.TrimSpace(text); text == "" {
		return
	}
	s.WriteString(KnowledgeIntro)
	s.WriteString(text + "\n\n")
}

// KnowledgeIntro opens the repository knowledge section. The section's size limit
// covers it, so the caller leaves room for it.
const KnowledgeIntro = "## Repository knowledge\n\nForeman learned these from earlier tasks in this repository. They may be wrong or out of date. The repository's own instructions, the issue, the developer's decisions and context, and facts you verify take precedence. Where one of them disagrees, follow it and ignore the learning.\n\n"

func plannedStatus(planning bool) string {
	if !planning {
		return ""
	}
	return `- "planned" once the four documents are written: set documents, with prNumber and prUrl null and validation and questions [].
`
}

func checkpointStatus(b TaskBrief) string {
	if b.Planning || b.PR != 0 || b.Conflict != nil || b.Merge != nil {
		return ""
	}
	return "- \"checkpoint\" after committing a tested part when more implementation remains: set nextObjective and validation. Set prNumber and prUrl to null.\n"
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
// local is set for a task reviewed before its PR, which ends with review_ready.
func ResumeMessage(ds []task.Decision, open *task.Review, planning, local bool) string {
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
		if local {
			fmt.Fprintf(&s, "Review round %d of your local change requested these changes. Handle them as the decisions say.\n\n", open.Round)
		} else {
			fmt.Fprintf(&s, "Review round %d of PR #%d requested these changes. Handle them as the decisions say.\n\n", open.Round, open.PR)
		}
		writeFindings(&s, open.Findings)
		s.WriteString("\n")
	}
	outcomes := "pr_opened, blocked, or failed"
	switch {
	case planning:
		outcomes = "planned, blocked, or failed"
	case local:
		s.WriteString(localFinishRule)
		outcomes = "review_ready, blocked, or failed"
	}
	fmt.Fprintf(&s, "Continue the task with these decisions under the same instructions as before. End with the same JSON outcome: %s.\n", outcomes)
	return s.String()
}

// MergeMessage resumes the implementer's session when its PR conflicts with the
// latest base branch. The merge is reviewed like any other change.
func MergeMessage(branch, base string, files []string) string {
	var s strings.Builder
	mergeRules(&s, branch, base, files, false)
	s.WriteString("End with the same JSON outcome: pr_opened, blocked, or failed.\n")
	return s.String()
}

// LocalMergeMessage resumes the implementer of a task reviewed before its PR when its
// local branch conflicts with the latest base. The merge stays local until review.
func LocalMergeMessage(branch, base string, files []string) string {
	var s strings.Builder
	mergeRules(&s, branch, base, files, true)
	s.WriteString("End with the same JSON outcome: review_ready, blocked, or failed.\n")
	return s.String()
}

// localFinishRule ends every turn of a task reviewed before its PR: the change stays
// local and comes back with the description the reviewer reads.
const localFinishRule = "Commit the change locally. Do not push the branch or open a pull request. Return the proposed pull request in prTitle and prBody, updated so it describes the change as it now is.\n\n"

// mergeRules tells the implementer how to bring the base into an open PR. A rebase or
// force-push would rewrite a branch the developer and reviewers already see, and an
// aborted merge would lose the state the developer needs to finish it by hand.
func mergeRules(s *strings.Builder, branch, base string, files []string, local bool) {
	if local {
		localMergeRules(s, branch, base, files)
		return
	}
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
	return "\n## Pull request description\n\nRead the repository's AGENTS.md and pull request template before opening or updating the pull request. Follow their title and body rules where they differ from the developer's instructions below. These instructions apply only to the pull request, not to the code.\n\n" + text + "\n"
}

// PriorPR is the pull request a reset run left on GitHub, read again when the next
// run starts. Continue means it is open and the new run pushes to its branch.
type PriorPR struct {
	Number   int
	URL      string
	Title    string
	State    string
	Body     string
	Comments []FeedbackItem
	Continue bool
}

// priorPRSection gives a fresh run the pull request of the run the developer reset:
// what it tried and what reviewers said about it.
func priorPRSection(s *strings.Builder, pr PriorPR) {
	fmt.Fprintf(s, "## Prior pull request\n\nThe developer discarded an earlier run on this issue and started over. This is a new session. Pull request #%d (%s) came from that run and is %s.\n\n", pr.Number, pr.URL, pr.State)
	if pr.Continue {
		fmt.Fprintf(s, "Your branch starts at that pull request's head. Continue updating pull request #%d; do not open another. Use its description and comments to decide what to keep and what to change.\n\n", pr.Number)
	} else {
		s.WriteString("Use it as context only. Do not reopen it or push to its branch; your work goes on your own branch and pull request.\n\n")
	}
	fmt.Fprintf(s, "### Title\n\n%s\n\n### Description\n\n", pr.Title)
	body := strings.TrimSpace(pr.Body)
	if body == "" {
		body = "(no description)"
	}
	s.WriteString(quote(body) + "\n\n")
	if len(pr.Comments) == 0 {
		return
	}
	s.WriteString("### Comments\n\n")
	for i, c := range pr.Comments {
		fmt.Fprintf(s, "%d. %s, %s:\n\n%s\n\n", i+1, author(c), where(c), quote(c.Body))
	}
}

// localMergeRules is mergeRules for a branch that is not pushed yet. The reviewer reads
// the merged branch, so the merge is committed locally and pushed after review.
func localMergeRules(s *strings.Builder, branch, base string, files []string) {
	fmt.Fprintf(s, "Your local branch %s conflicts with the latest origin/%s in these files:\n\n", branch, base)
	for _, f := range files {
		fmt.Fprintf(s, "- %s\n", f)
	}
	fmt.Fprintf(s, `
Resolve the conflicts before anything else. A reviewer checks the result.

- Run `+"`git fetch origin %s`"+`, then `+"`git merge origin/%s`"+` on %s. If a merge is already in progress (`+"`git status`"+` shows it), finish that one instead.
- Never rebase. Do not push the branch or open a pull request.
- Resolve each file so both sides' changes keep working. Run the project's checks and commit the merge.
- End with "review_ready", the validation, and the proposed pull request in prTitle and prBody.
- If you can't resolve a file safely, do not run `+"`git merge --abort`"+`. Leave the merge in progress in the worktree and end with "blocked". The question names every unresolved file, says the merge is in progress in the worktree, and offers the option "I resolved the files; verify and commit".

`, base, base, branch)
}

// OpenPRBrief is the reviewed change a task reviewed before its PR now pushes: the
// commit that passed review and the PR title and description reviewed with it.
type OpenPRBrief struct {
	Branch     string
	BaseBranch string
	Head       string
	Title      string
	IssueURL   string
	// BodyFile holds the reviewed description, written by foreman so it reaches gh
	// byte for byte.
	BodyFile   string
	Validation []task.Check
	// Decisions are answers the implementer has not received yet.
	Decisions []task.Decision
	// Claims are the reviewed claims of a task with claim verification, which the
	// turn returns unchanged.
	Claims []task.Claim
}

// OpenPRPrompt has the implementer push the reviewed commit and open the PR. It is
// complete on its own, so it serves a resumed session and a fresh one alike. Foreman
// checks the PR's head, title, and body against the review afterwards.
func OpenPRPrompt(b OpenPRBrief) string {
	var s strings.Builder
	s.WriteString("Another agent reviewed your local change and found nothing to fix. Push it and open the pull request now.\n\n")
	if b.IssueURL != "" {
		fmt.Fprintf(&s, "The issue is %s. Check it and repository instructions for requested screenshots, and use any screenshots already captured for this change.\n\n", b.IssueURL)
	}
	if len(b.Decisions) > 0 {
		s.WriteString("The developer answered your questions:\n\n")
		for _, d := range b.Decisions {
			fmt.Fprintf(&s, "Question: %s\nDecision: %s\n\n", d.Question.Issue, d.Answer)
		}
	}
	fmt.Fprintf(&s, `Push exactly the reviewed commit, with exactly the reviewed title and description. Do not change the reviewed code or description, and do not commit, rebase, or amend.

1. Check that `+"`git rev-parse HEAD`"+` is %s and `+"`git status`"+` is clean. If not, stop and end with "failed".
2. Push with `+"`git push -u origin %s`"+`.
3. If `+"`gh pr view %s`"+` shows an open pull request, do not open another. Otherwise run `+"`gh pr create --base %s --head %s --title <title> --body-file %s`"+` with this title, quoted for the shell:

%s

4. Take prNumber and prUrl from `+"`gh pr view --json number,url`"+`; do not guess them.

%s
End with JSON matching the provided schema: "pr_opened" with prNumber, prUrl, and the validation below; "blocked" with questions if the push or PR creation fails in a way you cannot fix without changing the branch; or "failed". Set fields that do not apply to null, or [] for questions. Set documents to null.

Validation run on the reviewed commit:
`, b.Head, b.Branch, b.Branch, b.BaseBranch, b.Branch, b.BodyFile, b.Title, screenshotCommentInstructions(true))
	for _, c := range b.Validation {
		fmt.Fprintf(&s, "- %s: passed %t\n", c.Command, c.Passed)
	}
	if len(b.Claims) > 0 {
		claims, _ := json.Marshal(b.Claims)
		fmt.Fprintf(&s, "\nWith \"pr_opened\", return these reviewed claims unchanged in \"claims\". Keep them out of the pull request and its comments.\n\n%s\n", claims)
	}
	return s.String()
}
