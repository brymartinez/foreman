package agent

import (
	"strings"
	"testing"
)

func TestBothReviewPromptsRequireTheCompletePinnedDiff(t *testing.T) {
	for _, self := range []bool{false, true} {
		b := ReviewBrief{BaseHead: "abc1234", Head: "def5678", Self: self}
		p := ReviewPrompt(b)
		role := "competing reviewer"
		if self {
			role = "self-reviewer"
		}
		for _, want := range []string{role, "git diff --no-ext-diff --no-textconv --ignore-submodules=none abc1234...def5678 --", "read-only", "Review every change", "blocking limitation"} {
			if !strings.Contains(p, want) {
				t.Errorf("%s prompt missing %q", role, want)
			}
		}
	}
}

func TestReviewMethodsCannotOverrideScope(t *testing.T) {
	b := ReviewBrief{BaseHead: "abc1234", Head: "def5678", Passes: 3, StageInstructions: "Look for regressions in callers."}
	p := ReviewPrompt(b)
	for _, want := range []string{"Look for regressions in callers.", "where they agree with the scope above", "Read the complete diff 3 times"} {
		if !strings.Contains(p, want) {
			t.Errorf("review prompt missing %q", want)
		}
	}
}
