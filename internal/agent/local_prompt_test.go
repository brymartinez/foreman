package agent

import (
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func TestLocalImplementationPromptKeepsTheSourceLinkOutOfThePullRequest(t *testing.T) {
	b := TaskBrief{Key: task.Key{Repo: "acme/web", Source: task.Local, ExternalID: "L-0000002A"}, Identifier: "L-0000002A", Title: "Fix cache", URL: "https://tracker.example/WEB-42", Branch: "feat/local", BaseBranch: "main", Worktree: "/wt", PRInstructions: "The pull request body must include Closes #N."}
	p := ImplementationPrompt(b)
	for _, want := range []string{"local issue L-0000002A", "GitHub hosts the pull request", "Put L-0000002A in the pull request title", "Do not put the issue's source link, private notes"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Contains(p, "https://tracker.example/WEB-42") {
		t.Errorf("prompt exposes the source link:\n%s", p)
	}
	if rule := prIssueRule(b); strings.Contains(rule, "tracker.example") || strings.Contains(rule, "Closes #N") && !strings.Contains(rule, "Do not") {
		t.Errorf("PR rule = %q", rule)
	}
}
