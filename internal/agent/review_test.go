package agent

import (
	"strings"
	"testing"
)

func TestReviewPromptProvidesPinnedDiffAndPassData(t *testing.T) {
	p := ReviewPrompt(ReviewBrief{BaseHead: "abc1234", Head: "def5678", Passes: 2})
	for _, want := range []string{"Assigned diff command: `git diff --no-ext-diff --no-textconv --ignore-submodules=none abc1234...def5678 --`", "Review passes: 2", "Full suite revision: none"} {
		if !strings.Contains(p, want) {
			t.Errorf("review prompt missing %q", want)
		}
	}
}

func TestReviewPromptIncludesStageInstructions(t *testing.T) {
	b := ReviewBrief{BaseHead: "abc1234", Head: "def5678", Passes: 3, StageInstructions: "Look for regressions in callers."}
	p := ReviewPrompt(b)
	for _, want := range []string{"## Stage instructions", "Look for regressions in callers.", "Review passes: 3"} {
		if !strings.Contains(p, want) {
			t.Errorf("review prompt missing %q", want)
		}
	}
}
