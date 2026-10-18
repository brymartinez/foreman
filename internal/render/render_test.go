package render

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

func TestTaskRowsCarryStateAndBadges(t *testing.T) {
	rows := TaskRows(&task.Record{
		Key: task.Key{Repo: "testorg/example", Number: 831}, Title: "Provider retries", State: task.WaitingForCI,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Reviewer: task.Codex, PR: 912},
		CI:         &task.CIStatus{State: task.CIPassing},
		Reviews:    []task.Review{{Round: 1, Clean: true}},
	})
	want := []string{
		"testorg/example#831       Claude  WAITING_FOR_CI",
		"Provider retries",
		"PR #912 · reviewer: Codex · CI ✓ · review ✓",
	}
	if !slices.Equal(rows, want) {
		t.Fatalf("TaskRows = %q\nwant %q", rows, want)
	}
	rows = TaskRows(&task.Record{
		Key: task.Key{Repo: "a/b", Number: 7}, Title: "No PR yet", State: task.Implementing,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Codex}, Reviewer: task.Claude},
	})
	if len(rows) != 2 {
		t.Fatalf("TaskRows without a PR = %q; want two rows", rows)
	}
}

func TestLinearDelegatedTaskShowsItsIdentifier(t *testing.T) {
	rec := &task.Record{Key: task.Key{Repo: "acme/linear", Source: task.Linear, ExternalID: "uuid-7"}, Identifier: "LIN-7", State: task.Implementing, Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}}}
	if identity, _, _ := TaskHead(rec); identity != "acme/linear · LIN-7" {
		t.Fatalf("task identity = %q", identity)
	}
}

func TestTaskPRKeepsTheStatesInItsShortForms(t *testing.T) {
	// The dashboard falls back to the short forms when its pane is too narrow for the
	// whole row. They may drop the failing checks' names and the review round, but
	// not the result itself.
	for _, tc := range []struct {
		ci                        task.CIStatus
		reviews                   []task.Review
		ciState, review, reviewIn string
	}{
		{task.CIStatus{State: task.CIPassing}, []task.Review{{Round: 1, Clean: true}}, "CI ✓", "review ✓", "review ✓"},
		{task.CIStatus{State: task.CIPending}, nil, "CI pending", "", ""},
		{
			task.CIStatus{State: task.CIFailing, Failing: []task.CheckRun{{Name: "test"}, {Name: "lint"}}},
			[]task.Review{{Round: 2, Findings: []task.Finding{{}, {}, {}}}},
			"CI ✗", "review ✗ 3", "review round 2: 3 findings",
		},
		{
			task.CIStatus{State: task.CIPending},
			[]task.Review{{Round: 1, Kind: task.SelfReview, Findings: []task.Finding{{}}}},
			"CI pending", "self-review ✗ 1", "self-review round 1: 1 finding",
		},
		{task.CIStatus{State: task.CIPending}, []task.Review{{Round: 2, Kind: task.SelfReview, Clean: true}}, "CI pending", "self-review ✓", "self-review ✓"},
	} {
		s := TaskPR(&task.Record{
			Key: task.Key{Repo: "a/b", Number: 831}, State: task.WaitingForCI, CI: &tc.ci, Reviews: tc.reviews,
			Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Reviewer: task.Codex, PR: 912},
		})
		if s.PR != "PR #912" || s.Reviewer != "reviewer: Codex" {
			t.Errorf("%s: PR = %q reviewer = %q", tc.ci.State, s.PR, s.Reviewer)
		}
		if s.CIState != tc.ciState || !strings.HasPrefix(s.CI, tc.ciState) {
			t.Errorf("%s: CI = %q short = %q; want the short form to keep the state", tc.ci.State, s.CI, s.CIState)
		}
		if s.ReviewState != tc.review || s.Review != tc.reviewIn {
			t.Errorf("%s: review = %q short = %q; want %q and %q", tc.ci.State, s.Review, s.ReviewState, tc.reviewIn, tc.review)
		}
	}
}

func TestJoinFactsSkipsTheOnesThatAreMissing(t *testing.T) {
	if got := JoinFacts("PR #912", "", "CI ✓", ""); got != "PR #912 · CI ✓" {
		t.Errorf("JoinFacts = %q", got)
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

func TestShowFeedback(t *testing.T) {
	var buf bytes.Buffer
	Show(&buf, &task.Record{
		Key: task.Key{Repo: "a/b", Number: 825}, State: task.Reviewed,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Reviewer: task.Codex, PR: 912},
		Feedback: []task.Feedback{
			{Kind: task.IssueComment, ID: 1, Author: "dev", URL: "https://x/1", HandledAt: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)},
			{Kind: task.ReviewComment, ID: 2, Author: "bot", URL: "https://x/2"},
		},
	})
	for _, want := range []string{"Pull request comments\n", "dev · answered ", "https://x/1\n", "bot · waiting · https://x/2\n"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q in:\n%s", want, buf.String())
		}
	}
}

