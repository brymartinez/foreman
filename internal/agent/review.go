package agent

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

// ReviewSchema constrains the reviewer's final message, like OutcomeSchema does for
// the implementer. Strict mode again: nullable fields instead of optional ones.
//
//go:embed review.schema.json
var ReviewSchema []byte

type ReviewOutcome struct {
	Skipped bool // Controller-only: adversarial provider has confirmed exhausted quota.
	// These fields are set by the runner, never parsed from model output.
	BaseRef  string
	BaseHead string
	FromHead string
	Advice   []task.Advice
	Clean    bool
	Summary  string
	Findings []task.Finding
	// Limitations are the behaviors the reviewer could not check.
	Limitations []task.Limitation
	Usage       task.Usage
}

func ParseReview(raw []byte) (ReviewOutcome, error) {
	var w struct {
		Verdict  string `json:"verdict"`
		Summary  string `json:"summary"`
		Findings []struct {
			Category string  `json:"category"`
			Severity string  `json:"severity"`
			File     *string `json:"file"`
			Line     *int    `json:"line"`
			Summary  string  `json:"summary"`
			Detail   string  `json:"detail"`
		} `json:"findings"`
		Limitations []task.Limitation `json:"limitations"`
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
		switch f.Category {
		case "writing_advice":
			out.Advice = append(out.Advice, task.Advice{Summary: f.Summary, Detail: f.Detail})
			continue
		case "defect":
		default:
			return ReviewOutcome{}, fmt.Errorf("finding has unknown category %q", f.Category)
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
	blocked := false
	for _, l := range w.Limitations {
		if strings.TrimSpace(l.Behavior) == "" || strings.TrimSpace(l.Reason) == "" {
			return ReviewOutcome{}, errors.New("limitation needs the behavior and the reason it could not be checked")
		}
		blocked = blocked || l.Blocking
	}
	out.Limitations = append(out.Limitations, w.Limitations...)
	switch w.Verdict {
	case "clean":
		if len(out.Findings) > 0 {
			return ReviewOutcome{}, errors.New("review is clean but lists findings")
		}
		// A blocking limitation keeps the reviewer from judging the change, so the
		// review cannot pass it.
		if blocked {
			return ReviewOutcome{}, errors.New("review is clean but lists a blocking limitation")
		}
		out.Clean = true
	case "changes_requested":
		if len(out.Findings) == 0 {
			for _, l := range out.Limitations {
				if l.Blocking {
					return ReviewOutcome{}, fmt.Errorf("review incomplete for %s: %s", l.Behavior, l.Reason)
				}
			}
			return ReviewOutcome{}, errors.New("review requests changes but lists no findings")
		}
		// The implementer receives findings, not limitations, so a blocking limitation
		// reaches it only through a blocking finding.
		if blocked && !slices.ContainsFunc(out.Findings, func(f task.Finding) bool { return f.Severity == "blocking" }) {
			return ReviewOutcome{}, errors.New("review lists a blocking limitation but no blocking finding")
		}
	default:
		return ReviewOutcome{}, fmt.Errorf("unknown review verdict %q", w.Verdict)
	}
	return out, nil
}

// ReviewBrief pins the code under review to two Git revisions.
type ReviewBrief struct {
	BaseHead          string
	FromHead          string
	Head              string
	Self              bool
	Passes            int
	StageInstructions string
}

// ReviewPrompt gives a fresh reviewer the pinned branch or fix diff.
func ReviewPrompt(b ReviewBrief) string {
	var s strings.Builder
	role := "competing reviewer"
	if b.Self {
		role = "self-reviewer"
	}
	fmt.Fprintf(&s, "You are the %s for this change. This is a read-only review session. Do not edit files, commit, or push.\n\n", role)
	s.WriteString("## Review scope\n\nJudge concrete defects and regressions introduced by the supplied diff. Read surrounding code and tests as needed to verify a finding. Do not read the issue, plan, claims, decisions, PR description, previous reviews, or implementation transcript.\n\n")
	if b.FromHead != "" {
		fmt.Fprintf(&s, "Read the fix diff with `git diff --no-ext-diff --no-textconv --ignore-submodules=none %s..%s --`. Review every changed line in this range.\n\n", b.FromHead, b.Head)
	} else {
		fmt.Fprintf(&s, "Read the complete diff with `git diff --no-ext-diff --no-textconv --ignore-submodules=none %s...%s --` from the current working directory. Review every change before returning. If the tool truncates the output, inspect the missing paths separately using the same revisions.\n\n", b.BaseHead, b.Head)
	}
	if b.Passes > 1 {
		fmt.Fprintf(&s, "Read the %s diff %d times, looking for missed problems on each pass.\n\n", diffLabel(b.FromHead), b.Passes)
	} else if b.FromHead == "" {
		s.WriteString("Read the complete diff once.\n\n")
	}
	silentWorkSection(&s)
	if text := strings.TrimSpace(b.StageInstructions); text != "" {
		s.WriteString("## Developer workflow instructions\n\nFollow these review methods where they agree with the scope above.\n\n")
		s.WriteString(text + "\n\n")
	}
	s.WriteString("Use read-only tools to check files, callers, and test cases. Leave tests, builds, and execution of project code to the implementer.\n\n")
	s.WriteString("## Final response\n\nReply with JSON matching the provided schema. Report concrete defects with a failing case and its effect. Severity is blocking or should_fix. Set file and line when available. Use clean when you inspected the entire supplied diff and found no defects. If you cannot inspect the entire supplied diff, return changes_requested and a blocking limitation with the reason. Report only verified defects as findings.\n")
	return s.String()
}

func diffLabel(from string) string {
	if from != "" {
		return "fix"
	}
	return "complete"
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// FindingsMessage resumes the implementer with a review round to address.
func FindingsMessage(r task.Review) string {
	var s strings.Builder
	fmt.Fprintf(&s, "Review round %d of PR #%d by %s requested changes.\n\n%s\n\n", r.Round, r.PR, reviewerName(r), orNone(r.Summary, ""))
	writeFindings(&s, r.Findings)
	assessmentInstructions(&s, r)
	s.WriteString(`
Address the findings under your implementation instructions, then commit and push to the same branch and pull request. End with the same JSON outcome: pr_opened (with the same PR), blocked, or failed.
`)
	return s.String()
}

// LocalFindingsMessage resumes the implementer of a task reviewed before its PR. The
// fix stays local and comes back with the updated proposed description.
func LocalFindingsMessage(r task.Review) string {
	var s strings.Builder
	fmt.Fprintf(&s, "Review round %d of your local change by %s requested changes.\n\n%s\n\n", r.Round, reviewerName(r), orNone(r.Summary, ""))
	writeFindings(&s, r.Findings)
	assessmentInstructions(&s, r)
	s.WriteString("\nAddress the findings under your implementation instructions. ")
	s.WriteString(localFinishRule)
	s.WriteString("End with the same JSON outcome: review_ready, blocked, or failed.\n")
	return s.String()
}

// reviewerName names who reviewed, so the implementer knows a self-review's findings
// come from its own fresh, read-only session.
func reviewerName(r task.Review) string {
	if r.Kind == task.SelfReview {
		return fmt.Sprintf("your own self-review (a fresh %s session)", r.Reviewer)
	}
	return string(r.Reviewer)
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

func assessmentInstructions(s *strings.Builder, r task.Review) {
	s.WriteString("\nAssess each finding using your task context. Fix valid defects, reject invalid findings with concrete evidence, and ask the developer about product decisions or conflicting requirements. Do not change approved behavior merely to satisfy a reviewer.\n")
	if r.Kind == task.SelfReview {
		fmt.Fprintf(s, "Always account for self-review findings in reviewAssessment when returning a correction. For rejected self-review findings, set reviewAssessment to {\"round\": %d, \"rejected\": [{\"finding\": 1, \"reason\": \"concrete evidence\"}]}, using the one-based finding numbers above. Omit fixed findings from rejected; an empty rejected list means all findings were fixed and still requires review. Rejecting every finding without changing the reviewed commit ends self-review early. Otherwise the new commit gets another independent self-review.\n", r.Round)
	} else {
		s.WriteString("The next adversarial reviewer starts fresh and checks the fix diff. Before returning a successful correction, verify that every finding is resolved on the resulting revision. If any finding remains unresolved, return blocked and explain what is needed. Set reviewAssessment to null. A clean adversarial re-review is required.\n")
	}
}

// SelfReviewLimitInstruction keeps a resumed or rebuilt assessor within the fix limit.
const SelfReviewLimitInstruction = "\nThe self-review fix limit is reached. Assess the remaining findings without changing code. Reject invalid findings with reviewAssessment and concrete evidence, or return blocked with your question about how to proceed. Do not make another correction until the developer answers.\n"
