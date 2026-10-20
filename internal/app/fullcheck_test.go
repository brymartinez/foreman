package app

import (
	"context"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

func fullCheck(passed bool) task.Check {
	return task.Check{Command: "go vet ./... && go test ./...", Passed: passed, Scope: task.FullCheck}
}

func fullRun(revision string, docOnly ...task.DocOnlyPath) *agent.FullSuite {
	return &agent.FullSuite{Status: agent.FullSuiteRun, Revision: revision, DocOnly: docOnly}
}

func doc(path, evidence string) task.DocOnlyPath {
	return task.DocOnlyPath{Path: path, Evidence: evidence}
}

// evaluate checks report against head with the given changed paths between commits.
func evaluate(changed map[string][]string, checks []task.Check, report *agent.FullSuite, head string) *task.FullResult {
	f := newFixture()
	f.wt.changed = changed
	rec := &task.Record{Delegation: &task.Delegation{Worktree: "/wt/x"}}
	return f.app.evaluateFull(context.Background(), rec, agent.Outcome{Validation: checks, FullSuite: report}, head)
}

// Foreman checks only exact path coverage and nonempty evidence. Whether the evidence
// is sound is the implementer's judgment under the stage instructions.
func TestEvaluateFull(t *testing.T) {
	focused := task.Check{Command: "go test ./internal/app", Passed: true, Scope: task.FocusedCheck}
	cases := []struct {
		name    string
		changed map[string][]string
		checks  []task.Check
		report  *agent.FullSuite
		covered bool
		reason  string
	}{
		{name: "a full run on the head covers it",
			checks: []task.Check{focused, fullCheck(true)}, report: fullRun("head1"), covered: true},
		{name: "an unchanged result is reused when every later path has documentation-only evidence",
			changed: map[string][]string{"run1..head2": {"docs/guide.md"}},
			checks:  []task.Check{fullCheck(true)}, report: fullRun("run1", doc("docs/guide.md", "docs/ is not read by tests, build or CI (Makefile, ci.yml)")), covered: true},
		{name: "a later path with no entry makes the result stale",
			changed: map[string][]string{"run1..head2": {"docs/guide.md", "internal/app/run.go"}},
			checks:  []task.Check{fullCheck(true)}, report: fullRun("run1", doc("docs/guide.md", "not read by any check")), reason: "internal/app/run.go"},
		{name: "a later path with empty evidence makes the result stale",
			changed: map[string][]string{"run1..head2": {"docs/guide.md"}},
			checks:  []task.Check{fullCheck(true)}, report: fullRun("run1", doc("docs/guide.md", "  ")), reason: "docs/guide.md"},
		{name: "a documentation-looking name without evidence is stale",
			changed: map[string][]string{"run1..head2": {"README.md", "testdata/golden.txt"}},
			checks:  []task.Check{fullCheck(true)}, report: fullRun("run1"), reason: "README.md, testdata/golden.txt"},
		{name: "a verified documentation-only path outside a docs directory is covered",
			changed: map[string][]string{"run1..head2": {"internal/app/NOTES"}},
			checks:  []task.Check{fullCheck(true)}, report: fullRun("run1", doc("internal/app/NOTES", "go build and go test ignore files without a .go suffix; no test reads it")), covered: true},
		{name: "a failed full check does not cover the head",
			checks: []task.Check{fullCheck(false)}, report: fullRun("head1"), reason: "full checks failed"},
		{name: "a report with no full check does not cover the head",
			checks: []task.Check{focused}, report: fullRun("head1"), reason: "no full check"},
		{name: "a revision that is not an ancestor does not cover the head",
			checks: []task.Check{fullCheck(true)}, report: fullRun("elsewhere"), reason: "not an ancestor"},
		{name: "a base merge that brings in a source file makes the result stale",
			changed: map[string][]string{"run1..merge1": {"internal/github/gh.go"}},
			checks:  []task.Check{fullCheck(true)}, report: fullRun("run1"), reason: "internal/github/gh.go"},
		{name: "a base merge that brings in a build input makes the result stale",
			changed: map[string][]string{"run1..merge1": {"Makefile", "docs/guide.md"}},
			checks:  []task.Check{fullCheck(true)}, report: fullRun("run1", doc("docs/guide.md", "not read by any check")), reason: "Makefile"},
		{name: "a base merge that brings in only verified documentation-only paths keeps the result reusable",
			changed: map[string][]string{"run1..merge1": {"docs/guide.md", "CHANGELOG.md"}},
			checks:  []task.Check{fullCheck(true)},
			report:  fullRun("run1", doc("docs/guide.md", "docs/ is not read by tests, build or CI"), doc("CHANGELOG.md", "no test, build or generator reads CHANGELOG.md")), covered: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			head := "head1"
			for pair := range c.changed {
				_, head, _ = strings.Cut(pair, "..")
			}
			got := evaluate(c.changed, c.checks, c.report, head)
			if got == nil || got.Covered != c.covered || got.Head != head || got.Covers(head) != c.covered {
				t.Fatalf("result = %+v, want covered %v at %s", got, c.covered, head)
			}
			if !strings.Contains(got.Reason, c.reason) || (c.covered && got.Reason != "") {
				t.Errorf("reason = %q, want it to name %q", got.Reason, c.reason)
			}
			for _, check := range got.Checks {
				if check.Scope != task.FullCheck {
					t.Errorf("stored a %s check as part of the full result", check.Scope)
				}
			}
		})
	}
}

