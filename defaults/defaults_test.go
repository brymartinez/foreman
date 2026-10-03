// Package defaults holds the stage files that `make config` installs. These tests lock
// the parts of the default text that foreman's behavior depends on.
package defaults

import (
	"os"
	"strings"
	"testing"
)

func readStage(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("stages/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDefaultPlanStageKeepsFourDocumentsAndOmitsLineLevelDetail(t *testing.T) {
	plan := readStage(t, "plan.md")
	for _, want := range []string{
		"## Analysis", "## PRD", "## Design", "## Implementation plan",
		"Refer to an earlier comment instead of repeating it",
		"No line numbers",
		"no commit messages",
		"Do not list every test by name",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan.md lacks %q", want)
		}
	}
	for _, banned := range []string{"path:lines","Name the functions, types, and test cases", "commit boundary"} {
		if strings.Contains(plan, banned) {
			t.Errorf("plan.md still asks for line-level detail: %q", banned)
		}
	}
}

func TestDefaultPRStageHasProblemFixRiskAndNoHowToTest(t *testing.T) {
	pr := readStage(t, "pr.md")
	for _, want := range []string{"`## Problem`", "`## Fix`", "`## Risk`", "`## Review notes`", "`Closes #N`", "Leave out test commands, test results, reviewer test steps"} {
		if !strings.Contains(pr, want) {
			t.Errorf("pr.md lacks %q", want)
		}
	}
	if strings.Contains(pr, "How to test") {
		t.Error("pr.md asks for a How to test section")
	}
}
