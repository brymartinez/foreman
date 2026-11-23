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
// the implementer. Codex strict mode requires every property, so nullable fields are
// used instead of optional ones. Claude's schema below makes the empty findings and
// limitations arrays optional; ParseReview already treats omitted arrays as empty.
//
//go:embed review.schema.json
var ReviewSchema []byte

func claudeReviewSchema() ([]byte, error) {
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(ReviewSchema, &schema); err != nil {
		return nil, fmt.Errorf("decode review schema: %w", err)
	}
	rawRequired, ok := schema["required"]
	if !ok {
		return nil, errors.New("review schema has no required fields")
	}
	var required []string
	if err := json.Unmarshal(rawRequired, &required); err != nil {
		return nil, fmt.Errorf("decode review schema required fields: %w", err)
	}
	seen := map[string]bool{}
	required = slices.DeleteFunc(required, func(field string) bool {
		if field == "findings" || field == "limitations" {
			seen[field] = true
			return true
		}
		return false
	})
	if !seen["findings"] || !seen["limitations"] {
		return nil, errors.New("review schema must require findings and limitations before Claude normalization")
	}
	rawRequired, err := json.Marshal(required)
	if err != nil {
		return nil, fmt.Errorf("encode Claude review schema required fields: %w", err)
	}
	schema["required"] = rawRequired
	result, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("encode Claude review schema: %w", err)
	}
	return result, nil
}

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
	DocOnly     []task.DocOnlyPath
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
		Limitations []task.Limitation  `json:"limitations"`
		DocOnly     []task.DocOnlyPath `json:"docOnly"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return ReviewOutcome{}, fmt.Errorf("review is not valid JSON: %w", err)
	}
	out := ReviewOutcome{Summary: w.Summary, DocOnly: w.DocOnly}
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
	Passes            int
	StageInstructions string
	FullSuiteRevision string
}

// ReviewPrompt gives a fresh reviewer the pinned branch or fix diff.
func ReviewPrompt(b ReviewBrief) string {
	var s strings.Builder
	s.WriteString("## Review data\n\n")
	if b.FromHead != "" {
		fmt.Fprintf(&s, "Assigned diff command: `git diff --no-ext-diff --no-textconv --ignore-submodules=none %s..%s --`\n\n", b.FromHead, b.Head)
	} else {
		fmt.Fprintf(&s, "Assigned diff command: `git diff --no-ext-diff --no-textconv --ignore-submodules=none %s...%s --`\n\n", b.BaseHead, b.Head)
	}
	fmt.Fprintf(&s, "Review passes: %d\n", b.Passes)
	if b.FullSuiteRevision != "" {
		fmt.Fprintf(&s, "Full suite revision: %s\n", b.FullSuiteRevision)
	} else {
		s.WriteString("Full suite revision: none\n")
	}
	if text := strings.TrimSpace(b.StageInstructions); text != "" {
		s.WriteString("\n## Stage instructions\n\n")
		s.WriteString(text + "\n")
	}
	return s.String()
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
