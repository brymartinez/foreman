// Package render formats app results for the terminal. It does no I/O beyond the writer.
package render

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/task"
)

func Inbox(w io.Writer, r app.InboxReport) {
	sections := 0
	section := func(title string) {
		if sections > 0 {
			fmt.Fprintln(w)
		}
		sections++
		fmt.Fprintln(w, title)
	}
	if len(r.NeedsDecision) > 0 {
		section("NEEDS DECISION")
		for _, rec := range r.NeedsDecision {
			line(w, rec.Key, rec.Title, pendingQuestion(rec))
			for _, change := range r.WaitingChanges[rec.Key] {
				fmt.Fprintf(w, "        • %s\n", change)
			}
		}
	}
	if len(r.Reviewed) > 0 {
		section("REVIEWED")
		for _, rec := range r.Reviewed {
			reviewedTask(w, rec)
			for _, change := range r.WaitingChanges[rec.Key] {
				fmt.Fprintf(w, "        • %s\n", change)
			}
		}
	}
	if len(r.Ready) > 0 {
		section("READY FOR YOU")
		for i, rec := range r.Ready {
			if i > 0 {
				fmt.Fprintln(w)
			}
			ready(w, rec)
			for _, change := range r.WaitingChanges[rec.Key] {
				fmt.Fprintf(w, "        • %s\n", change)
			}
		}
	}
	if len(r.Changed) > 0 {
		section("CHANGED")
		for _, e := range r.Changed {
			line(w, e.Item.Key, e.Item.Title, strings.Join(e.Changes, ", "))
		}
	}
	if len(r.New) > 0 {
		section("NEW")
		for _, e := range r.New {
			line(w, e.Item.Key, e.Item.Title, kindLabel(e.Item.Kind)+strings.Join(e.Changes, ", "))
		}
	}
	if sections == 0 {
		fmt.Fprintln(w, "Nothing new.")
	}
	if r.Unchanged > 0 {
		fmt.Fprintf(w, "\nNO CHANGE\n  %d tracked %s hidden\n", r.Unchanged, plural(r.Unchanged, "item", "items"))
	}
	SearchLimits(w, r.Truncated)
	Warnings(w, r.Warnings)
}

// InboxActivity shows updates for items the delegation picker will not display.
func InboxActivity(w io.Writer, r app.InboxReport) {
	r.New, r.Current, r.Unchanged = nil, nil, 0
	var changed []app.InboxEntry
	for _, entry := range r.Changed {
		// An entry with no reasons is a note about an item that left GitHub, which no
		// picker can offer and the developer should still hear about.
		if len(entry.Item.Reasons) == 0 || !app.CanDelegate(entry) {
			changed = append(changed, entry)
		}
	}
	r.Changed = changed
	if len(r.NeedsDecision)+len(r.Ready)+len(r.Changed) > 0 {
		Inbox(w, r)
		fmt.Fprintln(w)
		return
	}
	SearchLimits(w, r.Truncated)
	Warnings(w, r.Warnings)
}

// Warnings lists lookups that failed this run; the task files keep their last state.
func Warnings(w io.Writer, warnings []error) {
	for _, warning := range warnings {
		fmt.Fprintf(w, "\n! %v (stored state kept; retry on next refresh)\n", warning)
	}
}

// SearchLimits warns about searches that returned their maximum, so the developer
// knows older matches are missing.
func SearchLimits(w io.Writer, truncated []string) {
	for _, what := range truncated {
		fmt.Fprintf(w, "\n! The search for %s hit gh's result limit; some matches are missing.\n", what)
	}
}

func line(w io.Writer, k task.Key, title, detail string) {
	fmt.Fprintf(w, "  %s  %s\n", issueLabel(k), title)
	fmt.Fprintf(w, "        %s · %s\n", k.Repo, detail)
}

func issueLabel(k task.Key) string { return fmt.Sprintf("%s#%d", k.Repo, k.Number) }

func kindLabel(k task.Kind) string {
	if k == task.PullRequest {
		return "PR · "
	}
	return ""
}

func pendingQuestion(rec *task.Record) string {
	p := rec.Pending()
	if p == nil {
		return "decision needed"
	}
	if n := rec.PendingCount(); n > 1 {
		return fmt.Sprintf("%s (+%d more)", p.Question.Issue, n-1)
	}
	return p.Question.Issue
}

