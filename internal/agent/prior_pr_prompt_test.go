package agent

import (
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func priorBrief(cont bool, state string) TaskBrief {
	return TaskBrief{
		Key: task.Key{Repo: "a/b", Number: 3}, Title: "Fix cache", Branch: "feat/3-fix-cache", BaseBranch: "main", Worktree: "/wt",
		PriorPR: &PriorPR{
			Number: 12, URL: "https://github.com/a/b/pull/12", Title: "Cache the wrong thing", State: state, Body: "Caches every request.", Continue: cont,
			Comments: []FeedbackItem{{Kind: task.ReviewComment, Author: "dev", Body: "Only cache GETs.", Path: "cache.go", Line: 9}},
		},
	}
}

func TestPromptAfterResetCarriesThePriorPRAsContext(t *testing.T) {
	p := ImplementationPrompt(priorBrief(false, "closed"))
	for _, want := range []string{"## Prior pull request", "#12", "https://github.com/a/b/pull/12", "Cache the wrong thing", "closed", "> Caches every request.", "dev", "cache.go", "> Only cache GETs.", "Do not reopen it or push to its branch"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestPromptAfterResetOnAnOpenPRContinuesIt(t *testing.T) {
	p := ImplementationPrompt(priorBrief(true, "open"))
	if !strings.Contains(p, "## Prior pull request") || !strings.Contains(p, "Continue updating pull request #12") {
		t.Errorf("prompt = %s", p)
	}
	if strings.Contains(p, "Do not reopen it") {
		t.Error("an open PR the run continues must not be ruled out")
	}
}

func TestPromptWithoutAResetHasNoPriorPR(t *testing.T) {
	b := priorBrief(false, "closed")
	b.PriorPR = nil
	if strings.Contains(ImplementationPrompt(b), "Prior pull request") {
		t.Error("prior PR section without a reset")
	}
}
