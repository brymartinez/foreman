package agent

import (
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func TestLinearImplementationPromptUsesLinearIssueAndGitHubPullRequestRules(t *testing.T) {
	b := TaskBrief{Key: task.Key{Repo: "acme/web", Source: task.Linear, ExternalID: "uuid-42"}, Identifier: "WEB-42", Title: "Fix cache", URL: "https://linear.app/acme/issue/WEB-42", Branch: "feat/WEB-42", BaseBranch: "main", Worktree: "/wt", PRInstructions: "The pull request body must include Closes #N."}
	p := ImplementationPrompt(b)
	for _, want := range []string{"Linear issue WEB-42", "GitHub hosts the pull request", "WEB-42 in the pull request title", "https://linear.app/acme/issue/WEB-42 in the pull request body"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Contains(p, "Closes #0") || !strings.Contains(p, "Do not add a GitHub Closes #N line") {
		t.Errorf("Linear PR rule = %q", p)
	}
}
