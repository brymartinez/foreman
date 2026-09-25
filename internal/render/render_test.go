package render

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

func TestInboxSectionsAndHiddenCount(t *testing.T) {
	k := func(n int) task.Key { return task.Key{Repo: "ridezoomo/armada-backend", Number: n} }
	var buf bytes.Buffer
	Inbox(&buf, app.InboxReport{
		New:     []app.InboxEntry{{Item: app.InboxItem{Key: k(825), Title: "Provider retries"}, Changes: []string{"assigned to you"}}},
		Changed: []app.InboxEntry{{Item: app.InboxItem{Key: k(812), Title: "Pagination"}, Changes: []string{"closed"}}},
		NeedsDecision: []*task.Record{{Key: k(830), Title: "Event pagination",
			Decisions: []task.Decision{{Question: task.Question{Issue: "Should retries apply to 4xx?"}}}}},
		Ready: []*task.Record{{Key: k(790), Title: "Provider retries",
			Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Reviewer: task.Codex, PR: 912, PRURL: "https://github.com/ridezoomo/armada-backend/pull/912"},
			Validation: []task.Check{{Command: "go test ./...", Passed: true}, {Command: "go vet ./...", Passed: true}},
			CI:         &task.CIStatus{State: task.CIPassing},
			Reviews:    []task.Review{{Round: 1}, {Round: 2, Clean: true}},
			Decisions:  []task.Decision{{Answer: "Keep retries limited to 5xx."}, {Answer: ""}}}},
		Unchanged: 17,
	})
	want := `NEEDS DECISION
  #830  Event pagination
        ridezoomo/armada-backend · Should retries apply to 4xx?

READY FOR YOU
  #790  Provider retries
        ridezoomo/armada-backend · Claude → Codex review
        PR #912 · https://github.com/ridezoomo/armada-backend/pull/912

        ✓ go test ./...
        ✓ go vet ./...
        ✓ CI
        ✓ competing review

        Decisions:
        - Keep retries limited to 5xx.

CHANGED
  #812  Pagination
        ridezoomo/armada-backend · closed

NEW
  #825  Provider retries
        ridezoomo/armada-backend · assigned to you

NO CHANGE
  17 tracked items hidden
`
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestInboxEmpty(t *testing.T) {
	var buf bytes.Buffer
	Inbox(&buf, app.InboxReport{Unchanged: 1})
	if buf.String() != "Nothing new.\n\nNO CHANGE\n  1 tracked item hidden\n" {
		t.Errorf("got %q", buf.String())
	}
}

func TestStatus(t *testing.T) {
	var buf bytes.Buffer
	Status(&buf, []*task.Record{{
		Key: task.Key{Repo: "ridezoomo/armada-backend", Number: 831}, Title: "Provider retries", State: task.Reviewing,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Reviewer: task.Codex, PR: 912},
	}})
	want := "#831   Claude  REVIEWING\n       Provider retries · ridezoomo/armada-backend\n       PR #912 · reviewer: Codex\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
	buf.Reset()
	Status(&buf, []*task.Record{{
		Key: task.Key{Repo: "ridezoomo/armada-backend", Number: 831}, Title: "Provider retries", State: task.Reviewing,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Reviewer: task.Codex, PR: 912},
		Reviews:    []task.Review{{Round: 1}, {Round: 2, Clean: true}},
	}})
	want = "#831   Claude  REVIEWING\n       Provider retries · ridezoomo/armada-backend\n       PR #912 · reviewer: Codex · review ✓\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
	for _, tc := range []struct {
		ci   task.CIStatus
		want string
	}{
		{task.CIStatus{State: task.CINone}, " · CI ✓ · review ✓"},
		{task.CIStatus{State: task.CIPending}, " · CI pending · review ✓"},
		{task.CIStatus{State: task.CIFailing, Failing: []task.CheckRun{{Name: "test"}, {Name: "lint"}}}, " · CI ✗ test, lint · review ✓"},
	} {
		buf.Reset()
		Status(&buf, []*task.Record{{
			Key: task.Key{Repo: "a/b", Number: 820}, State: task.ReadyForHuman, CI: &tc.ci,
			Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Codex}, Reviewer: task.Claude, PR: 905},
			Reviews:    []task.Review{{Round: 1, Clean: true}},
		}})
		if !strings.Contains(buf.String(), "PR #905 · reviewer: Claude"+tc.want+"\n") {
			t.Errorf("%s: got %q", tc.ci.State, buf.String())
		}
	}
}

func TestShowCI(t *testing.T) {
	var buf bytes.Buffer
	Show(&buf, &task.Record{
		Key: task.Key{Repo: "a/b", Number: 825}, State: task.ReadyForHuman,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Reviewer: task.Codex, PR: 912},
		CI:         &task.CIStatus{State: task.CIFailing, Failing: []task.CheckRun{{Name: "test", Link: "https://ci/1"}}},
	})
	for _, want := range []string{"CI            failing · checked ", "Failing checks\n  ✗ test https://ci/1\n"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q in:\n%s", want, buf.String())
		}
	}
}

