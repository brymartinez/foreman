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

func TestSelfReviewDoesNotReceiveTaskContext(t *testing.T) {
	b := claimedReviewBrief()
	b.Self = true
	b.PreviousHead = "old-head"
	b.PreviousFindings = []task.Finding{{Summary: "EARLIER-FINDING"}}
	b.Decisions = []task.Decision{{Question: task.Question{Issue: "Q"}, Answer: "DECISION"}}
	b.Validation = []task.Check{{Command: "VALIDATION"}}
	b.FullResult = &task.FullResult{Revision: "FULL-REVISION"}
	p := ReviewPrompt(b)
	for _, leak := range []string{"AC1.", "only 5xx is retried", "retry.go:40", "DECISION", "VALIDATION", "FULL-REVISION"} {
		if strings.Contains(p, leak) {
			t.Errorf("self-review leaked %q", leak)
		}
	}
}

func TestSelfReviewFollowupStartsFromTheFixDiff(t *testing.T) {
	b := reviewBrief()
	b.Self = true
	b.PreviousHead = "abcdef1234567"
	b.PreviousFindings = []task.Finding{{File: "queue.go", Line: 12, Summary: "Lease expiry is not checked", Detail: "A stale worker can publish."}}
	p := ReviewPrompt(b)
	if !strings.Contains(p, "git diff abcdef1234567..HEAD") || !strings.Contains(p, "Lease expiry is not checked") {
		t.Fatalf("followup lost fix scope: %s", p)
	}
	if strings.Contains(p, "Read the complete diff once.") {
		t.Fatal("followup repeats a full branch review")
	}
}

func TestScopedReviewUsesLiteralAssignedPaths(t *testing.T) {
	b := reviewBrief()
	b.Self = true
	b.Head = "new-head"
	b.BaseHead = "base-head"
	b.Area = &task.ReviewArea{Name: "storage", Paths: []string{"db/save.go", "db/a'$(secret).go"}}
	p := ReviewPrompt(b)
	for _, want := range []string{"Assigned review area: storage", "Inspect every assigned file", "git diff base-head...new-head -- ':(literal)db/save.go' ':(literal)db/a'\\''$(secret).go'", "Collect all verified findings", "If a command is denied"} {
		if !strings.Contains(p, want) {
			t.Errorf("scoped prompt missing %q", want)
		}
	}
	if strings.Contains(p, "Read the complete diff once.") {
		t.Fatal("scoped review requested the full branch")
	}
}
