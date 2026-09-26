package agent

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

// ReviewSchema constrains the reviewer's final message, like OutcomeSchema does for
// the implementer. Strict mode again: nullable fields instead of optional ones.
//
//go:embed review.schema.json
var ReviewSchema []byte

type ReviewOutcome struct {
	Clean    bool
	Summary  string
	Findings []task.Finding
	Usage    task.Usage
}

func ParseReview(raw []byte) (ReviewOutcome, error) {
	var w struct {
		Verdict  string `json:"verdict"`
		Summary  string `json:"summary"`
		Findings []struct {
			Severity string  `json:"severity"`
			File     *string `json:"file"`
			Line     *int    `json:"line"`
			Summary  string  `json:"summary"`
			Detail   string  `json:"detail"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return ReviewOutcome{}, fmt.Errorf("review is not valid JSON: %w", err)
	}
	out := ReviewOutcome{Summary: w.Summary}
	for _, f := range w.Findings {
		if f.Severity != "blocking" && f.Severity != "should_fix" {
			return ReviewOutcome{}, fmt.Errorf("finding has unknown severity %q", f.Severity)
		}
		if strings.TrimSpace(f.Summary) == "" {
			return ReviewOutcome{}, errors.New("finding has no summary")
		}
		tf := task.Finding{Severity: f.Severity, Summary: f.Summary, Detail: f.Detail}
		if f.File != nil {
			tf.File = *f.File
		}
		if f.Line != nil {
			tf.Line = *f.Line
		}
		out.Findings = append(out.Findings, tf)
	}
	switch w.Verdict {
	case "clean":
		if len(out.Findings) > 0 {
			return ReviewOutcome{}, errors.New("review is clean but lists findings")
		}
		out.Clean = true
	case "changes_requested":
		if len(out.Findings) == 0 {
			return ReviewOutcome{}, errors.New("review requests changes but lists no findings")
		}
	default:
		return ReviewOutcome{}, fmt.Errorf("unknown review verdict %q", w.Verdict)
	}
	return out, nil
}

type ReviewBrief struct {
	TaskBrief
	PR         int
	Validation []task.Check
	// Previous rounds, so the reviewer can check fixes and not repeat settled points.
	Previous []task.Review
}

// ReviewPrompt is built only from durable state: the issue, context, decisions, and
// the branch diff. The reviewer never sees the implementer's conversation.
func ReviewPrompt(b ReviewBrief) string {
	var s strings.Builder
	fmt.Fprintf(&s, "You are reviewing pull request #%d for GitHub issue %s#%d. Another agent implemented it; you have not seen its reasoning. You are read-only: do not edit files, commit, or push.\n\n", b.PR, b.Key.Repo, b.Key.Number)
	fmt.Fprintf(&s, "## Issue\n\n%s\n%s\n\n%s\n\n", b.Title, b.URL, orNone(b.IssueBody, "(The issue has no description.)"))
	stageSection(&s, b.StageInstructions)
	if ctx := strings.TrimSpace(b.Context); ctx != "" {
		fmt.Fprintf(&s, "## Implementation context\n\nFrom the developer, authoritative:\n\n%s\n\n", ctx)
	}
	if answered := answeredDecisions(b.Decisions); len(answered) > 0 {
		s.WriteString("## Decisions the developer made\n\n")
		for _, d := range answered {
			fmt.Fprintf(&s, "- Q: %s\n  A: %s\n", d.Question.Issue, d.Answer)
		}
		s.WriteString("\n")
	}
	fmt.Fprintf(&s, "## The change\n\nYou are in the implementation worktree on branch %s. Read the full change with `git diff origin/%s...HEAD` and `git log origin/%s..HEAD`, and read surrounding code as needed.\n\n", b.Branch, b.BaseBranch, b.BaseBranch)
	if len(b.Validation) > 0 {
		s.WriteString("The implementer reports this validation:\n")
		for _, c := range b.Validation {
			fmt.Fprintf(&s, "- %s: %s\n", c.Command, passFail(c.Passed))
		}
		s.WriteString("\n")
	}
	if len(b.Previous) > 0 {
		s.WriteString("## Earlier review rounds\n\n")
		for _, r := range b.Previous {
			fmt.Fprintf(&s, "Round %d (%s):\n", r.Round, orNone(r.Summary, "no summary"))
			for _, f := range r.Findings {
				fmt.Fprintf(&s, "- [%s] %s\n", f.Severity, f.Summary)
			}
		}
		s.WriteString("\n")
	}
	s.WriteString(`## Final response

Reply with JSON matching the provided schema. Use verdict "clean" with no findings when nothing substantive remains; otherwise "changes_requested" with one finding per problem. Severity is "blocking" or "should_fix". Set file and line when you can point at them, else null.
`)
	return s.String()
}

// FindingsMessage resumes the implementer with a review round to address.
func FindingsMessage(r task.Review) string {
	var s strings.Builder
	fmt.Fprintf(&s, "Review round %d of PR #%d by %s requested changes.\n\n%s\n\n", r.Round, r.PR, r.Reviewer, orNone(r.Summary, ""))
	for i, f := range r.Findings {
		fmt.Fprintf(&s, "%d. [%s] %s\n", i+1, f.Severity, f.Summary)
		if f.File != "" {
			if f.Line > 0 {
				fmt.Fprintf(&s, "   %s:%d\n", f.File, f.Line)
			} else {
				fmt.Fprintf(&s, "   %s\n", f.File)
			}
		}
		if d := strings.TrimSpace(f.Detail); d != "" {
			fmt.Fprintf(&s, "   %s\n", d)
		}
	}
	s.WriteString(`
Address the findings under your implementation instructions, then commit and push to the same branch and pull request. End with the same JSON outcome: pr_opened (with the same PR), blocked, or failed.
`)
	return s.String()
}

func passFail(ok bool) string {
	if ok {
		return "passed"
	}
	return "failed"
}

func orNone(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return strings.TrimSpace(s)
}