// The same paths are covered or stale by the evidence alone, never by their names.
func TestEvaluateFullFollowsTheEvidenceNotThePathName(t *testing.T) {
	changed := map[string][]string{"run1..head2": {"README.md"}}
	with := evaluate(changed, []task.Check{fullCheck(true)}, fullRun("run1", doc("README.md", "no test or build reads README.md")), "head2")
	without := evaluate(changed, []task.Check{fullCheck(true)}, fullRun("run1", doc("README.md", "")), "head2")
	if !with.Covered || without.Covered {
		t.Fatalf("with evidence %+v, without %+v", with, without)
	}
}

func TestEvaluateFullRecordsAnUnavailableSuiteWithItsExplanation(t *testing.T) {
	got := evaluate(nil, []task.Check{{Command: "npm run lint", Passed: true, Scope: task.FocusedCheck}},
		&agent.FullSuite{Status: agent.FullSuiteUnavailable, Explanation: "the repository documents no test suite"}, "head1")
	if got.Unverified != "the repository documents no test suite" || got.Covered || !got.Covers("head1") || !got.Covers("head2") {
		t.Fatalf("result = %+v", got)
	}
}

func TestEvaluateFullLeavesATurnWithoutAReport(t *testing.T) {
	if got := evaluate(nil, []task.Check{fullCheck(true)}, nil, "head1"); got != nil {
		t.Fatalf("result = %+v", got)
	}
}

func TestAStoredResultStopsCoveringAMovedHead(t *testing.T) {
	stored := &task.FullResult{Head: "head1", Revision: "head1", Covered: true}
	if !stored.Covers("head1") || stored.Covers("head2") || (*task.FullResult)(nil).Covers("head1") {
		t.Fatalf("covers = %v at head1, %v at head2", stored.Covers("head1"), stored.Covers("head2"))
	}
}

// evaluateWith checks report against head on a task whose stored result is stored.
func evaluateWith(stored *task.FullResult, changed map[string][]string, checks []task.Check, report *agent.FullSuite, head string) *task.FullResult {
	f := newFixture()
	f.wt.changed = changed
	rec := &task.Record{Delegation: &task.Delegation{Worktree: "/wt/x"}, FullResult: stored}
	return f.app.evaluateFull(context.Background(), rec, agent.Outcome{Validation: checks, FullSuite: report}, head)
}

