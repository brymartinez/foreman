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

func TestPromptAfterResetOmitsThePriorPR(t *testing.T) {
	p := ImplementationPrompt(priorBrief(false, "closed"))
	for _, excluded := range []string{"## Prior pull request", "https://github.com/a/b/pull/12", "Caches every request.", "Only cache GETs."} {
		if strings.Contains(p, excluded) {
			t.Errorf("prompt leaked %q", excluded)
		}
	}
}

func TestPromptAfterResetOnAnOpenPROmitsItsHistory(t *testing.T) {
	p := ImplementationPrompt(priorBrief(true, "open"))
	if strings.Contains(p, "## Prior pull request") || strings.Contains(p, "Continue updating pull request #12") {
		t.Errorf("prompt = %s", p)
	}
}

func TestPromptWithoutAResetHasNoPriorPR(t *testing.T) {
	b := priorBrief(false, "closed")
	b.PriorPR = nil
	if strings.Contains(ImplementationPrompt(b), "Prior pull request") {
		t.Error("prior PR section without a reset")
	}
}
