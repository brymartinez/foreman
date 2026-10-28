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
	unavailable := false
	for _, f := range w.Findings {
		if f.Severity != "blocking" && f.Severity != "should_fix" {
			return ReviewOutcome{}, fmt.Errorf("finding has unknown severity %q", f.Severity)
		}
		if strings.TrimSpace(f.Summary) == "" {
			return ReviewOutcome{}, errors.New("finding has no summary")
		}
		// Earlier reviewers put denied review commands in findings. Keep that
		// environment-only result from blocking even when it is mislabeled a defect.
		if f.Category == "defect" && f.File == nil && f.Line == nil && reviewEnvironmentBlocked(f.Summary+" "+f.Detail) {
			f.Category = "unavailable_check"
		}
		switch f.Category {
		case "unavailable_check":
			if f.File != nil || f.Line != nil || strings.TrimSpace(f.Detail) == "" {
				return ReviewOutcome{}, errors.New("unavailable check needs a reason and no code location")
			}
			out.Limitations = append(out.Limitations, task.Limitation{Behavior: f.Summary, Reason: f.Detail})
			unavailable = true
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
			if unavailable && !blocked {
				out.Clean = true
				out.Summary = "No code defect found; unavailable checks recorded as nonblocking limitations"
				break
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

func reviewEnvironmentBlocked(text string) bool {
	text = strings.ToLower(text)
	context := strings.Contains(text, "review") || strings.Contains(text, "session") || strings.Contains(text, "sandbox")
	denied := strings.Contains(text, "denied") || strings.Contains(text, "permission") || strings.Contains(text, "read-only") || strings.Contains(text, "read only")
	check := strings.Contains(text, "go vet") || strings.Contains(text, "go test") || strings.Contains(text, "e2e") || strings.Contains(text, "test") || strings.Contains(text, "build") || strings.Contains(text, "command") || strings.Contains(text, "check")
	return context && denied && check
}

// ReviewBrief contains only the task context allowed for a fresh review.
type ReviewBrief struct {
	Key               task.Key
	Branch            string
	BaseBranch        string
	StageInstructions string
	Passes            int
	// Self gets the checked plan; the competing reviewer gets only the diff.
	Self bool
	// Claim verification explicitly adds PRD acceptance criteria and claims.
	Claimed bool
	PRD     string
	Claims  []task.Claim
	Plan    string
}

// ReviewPrompt gives self-review the checked plan and competing review the diff.
// Both run in fresh, read-only sessions.
func ReviewPrompt(b ReviewBrief) string {
	var s strings.Builder
	role := "competing reviewer"
	if b.Self {
		role = "self-reviewer"
	}
	fmt.Fprintf(&s, "You are the %s for the change on branch %s in %s. This is a fresh, read-only session. Do not edit files, commit, or push.\n\n", role, b.Branch, b.Key.Repo)
	if b.Self {
		s.WriteString("## Implementation plan\n\nThis is the complete task specification for this review. Check the implemented change against it. Do not read the issue, triage, other planning documents, PR description, or earlier review rounds for requirements.\n\n")
		s.WriteString(quote(b.Plan) + "\n\n")
	} else {
		s.WriteString("## Review scope\n\nForm an independent judgment from the file diff. Do not read the issue, triage, planning documents, PR description, comments, or earlier review rounds for requirements. Read surrounding repository code and tests only as needed to verify a concrete defect in the diff.\n\n")
	}
	if b.Claimed {
		writeClaims(&s, b)
	}
	silentWorkSection(&s)
	if text := strings.TrimSpace(b.StageInstructions); text != "" {
		s.WriteString("## Developer workflow instructions\n\nFollow these review methods where they agree with the scope above.\n\n")
		s.WriteString(text + "\n\n")
	}
	fmt.Fprintf(&s, "## The change\n\nRead the file diff with `git diff origin/%s...HEAD`. Review the complete diff for correctness, regressions, missing cases, and unnecessary changes. Do not run tests, builds, or project code in this read-only session. Report only concrete defects you can verify.\n\n", b.BaseBranch)
	s.WriteString("Use Read to inspect cited files and line ranges, Grep to find symbols and fixture setup, and Glob to locate test files. These tools are available even when other Bash commands are denied. Verify line-number claims and relevant test fixtures yourself before deciding whether the evidence supports a finding. Do not hand verification commands to the developer in place of checking the repository.\n\n")
	if b.Passes > 0 {
		fmt.Fprintf(&s, "Read the complete diff %d times, looking for missed problems on each pass.\n\n", b.Passes)
	}
	s.WriteString("## Final response\n\nReply with JSON matching the provided schema. Use verdict clean with no findings, or changes_requested with one verified defect per finding. Severity is blocking or should_fix. Set file and line when available. ")
	if b.Claimed {
		s.WriteString("List behaviors you could not check in limitations.\n")
	} else {
		s.WriteString("Set limitations to null.\n")
	}
	return s.String()
}

// writeClaims gives the reviewer the PRD and the implementer's claims, and sets it to
// test each claim rather than trust it.
func writeClaims(s *strings.Builder, b ReviewBrief) {
	fmt.Fprintf(s, "## PRD\n\nThe PRD posted on the issue, as it reads now. Its acceptance criteria are what the change must meet.\n\n%s\n\n", quote(b.PRD))
	s.WriteString("## Implementer's claims\n\nThe implementer claims the change delivers these behaviors. Claims and the checks they report are not proof.\n\n")
	for i, c := range b.Claims {
		fmt.Fprintf(s, "%d. Criteria: %s\n   Behavior: %s\n   Evidence: %s\n   Check: %s\n", i+1, strings.Join(c.Criteria, "; "), oneLine(c.Behavior), oneLine(c.Evidence), oneLine(c.Check))
	}
	s.WriteString(`
## Claim verification

- Match every acceptance criterion in the PRD to a claim. A criterion no claim covers is a finding.
- For each claim, read the cited code and tests and decide whether they establish the behavior. A reported passing check is the implementer's validation record, not proof that its tests establish the claimed behavior. Use CI results where you can read them.
- Try to find a counterexample to each claim: an input, state, or path where the claimed behavior fails.
- A material claim the evidence does not support is a finding. Say what the evidence shows and what is missing.
- When you cannot judge a behavior from the available code and tests, add a limitation that names the behavior and the reason. Mark it blocking when it keeps you from judging an acceptance criterion or could hide a plausible serious defect. A blocking limitation means the verdict is "changes_requested", with a blocking finding that says what would make the behavior checkable.
- A criterion that only requires commands such as vet, tests, or end-to-end checks to pass belongs to implementation validation. If the implementer reports those checks as passed but this review cannot rerun them, note the execution limit as nonblocking. A visible failure, missing test for a plausible failure path, or concrete code defect is still a finding.

`)
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// FindingsMessage resumes the implementer with a review round to address.
func FindingsMessage(r task.Review) string {
	var s strings.Builder
	fmt.Fprintf(&s, "Review round %d of PR #%d by %s requested changes.\n\n%s\n\n", r.Round, r.PR, reviewerName(r), orNone(r.Summary, ""))
	writeFindings(&s, r.Findings)
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