func TestShowUndelegated(t *testing.T) {
	var buf bytes.Buffer
	Show(&buf, &task.Record{Key: task.Key{Repo: "a/b", Number: 1}, Title: "T", URL: "u", State: task.Discovered})
	for _, s := range []string{"#1 T\na/b · u", "State  DISCOVERED", "Agent  not delegated"} {
		if !strings.Contains(buf.String(), s) {
			t.Errorf("missing %q in:\n%s", s, buf.String())
		}
	}
}

func TestShowPendingDecisionUsesBlockedFormat(t *testing.T) {
	var buf bytes.Buffer
	Show(&buf, &task.Record{
		Key: task.Key{Repo: "ridezoomo/armada-backend", Number: 825}, Title: "Provider retries", State: task.NeedsDecision,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Reviewer: task.Codex},
		Decisions: []task.Decision{
			{Question: task.Question{Issue: "Earlier?"}, Answer: "A. yes"},
			{Question: task.Question{
				Issue:   "Should retries apply to 4xx responses?",
				Context: "Current code retries 5xx only.\nIssue text says \"retry failed requests.\"",
				Options: []task.Option{
					{Label: "A", Description: "5xx only — preserves current semantics"},
					{Label: "B", Description: "4xx + 5xx — broader behavior change"},
				},
				Recommendation: "A",
				ImpactIfWrong:  "Could cause unnecessary repeated client errors.",
			}},
		},
	})
	want := `BLOCKED: Need decision

Issue:
Should retries apply to 4xx responses?

Context:
Current code retries 5xx only.
Issue text says "retry failed requests."

Options:
A. 5xx only — preserves current semantics
B. 4xx + 5xx — broader behavior change

Recommendation:
A

Impact if wrong:
Could cause unnecessary repeated client errors.

Answer with: foreman respond ridezoomo/armada-backend#825
`
	out := buf.String()
	if !strings.HasSuffix(out, want) {
		t.Errorf("got:\n%s", out)
	}
	if !strings.Contains(out, "Decisions\n  Q: Earlier?\n  A: A. yes\n") {
		t.Errorf("answered decisions missing:\n%s", out)
	}
}

func TestShowPRAndValidation(t *testing.T) {
	var buf bytes.Buffer
	Show(&buf, &task.Record{
		Key: task.Key{Repo: "a/b", Number: 825}, Title: "T", State: task.Reviewing,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Reviewer: task.Codex, PR: 912, PRURL: "https://github.com/a/b/pull/912"},
		Validation: []task.Check{{Command: "go test ./...", Passed: true}, {Command: "golangci-lint run", Passed: false}},
	})
	for _, want := range []string{"PR            #912 · https://github.com/a/b/pull/912", "Validation (reported by the agent)\n  ✓ go test ./...\n  ✗ golangci-lint run\n"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q in:\n%s", want, buf.String())
		}
	}
}

func TestShowLatestReview(t *testing.T) {
	var buf bytes.Buffer
	Show(&buf, &task.Record{
		Key: task.Key{Repo: "a/b", Number: 1}, State: task.Working,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Reviewer: task.Codex, PR: 912},
		Reviews: []task.Review{{Round: 1, Clean: true}, {Round: 2, Reviewer: task.Codex, Summary: "one bug",
			Findings: []task.Finding{{Severity: "blocking", File: "retry.go", Line: 40, Summary: "4xx retried"}}}},
	})
	for _, want := range []string{"Latest review (round 2 of 2, Codex,", "changes requested\n  one bug\n  - [blocking] retry.go:40 4xx retried\n"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q in:\n%s", want, buf.String())
		}
	}
}

func TestCleaned(t *testing.T) {
	rec := func(n int) *task.Record {
		return &task.Record{Key: task.Key{Repo: "a/b", Number: n}, Delegation: &task.Delegation{Worktree: fmt.Sprintf("/wt/%d", n), Branch: fmt.Sprintf("agent/%d-x", n)}}
	}
	var buf bytes.Buffer
	Cleaned(&buf, []app.CleanResult{{Rec: rec(1), BranchDeleted: true}, {Rec: rec(2)}, {Rec: rec(3), Err: errors.New("contains modified or untracked files")}}, false)
	want := `  #1  removed /wt/1
        branch deleted (PR merged) agent/1-x
  #2  removed /wt/2
        branch agent/2-x kept
  #3  kept /wt/3
        contains modified or untracked files
`
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
	buf.Reset()
	Cleaned(&buf, nil, true)
	if buf.String() != "No worktrees to clean.\n" {
		t.Errorf("got %q", buf.String())
	}
}

func TestReadyShowsFailingCIForTheDeveloper(t *testing.T) {
	var buf bytes.Buffer
	Inbox(&buf, app.InboxReport{Ready: []*task.Record{{Key: task.Key{Repo: "a/b", Number: 825}, Title: "T",
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Reviewer: task.Codex, PR: 912},
		CI:         &task.CIStatus{State: task.CIFailing, Failing: []task.CheckRun{{Name: "test", Link: "https://ci/1"}, {Name: "lint"}}},
		Reviews:    []task.Review{{Round: 1, Clean: true}}}}})
	if want := "        ✗ CI: test, lint\n            test  https://ci/1\n        ✓ competing review\n"; !strings.Contains(buf.String(), want) {
		t.Errorf("missing %q in:\n%s", want, buf.String())
	}
}
