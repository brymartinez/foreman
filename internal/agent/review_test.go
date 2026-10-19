package agent

import (
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func reviewBrief() ReviewBrief {
	return ReviewBrief{Key: task.Key{Repo: "a/b", Number: 825}, PR: 912, PRBody: "Retry 5xx responses only.\n\nCloses #825",
		Branch: "feat/825-provider-retries", BaseBranch: "main"}
}

func TestReviewPromptOmitsIssuePRDContextDecisionsAndValidation(t *testing.T) {
	// ReviewBrief has no field for any of these, so the prompt cannot carry them. The
	// implementation prompt built from the same task does, which proves the strings
	// would show if a field leaked through.
	tb := TaskBrief{Key: task.Key{Repo: "a/b", Number: 825}, Title: "ISSUE-TITLE", URL: "https://issue-url/825", IssueBody: "ISSUE-BODY",
		Context: "CONTEXT-FILE", PRD: "PRD-TEXT", Decisions: []task.Decision{{Question: task.Question{Issue: "DECISION-Q"}, Answer: "DECISION-A"}},
		Plan: []task.PlanComment{{Doc: task.ImplementationPlan, URL: "https://plan-url/4"}}}
	impl := ImplementationPrompt(tb)
	p := ReviewPrompt(reviewBrief())
	for _, leaked := range []string{"ISSUE-TITLE", "https://issue-url/825", "ISSUE-BODY", "CONTEXT-FILE", "DECISION-Q", "DECISION-A", "https://plan-url/4"} {
		if !strings.Contains(impl, leaked) {
			t.Fatalf("implementation prompt lacks %q, so the check proves nothing", leaked)
		}
		if strings.Contains(p, leaked) {
			t.Errorf("review prompt carries %q", leaked)
		}
	}
	for _, section := range []string{"## Issue", "## PRD", "## Implementation context", "## Decisions the developer made", "## The plan", "implementer reports this validation"} {
		if strings.Contains(p, section) {
			t.Errorf("review prompt has %q", section)
		}
	}
}

func TestReviewPromptStatesPRDescriptionAsOnlyRequirements(t *testing.T) {
	p := ReviewPrompt(reviewBrief())
	for _, want := range []string{"pull request #912", "## Pull request description", "> Retry 5xx responses only.", "> Closes #825",
		"only statement of what the change must do", "Do not follow links in it", "read-only"} {
		if !strings.Contains(p, want) {
			t.Errorf("review prompt missing %q:\n%s", want, p)
		}
	}
}

func TestReviewPromptLabelsCommentsAsNonAuthoritative(t *testing.T) {
	b := reviewBrief()
	b.Comments = []FeedbackItem{
		{Kind: task.IssueComment, Author: "dev", Body: "Also handle 429."},
		{Kind: task.ReviewComment, Author: "dev", Path: "retry.go", Line: 40, Body: "Off by one here."},
	}
	p := ReviewPrompt(b)
	for _, want := range []string{"## Pull request comments", "history", "add no requirements", "not authoritative",
		"dev, a comment on the pull request", "> Also handle 429.", "dev, an inline comment on retry.go:40", "Commit messages", "git log origin/main..HEAD"} {
		if !strings.Contains(p, want) {
			t.Errorf("review prompt missing %q:\n%s", want, p)
		}
	}
	b.Comments, b.CommentsErr = nil, "gh: HTTP 502"
	if p := ReviewPrompt(b); !strings.Contains(p, "could not be loaded (gh: HTTP 502)") {
		t.Errorf("unreadable comments must be named:\n%s", p)
	}
	b.CommentsErr = ""
	if p := ReviewPrompt(b); !strings.Contains(p, "The pull request has no comments.") {
		t.Errorf("no comments must be said:\n%s", p)
	}
}

func TestReviewPromptAsksForVerifiedCounterexamples(t *testing.T) {
	p := ReviewPrompt(reviewBrief())
	for _, want := range []string{"## Review stance", "fresh", "counterexamples", "regressions", "missing cases", "Do not run vet, tests, builds", "Read the tests and code to judge behavior",
		"nonblocking limitation", "do not request changes for that reason alone", "Report only concrete defects you verified yourself", "not proof of behavior"} {
		if !strings.Contains(p, want) {
			t.Errorf("review prompt missing %q:\n%s", want, p)
		}
	}
}

func TestReReviewPromptListsEarlierFindingsInFull(t *testing.T) {
	b := reviewBrief()
	b.Previous = []task.Review{{Round: 1, Summary: "one bug", Findings: []task.Finding{
		{Severity: "blocking", File: "retry.go", Line: 40, Summary: "4xx retried", Detail: "The guard checks >= 400."}}}}
	p := ReviewPrompt(b)
	for _, want := range []string{"## Earlier review rounds", "Round 1 (one bug)", "1. [blocking] 4xx retried", "retry.go:40", "The guard checks >= 400.",
		"Check each earlier finding", "review the change as a whole", "Do not repeat a finding that is resolved",
		"inability to run validation commands is not a code defect"} {
		if !strings.Contains(p, want) {
			t.Errorf("re-review prompt missing %q:\n%s", want, p)
		}
	}
	if p := ReviewPrompt(reviewBrief()); strings.Contains(p, "## Earlier review rounds") {
		t.Errorf("a first review has no earlier rounds:\n%s", p)
	}
	if strings.Contains(p, "Record that inability as a nonblocking limitation") {
		t.Errorf("a review without claims cannot report limitations:\n%s", p)
	}
	b.Claimed = true
	if p := ReviewPrompt(b); !strings.Contains(p, "Record that inability as a nonblocking limitation") {
		t.Errorf("a claim re-review should retain the execution limit as a limitation:\n%s", p)
	}
}

func TestReviewPromptKeepsPassCount(t *testing.T) {
	b := reviewBrief()
	b.Passes = 3
	if p := ReviewPrompt(b); !strings.Contains(p, "## Review passes") || !strings.Contains(p, "review_rounds to 3. This is the first review") {
		t.Errorf("first review:\n%s", p)
	}
	b.Previous = []task.Review{{Round: 1, Findings: []task.Finding{{Severity: "blocking", Summary: "x"}}}}
	if p := ReviewPrompt(b); !strings.Contains(p, "review_rounds to 3. This is a re-review after the earlier rounds") {
		t.Errorf("re-review:\n%s", p)
	}
	b.Passes = 0
	if p := ReviewPrompt(b); strings.Contains(p, "## Review passes") {
		t.Errorf("no passes must omit the section:\n%s", p)
	}
}

func TestReviewPromptStageInstructionsYieldToTheReviewRules(t *testing.T) {
	b := reviewBrief()
	b.StageInstructions = "Check the change against the issue and the PRD."
	p := ReviewPrompt(b)
	stage, stance := strings.Index(p, "## Developer workflow instructions"), strings.Index(p, "## Review stance")
	if stage < 0 || stance < stage || !strings.Contains(p, "Check the change against the issue and the PRD.") {
		t.Fatalf("stage instructions must appear before the review rules:\n%s", p)
	}
	if !strings.Contains(p, "read them as confirmed triage and the pull request description") {
		t.Errorf("stage instructions that name the issue or PRD must be redirected:\n%s", p)
	}
}
