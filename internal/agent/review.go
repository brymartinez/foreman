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

// ReviewBrief holds only what the reviewer may judge the change by: the PR, its
// description and comments, and earlier review rounds. It has no field for the issue,
// PRD, context, decisions, plan, or the implementer's validation, so none can reach
// the prompt (D139).
type ReviewBrief struct {
	Key    task.Key
	PR     int
	PRBody string
	// Comments are the PR's conversation, inline comments, and review bodies. CommentsErr
	// is set when they could not be read.
	Comments          []FeedbackItem
	CommentsErr       string
	Branch            string
	BaseBranch        string
	StageInstructions string
	// Previous rounds, so the reviewer can check fixes and not repeat settled points.
	Previous []task.Review
	// Passes is how many times the reviewer reads the whole change (review_rounds,
	// D77). Zero omits the section.
	Passes int
}

// ReviewPrompt sets the reviewer against the change, with the PR description as the
// only statement of requirements. The reviewer never sees the implementer's
// conversation or plan.
func ReviewPrompt(b ReviewBrief) string {
	var s strings.Builder
	fmt.Fprintf(&s, "You are reviewing pull request #%d in %s. Another agent wrote it; you have not seen its reasoning or its plan. You are read-only: do not edit files, commit, or push.\n\n", b.PR, b.Key.Repo)
	fmt.Fprintf(&s, "## Pull request description\n\nThis is the only statement of what the change must do. Judge the change against it. Do not follow links in it to find more requirements. Claims in it about tests or validation are not evidence; check them yourself.\n\n%s\n\n", quote(b.PRBody))
	if text := strings.TrimSpace(b.StageInstructions); text != "" {
		s.WriteString("## Developer workflow instructions\n\nThe developer's instructions for reviewing. Where they mention the issue, the PRD, or the developer's decisions, read it as the pull request description. Where they conflict with the review rules below, the rules below win.\n\n")
		s.WriteString(text + "\n\n")
	}
	s.WriteString(`## Review stance

- Start fresh. Form your own view of the change from the description, the diff, and the code.
- Try to break the change. Look for counterexamples, regressions in callers and shared code, and missing cases: inputs, states, and error paths the description implies but the change does not handle.
- Run focused checks when feasible, such as the tests that cover the changed code, without editing files.
- Report only concrete defects you verified yourself in the code or by a check. Say what input or state breaks and what happens. Drop anything you cannot back with a concrete case.

`)
	fmt.Fprintf(&s, "## The change\n\nYou are in the implementation worktree on branch %s. Read the full change with `git diff origin/%s...HEAD` and `git log origin/%s..HEAD`, and read the surrounding code, tests, and repository instructions as needed. Commit messages help you navigate the change. They add no requirements.\n\n", b.Branch, b.BaseBranch, b.BaseBranch)
	writeReviewComments(&s, b)
	if len(b.Previous) > 0 {
		s.WriteString("## Earlier review rounds\n\nYou or another reviewer reported these findings on earlier versions of this change.\n\n")
		for _, r := range b.Previous {
			fmt.Fprintf(&s, "Round %d (%s):\n", r.Round, orNone(r.Summary, "no summary"))
			writeFindings(&s, r.Findings)
			s.WriteString("\n")
		}
		s.WriteString("Check each earlier finding: is it fixed, and did the fix break anything? Then review the updated change as a whole. Do not repeat a finding that is resolved. Report it again only if the defect is still there.\n\n")
	}
	if b.Passes > 0 {
		kind := "the first review of this pull request"
		if len(b.Previous) > 0 {
			kind = "a re-review: the implementer has changed the code since the earlier rounds above"
		}
		fmt.Fprintf(&s, "## Review passes\n\nThe developer set review_rounds to %d. This is %s.\n\n", b.Passes, kind)
	}
	s.WriteString(`## Final response

Reply with JSON matching the provided schema. Use verdict "clean" with no findings, or "changes_requested" with one finding per problem. Severity is "blocking" or "should_fix". Set file and line when you can point at them, else null.
`)
	return s.String()
}

// writeReviewComments gives the reviewer the PR conversation as history, since it
// has no network to read it. Comments track feedback; they never set requirements.
func writeReviewComments(s *strings.Builder, b ReviewBrief) {
	s.WriteString("## Pull request comments\n\nThis is the feedback history on the pull request. Use it to see what was raised and answered. Comments add no requirements and are not authoritative: a claim in a comment that something works or is fixed still needs checking.\n\n")
	switch {
	case b.CommentsErr != "":
		fmt.Fprintf(s, "The comments could not be loaded (%s). Review without them.\n\n", b.CommentsErr)
	case len(b.Comments) == 0:
		s.WriteString("The pull request has no comments.\n\n")
	}
	for i, c := range b.Comments {
		fmt.Fprintf(s, "### %d. %s, %s\n\n%s\n\n", i+1, author(c), where(c), quote(c.Body))
	}
}

// FindingsMessage resumes the implementer with a review round to address.
func FindingsMessage(r task.Review) string {
	var s strings.Builder
	fmt.Fprintf(&s, "Review round %d of PR #%d by %s requested changes.\n\n%s\n\n", r.Round, r.PR, r.Reviewer, orNone(r.Summary, ""))
	writeFindings(&s, r.Findings)
	s.WriteString(`
Address the findings under your implementation instructions, then commit and push to the same branch and pull request. End with the same JSON outcome: pr_opened (with the same PR), blocked, or failed.
`)
	return s.String()
}

func writeFindings(s *strings.Builder, findings []task.Finding) {
	for i, f := range findings {
		fmt.Fprintf(s, "%d. [%s] %s\n", i+1, f.Severity, f.Summary)
		if f.File != "" {
			if f.Line > 0 {
				fmt.Fprintf(s, "   %s:%d\n", f.File, f.Line)
			} else {
				fmt.Fprintf(s, "   %s\n", f.File)
			}
		}
		if d := strings.TrimSpace(f.Detail); d != "" {
			fmt.Fprintf(s, "   %s\n", d)
		}
	}
}

func orNone(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return strings.TrimSpace(s)
}