// reviewedTask names the pull request whose comments are waiting, and who left them,
// so the developer can tell their own note from a bot's before resuming the task.
func reviewedTask(w io.Writer, rec *task.Record) {
	d := rec.Delegation
	waiting := rec.UnhandledFeedback()
	detail := fmt.Sprintf("PR #%d · %d %s", d.PR, len(waiting), plural(len(waiting), "comment", "comments"))
	if authors := feedbackAuthors(waiting); authors != "" {
		detail += " from " + authors
	}
	line(w, rec.Key, rec.Title, detail)
	if d.PRURL != "" {
		fmt.Fprintf(w, "        %s\n", d.PRURL)
	}
}

// feedbackAuthors lists who is waiting on an answer, each once, in the order GitHub
// returned their comments.
func feedbackAuthors(waiting []task.Feedback) string {
	var authors []string
	for _, f := range waiting {
		if f.Author != "" && !slices.Contains(authors, f.Author) {
			authors = append(authors, f.Author)
		}
	}
	return strings.Join(authors, ", ")
}

// ready lists what the gate checked, so the developer knows what is left to review.
func ready(w io.Writer, rec *task.Record) {
	d := rec.Delegation
	fmt.Fprintf(w, "  %s  %s\n", issueLabel(rec.Key), rec.Title)
	fmt.Fprintf(w, "        %s · %s → %s review\n", rec.Repo, agentName(d.Implementer.Agent), agentName(d.Reviewer))
	pr := fmt.Sprintf("PR #%d", d.PR)
	if d.PRURL != "" {
		pr += " · " + d.PRURL
	}
	fmt.Fprintf(w, "        %s\n\n", pr)
	for _, c := range rec.Validation {
		fmt.Fprintf(w, "        %s %s\n", mark(c.Passed), c.Command)
	}
	ciLine(w, rec.CI)
	latest := rec.LatestReview()
	fmt.Fprintf(w, "        %s competing review\n", mark(latest != nil && latest.Clean))
	if rec.CommitsSinceReview() {
		fmt.Fprintln(w, "        ! new commits since the review")
	}
	var answers []string
	for _, dec := range rec.Decisions {
		if dec.Answer != "" {
			answers = append(answers, dec.Answer)
		}
	}
	if len(answers) > 0 {
		fmt.Fprintf(w, "\n        Decisions:\n")
		for _, a := range answers {
			fmt.Fprintf(w, "        - %s\n", a)
		}
	}
}

// ciLine shows GitHub's check results. Failing checks are listed with their links,
// because judging them is the developer's call (D46).
func ciLine(w io.Writer, c *task.CIStatus) {
	switch {
	case c == nil:
		fmt.Fprintf(w, "        ? CI not checked\n")
	case c.State == task.CINone:
		fmt.Fprintf(w, "        ✓ CI (no checks)\n")
	case c.Green():
		fmt.Fprintf(w, "        ✓ CI\n")
	case c.State == task.CIPending:
		fmt.Fprintf(w, "        … CI running\n")
	default:
		fmt.Fprintf(w, "        ✗ CI: %s\n", checkNames(c.Failing))
		for _, f := range c.Failing {
			if f.Link != "" {
				fmt.Fprintf(w, "            %s  %s\n", f.Name, f.Link)
			}
		}
	}
}

func mark(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}

// ciBadge is the gate's last CI result; it is empty until the gate has looked.
func ciBadge(c *task.CIStatus) string {
	if c == nil || c.Green() || c.State == task.CIPending {
		return ciState(c)
	}
	return ciState(c) + " " + checkNames(c.Failing)
}

// ciState is ciBadge without the failing checks' names, for a pane too narrow for
// them. It is the same fact: whether CI passed, is running, or failed.
func ciState(c *task.CIStatus) string {
	switch {
	case c == nil:
		return ""
	case c.Green():
		return "CI ✓"
	case c.State == task.CIPending:
		return "CI pending"
	}
	return "CI ✗"
}

