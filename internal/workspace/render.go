package workspace

import (
	"fmt"
	"strings"
)

// Render is the readable text followed by the payload in a fenced block. Readers
// use only the payload.
func (r Record) Render() string {
	raw, err := r.Encode()
	if err != nil {
		return r.RenderText()
	}
	return r.RenderText() + fmt.Sprintf("\n```yaml\n%s```\n", raw)
}

// RenderText is the readable progress, questions, PR, plan, decisions, review
// findings, and checkpoint, without the payload.
func (r Record) RenderText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Status: %s\n", r.StatusLabel())
	if !r.StageSince.IsZero() {
		fmt.Fprintf(&b, "Since: %s\n", r.StageSince.Format("2006-01-02 15:04 UTC"))
	}
	if o := r.Outcome; o != nil && o.Summary != "" {
		fmt.Fprintf(&b, "\nLast outcome (%s): %s\n", o.Status, o.Summary)
	}
	if w := r.Work; w != nil {
		if w.Branch != "" {
			fmt.Fprintf(&b, "\nBranch: %s\n", w.Branch)
		}
		if w.PR != nil {
			fmt.Fprintf(&b, "PR: #%d %s\n", w.PR.Number, w.PR.URL)
		}
		if cp := w.Checkpoint; cp != nil {
			fmt.Fprintf(&b, "\nCheckpoint %d: %s\n", cp.Count, cp.Head)
			if cp.Next != "" {
				fmt.Fprintf(&b, "Next: %s\n", cp.Next)
			}
		}
	}
	for i, d := range r.Waiting() {
		q := d.Question
		if i == 0 {
			b.WriteString("\n## Needs your answer\n")
		}
		fmt.Fprintf(&b, "\n%s\n", q.Issue)
		if q.Context != "" {
			fmt.Fprintf(&b, "%s\n", q.Context)
		}
		for _, o := range q.Options {
			fmt.Fprintf(&b, "- %s: %s\n", o.Label, o.Description)
		}
		if q.Recommendation != "" {
			fmt.Fprintf(&b, "Recommended: %s\n", q.Recommendation)
		}
	}
	if w := r.Work; w != nil {
		if w.Requirements != "" {
			fmt.Fprintf(&b, "\n## Requirements\n\n%s\n", w.Requirements)
		}
		if w.Plan != "" {
			fmt.Fprintf(&b, "\n## Implementation plan\n\n%s\n", w.Plan)
		}
		var answered []string
		for _, d := range w.Decisions {
			if d.Answer != "" {
				answered = append(answered, fmt.Sprintf("- %s: %s", d.Question.Issue, d.Answer))
			}
		}
		if len(answered) > 0 {
			fmt.Fprintf(&b, "\n## Decisions\n\n%s\n", strings.Join(answered, "\n"))
		}
		if len(w.Reviews) > 0 {
			b.WriteString("\n## Reviews\n")
			for _, rv := range w.Reviews {
				fmt.Fprintf(&b, "\n%s review, round %d: %s. %s\n", rv.Kind, rv.Round, rv.Verdict, rv.Summary)
				for _, f := range rv.Findings {
					fmt.Fprintf(&b, "- [%s] %s:%d %s\n", f.Severity, f.File, f.Line, f.Summary)
				}
			}
		}
	}
	return b.String()
}
