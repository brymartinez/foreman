// Package render formats app results for the terminal. It does no I/O beyond the writer.
package render

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/brymartinez/foreman/internal/app"
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
		}
	}
	if len(r.Ready) > 0 {
		section("READY FOR YOU")
		for _, rec := range r.Ready {
			line(w, rec.Key, rec.Title, prSummary(rec))
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
}

func line(w io.Writer, k task.Key, title, detail string) {
	fmt.Fprintf(w, "  #%d  %s\n", k.Number, title)
	fmt.Fprintf(w, "        %s · %s\n", k.Repo, detail)
}

func kindLabel(k task.Kind) string {
	if k == task.PullRequest {
		return "PR · "
	}
	return ""
}

func pendingQuestion(rec *task.Record) string {
	for i := len(rec.Decisions) - 1; i >= 0; i-- {
		if rec.Decisions[i].Answer == "" {
			return rec.Decisions[i].Question.Issue
		}
	}
	return "decision needed"
}

func prSummary(rec *task.Record) string {
	if rec.Delegation != nil && rec.Delegation.PR > 0 {
		return fmt.Sprintf("PR #%d", rec.Delegation.PR)
	}
	return "ready"
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
		d := r.Delegation
		fmt.Fprintf(w, "#%-5d %-7s %s\n", r.Number, agentName(d.Implementer.Agent), r.State)
		fmt.Fprintf(w, "       %s · %s\n", r.Title, r.Repo)
		if d.PR > 0 {
			fmt.Fprintf(w, "       PR #%d · reviewer: %s\n", d.PR, agentName(d.Reviewer))
		}
	}
}

func Show(w io.Writer, r *task.Record) {
	fmt.Fprintf(w, "#%d %s\n%s · %s\n\n", r.Number, r.Title, r.Repo, r.URL)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	row := func(k, v string) { fmt.Fprintf(tw, "%s\t%s\n", k, orDash(v)) }
	row("State", string(r.State))
	if d := r.Delegation; d != nil {
		row("Agent", fmt.Sprintf("%s (reviewer: %s)", agentName(d.Implementer.Agent), agentName(d.Reviewer)))
		row("Session", d.Implementer.ID)
		row("Context file", d.ContextFile)
		row("Branch", fmt.Sprintf("%s (from %s)", d.Branch, orDash(d.BaseRef)))
		row("Worktree", d.Worktree)
		row("Runner log", d.RunnerLog)
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
	if g := r.GitHub; g != nil {
		row("GitHub", fmt.Sprintf("%s · updated %s · %s", g.State, g.UpdatedAt.Local().Format(time.DateTime), reasons(g.Reasons)))
	}
	if !r.LastGitHubSync.IsZero() {
		row("Last synced", r.LastGitHubSync.Local().Format(time.DateTime))
	}
	if o := r.LastOutcome; o != nil {
		row("Last outcome", fmt.Sprintf("%s · %s · %s", o.Status, o.At.Local().Format(time.DateTime), o.Summary))
	}
	tw.Flush()
	if len(r.Validation) > 0 {
		fmt.Fprintln(w, "\nValidation (reported by the agent)")
		for _, c := range r.Validation {
			mark := "✓"
			if !c.Passed {
				mark = "✗"
			}
			fmt.Fprintf(w, "  %s %s\n", mark, c.Command)
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
		fmt.Fprintf(w, "#%d could not start: %s\n", r.Number, r.LastOutcome.Summary)
		return
	}
	fmt.Fprintf(w, "Started #%d %s with %s\n", r.Number, r.Title, agentName(d.Implementer.Agent))
	fmt.Fprintf(w, "  branch    %s\n  worktree  %s\n  log       %s\n", d.Branch, d.Worktree, d.RunnerLog)
	fmt.Fprintf(w, "Check progress with: foreman status\n")
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
