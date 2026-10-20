package agent

import (
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func TestImplementationPromptAsksForCheckScopesAndTheFullSuiteReport(t *testing.T) {
	b := TaskBrief{Key: task.Key{Repo: "a/b", Number: 7}, Branch: "feat/7", BaseBranch: "main", Worktree: "/wt", FullSuite: true}
	p := ImplementationPrompt(b)
	for _, want := range []string{
		"## Validation",
		"Use the checks the repository documents",
		`"scope": "focused"`,
		`report the full suite in "fullSuite"`,
		"A name, extension, or directory alone is not evidence",
		"README, Markdown, reStructuredText, and .txt files can be test, build, or generated inputs",
		"a documentation-only file can live outside a docs directory",
		"If the evidence is absent or you are unsure, treat the path as an input",
		"A merge that brings in a behavior or validation input, or a path of uncertain effect, needs a new full run",
		"A merge that brings in only paths verified documentation-only leaves the earlier run valid",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	b.FullSuite = false
	if strings.Contains(ImplementationPrompt(b), "## Validation") {
		t.Error("a task without the gate was asked for a full-suite report")
	}
}

func TestFinalCheckMessageNamesTheGapAndTheChangedPaths(t *testing.T) {
	p := FinalCheckMessage(FinalCheck{Head: "c0ffee22", Reason: "the full result covers c0ffee1", Revision: "c0ffee11", Changed: []string{"docs/a.md", "main.go"}, PR: 912})
	for _, want := range []string{"## Final full check", "c0ffee2 has none: the full result covers c0ffee1", "since the full run on c0ffee1:\n\n- docs/a.md\n- main.go", "do not rerun the suite", "A new commit is reviewed again", `"pr_opened" for PR #912`, "unavailable"} {
		if !strings.Contains(p, want) {
			t.Errorf("message lacks %q:\n%s", want, p)
		}
	}
	if p := FinalCheckMessage(FinalCheck{Head: "c0ffee2", Reason: "r", Local: true}); !strings.Contains(p, `"review_ready"`) || !strings.Contains(p, "Do not push") {
		t.Errorf("local message:\n%s", p)
	}
}

func TestOpenPRPromptCarriesCheckScopesAndAnUnverifiedSuite(t *testing.T) {
	p := OpenPRPrompt(OpenPRBrief{Branch: "feat/7", BaseBranch: "main", Head: "abc1234", Title: "t", BodyFile: "/f",
		Validation: []task.Check{{Command: "npm run lint", Passed: true, Scope: task.FocusedCheck}}, Unverified: "no documented test suite"})
	for _, want := range []string{"- npm run lint (focused): passed true", "No full suite was run: no documented test suite"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
}
