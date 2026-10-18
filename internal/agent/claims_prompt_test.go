package agent

import (
	"strings"
	"testing"
)

func TestImplementationPromptWithClaimsAsksForAClaimPerCriterionAndKeepsThemPrivate(t *testing.T) {
	for _, local := range []bool{false, true} {
		b := localBrief()
		b.LocalReview, b.Claims = local, true
		p := ImplementationPrompt(b)
		for _, want := range []string{"## Claims", `"claims"`, "every acceptance criterion in the PRD", "evidence", "full updated set", "pull request title and description", "commit messages", "files in the worktree"} {
			if !strings.Contains(p, want) {
				t.Errorf("local %t: prompt lacks %q", local, want)
			}
		}
	}
}

func TestImplementationPromptWithoutClaimsIsUnchanged(t *testing.T) {
	b := localBrief()
	p := ImplementationPrompt(b)
	b.Claims = false
	if strings.Contains(p, "## Claims") || strings.Contains(p, `"claims"`) || p != ImplementationPrompt(b) {
		t.Fatal("a task without claim verification is asked for claims")
	}
}

func TestPlanningPromptDoesNotAskForClaims(t *testing.T) {
	b := localBrief()
	b.Claims, b.Planning = true, true
	if p := ImplementationPrompt(b); strings.Contains(p, "## Claims") {
		t.Fatal("a planning turn is asked for claims before any PRD exists")
	}
}

func TestClaimsMessageAsksForTheFullUpdatedSet(t *testing.T) {
	for _, want := range []string{`"claims"`, "full updated set", "acceptance criterion", "pull request title and description"} {
		if !strings.Contains(ClaimsMessage, want) {
			t.Errorf("ClaimsMessage lacks %q", want)
		}
	}
}

func TestMissingClaimsMessageAsksForClaimsWithoutChangingTheWork(t *testing.T) {
	p := MissingClaimsMessage(ReviewReady)
	for _, want := range []string{`"claims"`, `"review_ready"`, "Do not change"} {
		if !strings.Contains(p, want) {
			t.Errorf("message lacks %q:\n%s", want, p)
		}
	}
}