func TestShowClaudeTranscript(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/cfg")
	var buf bytes.Buffer
	Show(&buf, &task.Record{
		Key: task.Key{Repo: "a/b", Number: 7}, State: task.Failed,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude, ID: "sid"}, Reviewer: task.Codex, Worktree: "/w/7-x"},
	})
	if !strings.Contains(buf.String(), "Transcript    /cfg/projects/-w-7-x/sid.jsonl\n") {
		t.Errorf("missing transcript in:\n%s", buf.String())
	}
	buf.Reset()
	Show(&buf, &task.Record{
		Key: task.Key{Repo: "a/b", Number: 7}, State: task.Failed,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Codex, ID: "th"}, Reviewer: task.Claude, Worktree: "/w/7-x"},
	})
	if strings.Contains(buf.String(), "Transcript") {
		t.Errorf("codex task shows a transcript:\n%s", buf.String())
	}
}

func TestShowPlanLinks(t *testing.T) {
	var buf bytes.Buffer
	Show(&buf, &task.Record{
		Key: task.Key{Repo: "a/b", Number: 7}, State: task.Implementing,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Codex}, Reviewer: task.Claude},
		Plan:       []task.PlanComment{{Doc: task.Analysis, URL: "https://x/1"}, {Doc: task.PRD, URL: "https://x/2"}},
	})
	if !strings.Contains(buf.String(), "Plan          Analysis https://x/1\n              PRD https://x/2\n") {
		t.Errorf("missing plan links in:\n%s", buf.String())
	}
}

func TestShowUndelegated(t *testing.T) {
	var buf bytes.Buffer
	Show(&buf, &task.Record{Key: task.Key{Repo: "a/b", Number: 1}, Title: "T", URL: "u", State: task.Discovered})
	for _, s := range []string{"a/b#1 T\na/b · u", "State  DISCOVERED", "Agent  not delegated"} {
		if !strings.Contains(buf.String(), s) {
			t.Errorf("missing %q in:\n%s", s, buf.String())
		}
	}
}

func TestShowPendingDecisionUsesBlockedFormat(t *testing.T) {
	var buf bytes.Buffer
	Show(&buf, &task.Record{
		Key: task.Key{Repo: "testorg/example", Number: 825}, Title: "Provider retries", State: task.NeedsDecision,
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

Answer it: press Enter on the task in the dashboard.
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
		Key: task.Key{Repo: "a/b", Number: 825}, Title: "T", State: task.WaitingForCI,
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
		Key: task.Key{Repo: "a/b", Number: 1}, State: task.Implementing,
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

func TestShowLabelsASelfReview(t *testing.T) {
	var buf bytes.Buffer
	Show(&buf, &task.Record{
		Key: task.Key{Repo: "a/b", Number: 1}, State: task.Implementing,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Reviewer: task.Codex, PR: 912},
		Reviews:    []task.Review{{Round: 1, Kind: task.SelfReview, Reviewer: task.Claude, Findings: []task.Finding{{Severity: "blocking", Summary: "no test"}}}},
	})
	if !strings.Contains(buf.String(), "Latest self-review (round 1 of 1, Claude,") {
		t.Errorf("self-review not labelled in:\n%s", buf.String())
	}
}

func TestShowPrintsProfileAndReason(t *testing.T) {
	var buf bytes.Buffer
	Show(&buf, &task.Record{
		Key: task.Key{Repo: "a/b", Number: 7}, State: task.Implementing,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Codex}, Reviewer: task.Claude, RouteReason: "narrow, well-specified change",
			Profile: &task.Profile{Name: "codex", Agent: task.Codex, PlanModel: "gpt-6-luna", PlanReasoningEffort: "xhigh", Model: "gpt-6-sol", ReasoningEffort: "medium"}},
	})
	if !strings.Contains(buf.String(), "Profile       codex (plan gpt-6-luna xhigh, implement gpt-6-sol medium) · narrow, well-specified change\n") {
		t.Errorf("missing profile in:\n%s", buf.String())
	}
	buf.Reset()
	Show(&buf, &task.Record{Key: task.Key{Repo: "a/b", Number: 7}, State: task.Implementing,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Reviewer: task.Codex}})
	if strings.Contains(buf.String(), "Profile") {
		t.Errorf("an unrouted task shows a profile:\n%s", buf.String())
	}
}