// A turn that reuses an earlier run lists only what it ran. The run's full checks come
// from the result Foreman recorded for that revision.
func TestEvaluateFullTakesAReusedRunsChecksFromTheRecordedResult(t *testing.T) {
	focused := []task.Check{{Command: "go test ./internal/app", Passed: true, Scope: task.FocusedCheck}}
	changed := map[string][]string{"commit1..commit2": {"docs/guide.md"}}
	report := fullRun("commit1", doc("docs/guide.md", "no test, build, or CI step reads docs/"))
	t.Run("a passing run on the same revision is reused", func(t *testing.T) {
		stored := &task.FullResult{Head: "commit1", Revision: "commit1", Checks: []task.Check{fullCheck(true)}, Covered: true}
		got := evaluateWith(stored, changed, focused, report, "commit2")
		if !got.Covered || len(got.Checks) != 1 || got.Checks[0] != fullCheck(true) {
			t.Fatalf("result = %+v", got)
		}
	})
	t.Run("a short revision matches the recorded one", func(t *testing.T) {
		stored := &task.FullResult{Head: "commit1abcdef", Revision: "commit1abcdef", Checks: []task.Check{fullCheck(true)}, Covered: true}
		got := evaluateWith(stored, map[string][]string{"commit1..commit2": {"docs/guide.md"}}, focused, report, "commit2")
		if !got.Covered {
			t.Fatalf("result = %+v", got)
		}
	})
	t.Run("a failed run is not reused", func(t *testing.T) {
		stored := &task.FullResult{Head: "commit1", Revision: "commit1", Checks: []task.Check{fullCheck(false)}, Reason: "full checks failed"}
		if got := evaluateWith(stored, changed, focused, report, "commit2"); got.Covered || !strings.Contains(got.Reason, "full checks failed") {
			t.Fatalf("result = %+v", got)
		}
	})
	t.Run("a revision Foreman has no run for names no full check", func(t *testing.T) {
		stored := &task.FullResult{Head: "commit0", Revision: "commit0", Checks: []task.Check{fullCheck(true)}, Covered: true}
		if got := evaluateWith(stored, changed, focused, report, "commit2"); got.Covered || !strings.Contains(got.Reason, "no full check") {
			t.Fatalf("result = %+v", got)
		}
	})
}

// The rules a resume carries follow what the turn must hand in: a full run for the
// first review, the final-check instructions for a final check, focused checks otherwise.
func TestResumeRulesFollowWhatTheTurnMustHandIn(t *testing.T) {
	gated := func() *task.Record {
		return &task.Record{Delegation: &task.Delegation{FullSuiteGate: true},
			Decisions: []task.Decision{{Question: task.Question{Issue: "Retry 4xx?"}, Answer: "A"}}}
	}
	final := &agent.FinalCheck{Head: "c0ffee2", Reason: "no full-suite result was reported", Local: true}

	t.Run("an initial turn resumed with an answer runs the full suite", func(t *testing.T) {
		got := fullSuiteResume(gated(), turnRequest{}, "answers")
		if !strings.Contains(got, "Run the full suite once before you hand the change to its first review") || strings.Contains(got, "Do not rerun the full suite") {
			t.Fatalf("resume:\n%s", got)
		}
	})
	t.Run("a final check resumed with an answer keeps its instructions", func(t *testing.T) {
		got := fullSuiteResume(gated(), turnRequest{finalCheck: final}, "answers")
		if !strings.Contains(got, "## Final full check") || strings.Contains(got, "Do not rerun the full suite") {
			t.Fatalf("resume:\n%s", got)
		}
	})
	t.Run("a final check without answers is not repeated", func(t *testing.T) {
		msg := agent.FinalCheckMessage(*final)
		if got := fullSuiteResume(gated(), turnRequest{finalCheck: final}, msg); got != msg {
			t.Fatalf("resume:\n%s", got)
		}
	})
	t.Run("a review fix runs focused checks", func(t *testing.T) {
		rec := gated()
		rec.Reviews = []task.Review{{Round: 1}}
		got := fullSuiteResume(rec, turnRequest{findings: &rec.Reviews[0]}, "findings")
		if !strings.Contains(got, "Do not rerun the full suite in this turn") || strings.Contains(got, "Run the full suite once before") {
			t.Fatalf("resume:\n%s", got)
		}
	})
	t.Run("a task without the gate gets no rules", func(t *testing.T) {
		rec := gated()
		rec.Delegation.FullSuiteGate = false
		if got := fullSuiteResume(rec, turnRequest{}, "answers"); got != "answers" {
			t.Fatalf("resume:\n%s", got)
		}
	})
}
