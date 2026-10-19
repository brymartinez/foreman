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

// ReviewBrief holds only what the reviewer may judge the change by: the PR, its
// description and comments, earlier review rounds, and for a task with claim
// verification the PRD and the implementer's claims. It has no field for the issue,
// context, decisions, plan, or the implementer's validation, so none can reach the
// prompt.
type ReviewBrief struct {
	Key    task.Key
	PR     int
	PRBody string
	// PRTitle is the proposed title, shown only when Proposed is set.
	PRTitle string
	// Comments are the PR's conversation, inline comments, and review bodies. CommentsErr
	// is set when they could not be read.
	Comments          []FeedbackItem
	CommentsErr       string
	Branch            string
	BaseBranch        string
	StageInstructions string
	// Previous rounds, so the reviewer can check fixes and not repeat settled points.
	Previous []task.Review
	// Passes is how many times the reviewer reads the whole change (review_rounds).
	// Zero omits the section.
	Passes int
	// Proposed is set when the change is not pushed yet: PRBody is the description
	// the implementer proposes, and the PR opens only after a clean review.
	Proposed bool
	// Self is set for the implementing agent's own review. It reads the description
	// and the code alone: no comments and no earlier rounds.
	Self bool
	// Claimed is set for a task with claim verification. PRD is the current text of
	// the PRD comment on the issue, whose acceptance criteria the change must meet,
	// and Claims are the implementer's claims, which the reviewer tests.
	Claimed bool
	PRD     string
	Claims  []task.Claim
}

// ReviewPrompt sets the reviewer against the change, with the PR description as the
// only statement of requirements, or, for a task with claim verification, the PRD's
// acceptance criteria and the claims to test. The reviewer never sees the
// implementer's conversation or plan.
func ReviewPrompt(b ReviewBrief) string {
	var s strings.Builder
	heading := "Pull request description"
	author := "Another agent wrote it; you have not seen its reasoning or its plan."
	if b.Self {
		author = "It was written in another session you have not seen. Judge it as a stranger's change: you have not seen its reasoning or its plan."
	}
	if b.Proposed {
		fmt.Fprintf(&s, "You are reviewing a proposed pull request from branch %s in %s. It is not pushed yet; the pull request opens only after a clean review. %s You are read-only: do not edit files, commit, or push.\n\n", b.Branch, b.Key.Repo, author)
		heading = "Proposed pull request description"
	} else {
		fmt.Fprintf(&s, "You are reviewing pull request #%d in %s. %s You are read-only: do not edit files, commit, or push.\n\n", b.PR, b.Key.Repo, author)
	}
	if b.Claimed {
		fmt.Fprintf(&s, "## %s\n\nThis is the change as the implementer describes it. The PRD below holds the acceptance criteria the change must meet. Do not follow links in either to find more requirements. Claims in the description about tests or validation are not proof of behavior; judge the cited code and tests yourself.\n\n", heading)
	} else {
		fmt.Fprintf(&s, "## %s\n\nThis is the only statement of what the change must do. Judge the change against it. Do not follow links in it to find more requirements. Claims in it about tests or validation are not proof of behavior; judge the code and tests yourself.\n\n", heading)
	}
	if b.Proposed {
		fmt.Fprintf(&s, "Title: %s\n\n", b.PRTitle)
	}
	fmt.Fprintf(&s, "%s\n\n", quote(b.PRBody))
	if b.Claimed {
		writeClaims(&s, b)
	}
	silentWorkSection(&s)
	if text := strings.TrimSpace(b.StageInstructions); text != "" {
		read := "Where they mention the issue, the PRD, or the developer's decisions, read it as the pull request description."
		if b.Claimed {
			read = "Where they mention the issue or the developer's decisions, read it as the PRD and the pull request description."
		}
		fmt.Fprintf(&s, "## Developer workflow instructions\n\nThe developer's instructions for reviewing. %s Where they conflict with the review rules below, the rules below win.\n\n", read)
		s.WriteString(text + "\n\n")
	}
	s.WriteString(`## Review stance

- Start fresh. Form your own view of the change from the description, the diff, and the code.
- Try to break the change. Look for counterexamples, regressions in callers and shared code, and missing cases: inputs, states, and error paths the description implies but the change does not handle.
- This is a read-only code review. Do not run vet, tests, builds, end-to-end scripts, or other commands that execute project code. The implementer runs validation in its writable session.
- Read the tests and code to judge behavior. A reported passing check is not proof that the behavior is correct; look for concrete counterexamples.
- If this review cannot independently rerun a reported check because of its permissions, do not request changes for that reason alone. In a claim review, record it as a nonblocking limitation.
- Report only concrete defects you verified yourself in the code or by a check. Say what input or state breaks and what happens. Drop anything you cannot back with a concrete case.

`)
	fmt.Fprintf(&s, "## The change\n\nYou are in the implementation worktree on branch %s. Read the full change with `git diff origin/%s...HEAD` and `git log origin/%s..HEAD`, and read the surrounding code, tests, and repository instructions as needed. Commit messages help you navigate the change. They add no requirements.\n\n", b.Branch, b.BaseBranch, b.BaseBranch)
	if !b.Proposed && !b.Self {
		writeReviewComments(&s, b)
	}
	if len(b.Previous) > 0 {
		s.WriteString("## Earlier review rounds\n\nYou or another reviewer reported these findings in earlier rounds of this change.\n\n")
		for _, r := range b.Previous {
			fmt.Fprintf(&s, "Round %d (%s):\n", r.Round, orNone(r.Summary, "no summary"))
			writeFindings(&s, r.Findings)
			s.WriteString("\n")
		}
		s.WriteString("Check each earlier finding: is it fixed, and did the fix break anything? Then review the change as a whole. Do not repeat a finding that is resolved. An earlier finding based only on this review's inability to run validation commands is not a code defect; do not repeat it as a finding. Report a defect again only if it is still there.\n\n")
		if b.Claimed {
			s.WriteString("Record that inability as a nonblocking limitation.\n\n")
		}
	}
	if b.Passes > 0 {
		kind := "the first review of this pull request"
		if len(b.Previous) > 0 {
			kind = "a re-review after the earlier rounds above"
		}
		fmt.Fprintf(&s, "## Review passes\n\nThe developer set review_rounds to %d. This is %s.\n\n", b.Passes, kind)
	}
	s.WriteString(`## Final response

Reply with JSON matching the provided schema. Use verdict "clean" with no findings, or "changes_requested" with one finding per problem. Set category to "defect" for a concrete code or behavior problem. If you put an inability to execute a check in findings, set category to "unavailable_check"; Foreman records it as a nonblocking limitation, never as a defect. Severity is "blocking" or "should_fix". Set file and line when you can point at them, else null. An unavailable check has no file or line and needs the reason in detail.
`)
	if b.Claimed {
		s.WriteString("List each behavior you could not check in limitations, with the reason, or set limitations to []. A blocking limitation needs verdict \"changes_requested\" and a blocking finding.\n")
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
