// Package render formats app results for the terminal. It does no I/O beyond the writer.
package render

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

func issueLabel(k task.Key, identifier string) string {
	if k.Source == task.Linear {
		return fmt.Sprintf("%s · %s", k.Repo, identifier)
	}
	return fmt.Sprintf("%s#%d", k.Repo, k.Number)
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

// TaskRows is one delegated task as the dashboard shows it: the identity with its agent
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

// PRStatus is the PR row's facts, each worded as the dashboard shows it, beside the
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
	return issueLabel(r.Key, r.Identifier), agentName(r.Delegation.Implementer.Agent), string(r.State)
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
	fmt.Fprintf(w, "%s %s\n%s · %s\n\n", issueLabel(r.Key, r.Identifier), r.Title, r.Repo, r.URL)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	row := func(k, v string) { fmt.Fprintf(tw, "%s\t%s\n", k, orDash(v)) }
	row("State", string(r.State))
	if d := r.Delegation; d != nil {
		row("Agent", fmt.Sprintf("%s (reviewer: %s)", agentName(d.Implementer.Agent), agentName(d.Reviewer)))
		row("Session", d.Implementer.ID)
		if p := d.Profile; p != nil {
			row("Profile", fmt.Sprintf("%s (plan %s, implement %s) · %s", p.Name, ModelText(p.PlanModel, p.PlanReasoningEffort), ModelText(p.Model, p.ReasoningEffort), orDash(d.RouteReason)))
		}
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
	if len(r.Feedback) > 0 {
		fmt.Fprintln(w, "\nPull request comments")
		for _, f := range r.Feedback {
			state := "waiting"
			if !f.HandledAt.IsZero() {
				state = "answered " + f.HandledAt.Local().Format(time.DateTime)
			}
			fmt.Fprintf(w, "  %s %s · %s · %s\n", mark(!f.HandledAt.IsZero()), orDash(f.Author), state, f.URL)
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
		fmt.Fprintln(w, "\nAnswer it: press Enter on the task in the dashboard.")
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

// ModelText is a model and its reasoning effort, if any.
func ModelText(model, effort string) string {
	return strings.TrimSpace(orDefault(model, "default") + " " + effort)
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
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