func checkNames(checks []task.CheckRun) string {
	names := make([]string, len(checks))
	for i, c := range checks {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}

func Status(w io.Writer, recs []*task.Record) {
	if len(recs) == 0 {
		fmt.Fprintln(w, "No delegated work. Start one with: foreman start <issue> --agent claude|codex")
		return
	}
	for i, r := range recs {
		if i > 0 {
			fmt.Fprintln(w)
		}
		rows := TaskRows(r)
		fmt.Fprintln(w, rows[0])
		for _, row := range rows[1:] {
			fmt.Fprintf(w, "       %s\n", row)
		}
	}
}

// TaskRows is one delegated task as `status` shows it: the identity with its agent
// and state, the title with its repo, and the PR with its CI and review badges. The
// dashboard's pane uses the same rows, so the two views cannot drift.
func TaskRows(r *task.Record) []string {
	d := r.Delegation
	identity, agent, state := TaskHead(r)
	rows := []string{
		fmt.Sprintf("%-25s %-7s %s", identity, agent, state),
		fmt.Sprintf("%s · %s", r.Title, r.Repo),
	}
	if d.PR > 0 {
		s := TaskPR(r)
		rows = append(rows, JoinFacts(s.PR, s.Reviewer, s.CI, s.Review))
	}
	return rows
}

// PRStatus is the PR row's facts, each worded as `status` prints it, beside the
// shorter form of the two that can grow without a bound. The dashboard drops and
// shortens them to fit its pane, so neither view invents wording of its own.
type PRStatus struct {
	PR       string // PR #912
	Reviewer string // reviewer: Codex
	// CI is empty until the gate has looked at the checks. CIState is the same
	// result without the failing checks' names.
	CI      string // CI ✗ test, lint
	CIState string // CI ✗
	// Review is empty until the first review round. ReviewState is the same result
	// without the round number.
	Review      string // review round 2: 3 findings
	ReviewState string // review ✗ 3
}

// TaskPR is the facts TaskRows joins into a task's PR row.
func TaskPR(r *task.Record) PRStatus {
	d := r.Delegation
	return PRStatus{
		PR:          fmt.Sprintf("PR #%d", d.PR),
		Reviewer:    "reviewer: " + agentName(d.Reviewer),
		CI:          ciBadge(r.CI),
		CIState:     ciState(r.CI),
		Review:      reviewBadge(r),
		ReviewState: reviewState(r),
	}
}

// JoinFacts joins the facts that are there with the separator every foreman row uses.
func JoinFacts(facts ...string) string {
	kept := make([]string, 0, len(facts))
	for _, f := range facts {
		if f != "" {
			kept = append(kept, f)
		}
	}
	return strings.Join(kept, " · ")
}

// TaskHead is the three fields TaskRows pads into its first row. The dashboard's
// pane takes them apart, because the padding is wider than a narrow pane and the
// state is what a truncation would cut.
func TaskHead(r *task.Record) (identity, agent, state string) {
	return issueLabel(r.Key), agentName(r.Delegation.Implementer.Agent), string(r.State)
}

func reviewBadge(r *task.Record) string {
	latest := r.LatestReview()
	switch {
	case latest == nil:
		return ""
	case latest.Clean:
		return "review ✓"
	}
	return fmt.Sprintf("review round %d: %d %s", latest.Round, len(latest.Findings), plural(len(latest.Findings), "finding", "findings"))
}

// reviewState is reviewBadge without the round and the word findings, for a pane too
// narrow for them. It is the same fact: whether the last review was clean, and how
// many findings it left.
func reviewState(r *task.Record) string {
	latest := r.LatestReview()
	switch {
	case latest == nil:
		return ""
	case latest.Clean:
		return "review ✓"
	}
	return fmt.Sprintf("review ✗ %d", len(latest.Findings))
}

func Show(w io.Writer, r *task.Record) {
	fmt.Fprintf(w, "%s %s\n%s · %s\n\n", issueLabel(r.Key), r.Title, r.Repo, r.URL)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	row := func(k, v string) { fmt.Fprintf(tw, "%s\t%s\n", k, orDash(v)) }
	row("State", string(r.State))
	if d := r.Delegation; d != nil {
		row("Agent", fmt.Sprintf("%s (reviewer: %s)", agentName(d.Implementer.Agent), agentName(d.Reviewer)))
		row("Session", d.Implementer.ID)
		row("Context file", d.ContextFile)
		row("Branch", fmt.Sprintf("%s (from %s)", d.Branch, orDash(d.BaseRef)))
		if d.WorktreeRemovedAt.IsZero() {
			row("Worktree", d.Worktree)
		} else {
			row("Worktree", fmt.Sprintf("%s (removed %s)", d.Worktree, d.WorktreeRemovedAt.Local().Format(time.DateTime)))
		}
		row("Runner log", d.RunnerLog)
		if d.Implementer.Agent == task.Claude && d.Implementer.ID != "" {
			row("Transcript", agent.ClaudeTranscript(d.Worktree, d.Implementer.ID))
		}
		switch {
		case d.PR > 0 && d.PRURL != "":
			row("PR", fmt.Sprintf("#%d · %s", d.PR, d.PRURL))
		case d.PR > 0:
			row("PR", fmt.Sprintf("#%d", d.PR))
		default:
			row("PR", "")
		}
	} else {
		row("Agent", "not delegated")
	}
	for i, c := range r.Plan {
		label := ""
		if i == 0 {
			label = "Plan"
		}
		row(label, c.Doc.Title()+" "+c.URL)
	}
	if g := r.GitHub; g != nil {
		row("GitHub", fmt.Sprintf("%s · updated %s · %s", g.State, g.UpdatedAt.Local().Format(time.DateTime), reasons(g.Reasons)))
	}
	if !r.LastGitHubSync.IsZero() {
		row("Last synced", r.LastGitHubSync.Local().Format(time.DateTime))
	}
	if c := r.CI; c != nil {
		row("CI", fmt.Sprintf("%s · checked %s", c.State, c.CheckedAt.Local().Format(time.DateTime)))
	}
	if o := r.LastOutcome; o != nil {
		row("Last outcome", fmt.Sprintf("%s · %s · %s", o.Status, o.At.Local().Format(time.DateTime), o.Summary))
	}
	if r.Usage.ClaudeTurns > 0 || r.Usage.CodexTurns > 0 {
		row("Usage", fmt.Sprintf("Claude $%.2f (%d %s) · Codex %d tokens (%d %s)", r.Usage.ClaudeCostUSD, r.Usage.ClaudeTurns, plural(r.Usage.ClaudeTurns, "turn", "turns"), r.Usage.CodexInputTokens+r.Usage.CodexOutputTokens+r.Usage.CodexReasoningTokens, r.Usage.CodexTurns, plural(r.Usage.CodexTurns, "turn", "turns")))
	}
	tw.Flush()
	if r.CI != nil && len(r.CI.Failing) > 0 {
		fmt.Fprintln(w, "\nFailing checks")
		for _, c := range r.CI.Failing {
			fmt.Fprintf(w, "  ✗ %s %s\n", c.Name, c.Link)
		}
	}
	if len(r.Validation) > 0 {
		fmt.Fprintln(w, "\nValidation (reported by the agent)")
		for _, c := range r.Validation {
			fmt.Fprintf(w, "  %s %s\n", mark(c.Passed), c.Command)
		}
	}
	if latest := r.LatestReview(); latest != nil {
		verdict := "changes requested"
		if latest.Clean {
			verdict = "clean"
		}
		fmt.Fprintf(w, "\nLatest review (round %d of %d, %s, %s): %s\n", latest.Round, len(r.Reviews), agentName(latest.Reviewer), latest.At.Local().Format(time.DateTime), verdict)
		if s := strings.TrimSpace(latest.Summary); s != "" {
			fmt.Fprintf(w, "  %s\n", s)
		}
		for _, f := range latest.Findings {
			loc := ""
			if f.File != "" {
				loc = " " + f.File
				if f.Line > 0 {
					loc += fmt.Sprintf(":%d", f.Line)
				}
			}
			fmt.Fprintf(w, "  - [%s]%s %s\n", f.Severity, loc, f.Summary)
		}
	}
	var answered []task.Decision
	for _, d := range r.Decisions {
		if d.Answer != "" {
			answered = append(answered, d)
		}
	}
	if len(answered) > 0 {
		fmt.Fprintln(w, "\nDecisions")
		for _, d := range answered {
			fmt.Fprintf(w, "  Q: %s\n  A: %s\n", d.Question.Issue, d.Answer)
		}
	}
	if p := r.Pending(); p != nil && r.State == task.NeedsDecision {
		fmt.Fprintln(w)
		if n := r.PendingCount(); n > 1 {
			fmt.Fprintf(w, "%d questions are waiting; this is the first.\n\n", n)
		}
		Blocked(w, p.Question)
		fmt.Fprintf(w, "\nAnswer with: foreman respond %s#%d\n", r.Repo, r.Number)
	}
}

// Blocked prints a question in the product's fixed escalation format.
func Blocked(w io.Writer, q task.Question) {
	fmt.Fprintf(w, "BLOCKED: Need decision\n\nIssue:\n%s\n\nContext:\n%s\n\nOptions:\n", strings.TrimSpace(q.Issue), strings.TrimSpace(q.Context))
	for _, o := range q.Options {
		fmt.Fprintf(w, "%s. %s\n", strings.TrimSpace(o.Label), strings.TrimSpace(o.Description))
	}
	fmt.Fprintf(w, "\nRecommendation:\n%s\n\nImpact if wrong:\n%s\n", strings.TrimSpace(q.Recommendation), strings.TrimSpace(q.ImpactIfWrong))
}

func Started(w io.Writer, r *task.Record) {
	d := r.Delegation
	if r.State == task.Failed {
		fmt.Fprintf(w, "%s could not start: %s\n", issueLabel(r.Key), r.LastOutcome.Summary)
		return
	}
	fmt.Fprintf(w, "Started %s %s with %s\n", issueLabel(r.Key), r.Title, agentName(d.Implementer.Agent))
	fmt.Fprintf(w, "  branch    %s\n  worktree  %s\n  log       %s\n", d.Branch, d.Worktree, d.RunnerLog)
	fmt.Fprintf(w, "Check progress with: foreman status\n")
}

// Cancelled reports `foreman cancel`. A run that finished while it was being
// stopped keeps its own state.
func Cancelled(w io.Writer, r *task.Record) {
	if r.State != task.Failed || r.LastOutcome == nil || r.LastOutcome.Status != app.Cancelled {
		fmt.Fprintf(w, "%s finished before it could be cancelled: %s\n", issueLabel(r.Key), r.State)
		return
	}
	fmt.Fprintf(w, "Cancelled %s: %s\n", issueLabel(r.Key), r.LastOutcome.Summary)
	fmt.Fprintf(w, "The branch and worktree are kept. To resume, run from a checkout of %s:\n  foreman start %d --agent %s\n", r.Repo, r.Number, r.Delegation.Implementer.Agent)
}

// Cleaned reports `foreman clean`. A kept worktree is named with git's reason.
func Cleaned(w io.Writer, results []app.CleanResult, dryRun bool) {
	if len(results) == 0 {
		fmt.Fprintln(w, "No worktrees to clean.")
		return
	}
	removed, branchNote := "removed", "branch deleted (PR merged)"
	if dryRun {
		removed, branchNote = "would remove", "would delete branch (PR merged)"
	}
	for _, r := range results {
		d := r.Rec.Delegation
		switch {
		case r.Err != nil:
			fmt.Fprintf(w, "  %s  kept %s\n        %v\n", issueLabel(r.Rec.Key), d.Worktree, r.Err)
		case r.BranchDeleted:
			fmt.Fprintf(w, "  %s  %s %s\n        %s %s\n", issueLabel(r.Rec.Key), removed, d.Worktree, branchNote, d.Branch)
		default:
			fmt.Fprintf(w, "  %s  %s %s\n        branch %s kept\n", issueLabel(r.Rec.Key), removed, d.Worktree, d.Branch)
		}
	}
}

// StageReport is one stage as `foreman config` resolved it. Where is where each layer
// is looked up, for "not configured" lines; Err is a read failure.
type StageReport struct {
	Instructions task.StageInstructions
	GlobalWhere  string
	RepoWhere    string // "" outside a repository
	Err          error
}

// StageSources prints where each stage's instructions come from. home shortens paths
// to ~ and repoRoot makes repo paths relative.
func StageSources(w io.Writer, reports []StageReport, home, repoRoot string) {
	missing := false
	for i, r := range reports {
		if i > 0 {
			fmt.Fprintln(w)
		}
		stageHeader(w, r, home, repoRoot)
		missing = missing || r.GlobalMissing()
	}
	fmt.Fprintln(w, "\nTasks read .foreman/ from their worktree: the version committed on the base branch.")
	if missing {
		fmt.Fprintln(w, missingGlobalHint)
	}
}

const missingGlobalHint = "Foreman won't run until every global stage file exists. Run `make config` in the foreman repository, or copy its defaults/stages/*.md into the stages directory."

// GlobalMissing reports a stage whose required global file isn't there (D73).
func (r StageReport) GlobalMissing() bool {
	return r.Err == nil && r.Instructions.GlobalPath == ""
}

// Config prints the resolved, non-secret configuration used by foreman.
func Config(w io.Writer, c config.Config) {
	fmt.Fprintf(w, "Config file: %s\n", c.Path)
	fmt.Fprintf(w, "Tasks directory: %s\n", c.TasksDir)
	fmt.Fprintf(w, "Worktrees directory: %s\n", c.WorktreesDir)
	fmt.Fprintf(w, "Repositories directory: %s\n", c.RepositoriesDir)
	fmt.Fprintf(w, "Log directory: %s\n", c.LogDir)
	fmt.Fprintf(w, "Stages directory: %s\n", c.StagesDir)
	fmt.Fprintf(w, "Review rounds: %d passes per review\n", orThree(c.ReviewRounds))
	fmt.Fprintf(w, "Fix rounds: %d before asking you\n", orThree(c.FixRounds))
	fmt.Fprintf(w, "Implementer budget: $%.2f per Claude turn (Codex has no cap)\n", c.Budgets.ImplementerUSD)
	fmt.Fprintf(w, "Reviewer budget: $%.2f per Claude turn (Codex has no cap)\n", c.Budgets.ReviewerUSD)
	fmt.Fprintf(w, "Notifications: %t\n", c.Notifications.Enabled)
	fmt.Fprintf(w, "Mentions: %t\n", c.Mentions)
	fmt.Fprintf(w, "GitHub refresh: every %d seconds\n", c.Refresh)
	fmt.Fprintf(w, "Ignore repositories: %s\n", repoList(c.IgnoredRepos))
	fmt.Fprintf(w, "Claude command: %s\n", c.Claude.Command)
	fmt.Fprintf(w, "Claude model: %s\n", orDefault(c.Claude.Model, "CLI default"))
	fmt.Fprintf(w, "Claude review model: %s\n", orDefault(c.Claude.ReviewModel, "same as model"))
	fmt.Fprintf(w, "Codex command: %s\n", c.Codex.Command)
	fmt.Fprintf(w, "Codex model: %s\n", orDefault(c.Codex.Model, "CLI default"))
	fmt.Fprintf(w, "Codex review model: %s\n", orDefault(c.Codex.ReviewModel, "same as model"))
	fmt.Fprintf(w, "Cleanup on done: %t\n", c.Cleanup.OnDone)
	fmt.Fprintf(w, "Delete merged branch: %t\n", c.Cleanup.DeleteMergedBranch)
}

func orThree(rounds int) int {
	if rounds == 0 {
		return 3
	}
	return rounds
}

func repoList(repos []task.Repo) string {
	if len(repos) == 0 {
		return "none"
	}
	values := make([]string, len(repos))
	for i, repo := range repos {
		values[i] = string(repo)
	}
	return strings.Join(values, ", ")
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// StageContent prints one stage's sources and the combined text agents receive.
func StageContent(w io.Writer, r StageReport, home, repoRoot string) {
	stageHeader(w, r, home, repoRoot)
	if r.Err != nil {
		return
	}
	if r.GlobalMissing() {
		fmt.Fprintf(w, "\n%s\n", missingGlobalHint)
		return
	}
	fmt.Fprintf(w, "\n%s\n", r.Instructions.Content())
}

func stageHeader(w io.Writer, r StageReport, home, repoRoot string) {
	fmt.Fprintln(w, strings.ToUpper(string(r.Instructions.Stage)))
	if r.Err != nil {
		fmt.Fprintf(w, "  error:  %v\n", r.Err)
		return
	}
	if r.GlobalMissing() {
		fmt.Fprintf(w, "  global: MISSING, required (%s)\n", displayPath(r.GlobalWhere, home, ""))
	} else {
		fmt.Fprintf(w, "  global: %s\n", displayPath(r.Instructions.GlobalPath, home, ""))
	}
	if r.RepoWhere == "" {
		fmt.Fprintf(w, "  repo:   not in a git repository\n")
		return
	}
	fmt.Fprintf(w, "  repo:   %s\n", source(r.Instructions.RepoPath, r.RepoWhere, home, repoRoot))
}

func source(found, where, home, base string) string {
	if found != "" {
		return displayPath(found, home, base)
	}
	return "not configured (" + displayPath(where, home, base) + ")"
}

func displayPath(p, home, base string) string {
	if base != "" {
		if rel, err := filepath.Rel(base, p); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	if home != "" {
		if rest, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
			return "~/" + rest
		}
	}
	return p
}

func agentName(a task.Agent) string {
	switch a {
	case task.Claude:
		return "Claude"
	case task.Codex:
		return "Codex"
	}
	return string(a)
}

func reasons(rs []task.InboxReason) string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = strings.ReplaceAll(string(r), "_", " ")
	}
	return orDash(strings.Join(out, ", "))
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
