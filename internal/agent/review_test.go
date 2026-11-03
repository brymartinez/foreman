package agent

import (
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func reviewBrief() ReviewBrief {
	return ReviewBrief{Key: task.Key{Repo: "a/b", Number: 825},
		Branch: "feat/825-provider-retries", BaseBranch: "main", Plan: "PLAN-REQUIREMENT"}
}

func TestCompetingReviewUsesTaskContextAndDiff(t *testing.T) {
	b := reviewBrief()
	p := ReviewPrompt(b)
	for _, leaked := range []string{"PR-DESCRIPTION-SECRET", "TRIAGE-SECRET", "COMMENT-SECRET", "EARLIER-FINDING-SECRET"} {
		if strings.Contains(p, leaked) {
			t.Errorf("competing review leaked %q", leaked)
		}
	}
	for _, want := range []string{"PLAN-REQUIREMENT", "competing reviewer", "git diff origin/main...HEAD", "read-only", "file diff", "Set limitations to null", "Use Read to inspect cited files and line ranges", "Grep to find symbols and fixture setup", "Glob to locate test files", "Do not hand verification commands to the developer"} {
		if !strings.Contains(p, want) {
			t.Errorf("competing review missing %q", want)
		}
	}
}

func TestSelfReviewUsesOnlyCodeChanges(t *testing.T) {
	b := reviewBrief()
	b.Self = true
	p := ReviewPrompt(b)
	if strings.Contains(p, "PLAN-REQUIREMENT") || !strings.Contains(p, "git diff origin/main...HEAD") {
		t.Fatalf("self-review leaked plan or lacks diff: %s", p)
	}
	for _, leaked := range []string{"PR-DESCRIPTION-SECRET", "TRIAGE-SECRET", "COMMENT-SECRET", "EARLIER-FINDING-SECRET"} {
		if strings.Contains(p, leaked) {
			t.Errorf("self-review leaked %q", leaked)
		}
	}
}

func TestReviewMethodsCannotOverrideScope(t *testing.T) {
	b := reviewBrief()
	b.StageInstructions = "Look for regressions in callers."
	b.Passes = 3
	p := ReviewPrompt(b)
	for _, want := range []string{"Look for regressions in callers.", "where they agree with the scope above", "Read the complete diff 3 times"} {
		if !strings.Contains(p, want) {
			t.Errorf("review prompt missing %q", want)
		}
	}
}

func TestSelfReviewDoesNotReceiveTaskOrEarlierReviewContext(t *testing.T) {
	b := claimedReviewBrief()
	b.Self = true
	b.PreviousHead = "old-head"
	b.PreviousFindings = []task.Finding{{Summary: "EARLIER-FINDING"}}
	b.Decisions = []task.Decision{{Question: task.Question{Issue: "Q"}, Answer: "DECISION"}}
	b.Validation = []task.Check{{Command: "VALIDATION"}}
	b.FullResult = &task.FullResult{Revision: "FULL-REVISION"}
	p := ReviewPrompt(b)
	for _, leak := range []string{"AC1.", "only 5xx is retried", "retry.go:40", "EARLIER-FINDING", "DECISION", "VALIDATION", "FULL-REVISION", "Start with `git diff old-head"} {
		if strings.Contains(p, leak) {
			t.Errorf("self-review leaked %q", leak)
		}
	}
}
