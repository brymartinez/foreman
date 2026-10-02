package agent

import (
	"strings"
	"testing"
)

func TestConflictMessage(t *testing.T) {
	msg := ConflictMessage(Conflict{PR: 912, Base: "main", BaseHead: "ba5e001"})
	for _, want := range []string{
		"Pull request #912 now has merge conflicts with main.",
		"`git fetch origin main`",
		"`git merge origin/main`",
		"must include commit ba5e001",
		"do not force push",
		"`git merge --abort`",
		`"pr_opened" for PR #912`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
}

func TestImplementationPromptCarriesTheConflict(t *testing.T) {
	b := TaskBrief{Title: "t", Branch: "feat/1-x", BaseBranch: "main", PR: 912}
	if strings.Contains(ImplementationPrompt(b), "## Merge conflict") {
		t.Fatal("a prompt without a conflict has the section")
	}
	b.Conflict = &Conflict{PR: 912, Base: "main", BaseHead: "ba5e001"}
	p := ImplementationPrompt(b)
	if !strings.Contains(p, "## Merge conflict") || !strings.Contains(p, "`git merge origin/main`") {
		t.Errorf("prompt:\n%s", p)
	}
}
