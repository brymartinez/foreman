package workspace

import (
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/localissues"
	"github.com/brymartinez/foreman/internal/task"
)

var t0 = time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)

// fullTask sets every host-local field a task can hold, so a test can prove none
// of them reach the shared record.
func fullTask() *task.Record {
	q := task.Question{Issue: "Which store?", Context: "ctx", Options: []task.Option{{Label: "A", Description: "notion"}, {Label: "B", Description: "obsidian"}}, Recommendation: "A", ImpactIfWrong: "slow"}
	return &task.Record{
		Key:        task.Key{Repo: "o/r", Source: task.Local, ExternalID: "L-0A1B2C3D"},
		Identifier: "L-0A1B2C3D",
		Kind:       task.Issue,
		Title:      "Add storage",
		URL:        "https://example.com/1",
		State:      task.NeedsDecision,
		Body:       "notes",
		Delegation: &task.Delegation{
			LocalPlan:      &task.LocalPlan{Fingerprint: "fp1", Documents: map[task.PlanDoc]string{task.PRD: "reqs", task.ImplementationPlan: "plan"}},
			Planner:        task.Session{Agent: task.Claude, ID: "SESSION-PLANNER-SECRET"},
			Implementer:    task.Session{Agent: task.Claude, ID: "SESSION-IMPL-SECRET"},
			Account:        "ACCOUNT-PIN-SECRET",
			AccountEmail:   "pin-secret@users.noreply.github.com",
			Branch:         "feat/x",
			RepoDir:        "/host/checkout/REPODIR-SECRET",
			Worktree:       "/host/worktree/WORKTREE-SECRET",
			RunnerPID:      424242,
			RunnerLog:      "/host/logs/RUNNERLOG-SECRET.log",
			Launch:         &task.Launch{Token: "LAUNCH-TOKEN-SECRET", PID: 535353},
			PR:             7,
			PRURL:          "https://github.com/o/r/pull/7",
			PRHead:         "abcdef1",
			CheckpointHead: "abcdef2",
			CheckpointNext: "do more",
			Checkpoints:    2,
			LocalHead:      "abcdef3",
		},
		Usage:       task.Usage{ClaudeCostUSD: 987.65},
		Calls:       []task.ModelCall{{Step: "CALL-STEP-SECRET", ClaudeSessionID: "CALL-SESSION-SECRET"}},
		LastOutcome: &task.Outcome{Status: "BLOCKED", Summary: "needs a call", At: t0},
		Decisions: []task.Decision{
			{AskedAt: t0, Question: q, Answer: "A", AnsweredAt: t0.Add(time.Minute)},
			{AskedAt: t0.Add(time.Hour), Question: task.Question{Issue: "Second?", Options: []task.Option{{Label: "X"}, {Label: "X2"}}}},
			{AskedAt: t0.Add(2 * time.Hour), Question: task.Question{Issue: "Third?", Options: []task.Option{{Label: "Y"}, {Label: "Y2"}}}},
		},
		Reviews: []task.Review{{Kind: task.SelfReview, Round: 1, Reviewer: task.Claude, Clean: false, Summary: "found", Head: "abcdef2", At: t0,
			Findings: []task.Finding{{Severity: "high", Summary: "bug", Detail: "details"}}}},
	}
}

func fullInput() Input {
	return Input{
		Task: fullTask(),
		Issue: &localissues.Issue{ID: "L-0A1B2C3D", Title: "Add storage", Description: "desc", Status: localissues.Open, Repo: "o/r",
			Tags: []string{"a"}, CreatedAt: t0, UpdatedAt: t0},
		Triage:  "confirmed scope",
		StageAt: t0.Add(time.Hour),
	}
}

func TestRecordCarriesNoHostLocalField(t *testing.T) {
	r, err := Build(fullInput())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw) + r.Render() + r.QuestionText()
	for _, secret := range []string{"SECRET", "424242", "535353", "987.65", "/host/", "LAUNCH-TOKEN", "pin-secret"} {
		if strings.Contains(text, secret) {
			t.Errorf("shared record contains host-local value %q", secret)
		}
	}
}

func TestRecordCarriesTheSharedFields(t *testing.T) {
	r, err := Build(fullInput())
	if err != nil {
		t.Fatal(err)
	}
	if r.Format != Format || r.ID != "L-0A1B2C3D" || r.Source != task.Local || r.Repo != "o/r" || r.Title != "Add storage" || r.URL != "https://example.com/1" {
		t.Errorf("identity: %+v", r)
	}
	if r.Status != "NEEDS_DECISION" || !r.StageSince.Equal(t0.Add(time.Hour)) {
		t.Errorf("stage: %q %v", r.Status, r.StageSince)
	}
	if r.Outcome == nil || r.Outcome.Summary != "needs a call" {
		t.Errorf("outcome: %+v", r.Outcome)
	}
	w := r.Work
	if w == nil || w.Requirements != "reqs" || w.Plan != "plan" || w.Fingerprint != "fp1" || w.Branch != "feat/x" || w.PR == nil || w.PR.Number != 7 || w.Checkpoint == nil || len(w.Reviews) != 1 || len(w.Decisions) != 3 {
		t.Errorf("work: %+v", w)
	}
	if r.Triage != "confirmed scope" {
		t.Errorf("triage = %q", r.Triage)
	}
}

func TestRecordRoundTripsAndDigestIsStable(t *testing.T) {
	r, _ := Build(fullInput())
	raw, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if back.Digest() != r.Digest() {
		t.Fatal("digest changed across a round trip")
	}
	again, _ := Build(fullInput())
	if again.Digest() != r.Digest() {
		t.Fatal("digest is not deterministic")
	}
	in := fullInput()
	in.Task.State = task.Implementing
	changed, _ := Build(in)
	if changed.Digest() == r.Digest() {
		t.Fatal("a status change must change the digest")
	}
}

func TestParseRejectsBadPayloads(t *testing.T) {
	r, _ := Build(fullInput())
	good, _ := r.Encode()
	for name, mutate := range map[string]func(string) string{
		"other format":  func(s string) string { return strings.Replace(s, Format, "foreman-task/2", 1) },
		"unknown field": func(s string) string { return s + "worktree: /x\n" },
		"bad id":        func(s string) string { return strings.Replace(s, "L-0A1B2C3D", "nope", 1) },
		"two documents": func(s string) string { return s + "---\nformat: x\n" },
		"empty":         func(string) string { return "" },
	} {
		if _, err := Parse([]byte(mutate(string(good)))); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestParseSectionUsesTheLastPayloadThatParses(t *testing.T) {
	old, _ := Build(fullInput())
	in := fullInput()
	in.Task.State = task.Implementing
	newer, _ := Build(in)
	a, _ := old.Encode()
	b, _ := newer.Encode()
	text := "intro\n```yaml\n" + string(a) + "```\nmore\n```yaml\n" + string(b) + "```\n```yaml\nformat: broken\n```\n"
	got, err := ParseSection(text)
	if err != nil || got.Status != "IMPLEMENTING" {
		t.Fatalf("got %q, %v", got.Status, err)
	}
	if _, err := ParseSection("no payload here"); err == nil {
		t.Fatal("a section without a payload must fail")
	}
}

func TestQuestionTextShowsTheOldestUnansweredQuestion(t *testing.T) {
	r, _ := Build(fullInput())
	q := r.QuestionText()
	if !strings.Contains(q, "Second?") || strings.Contains(q, "Third?") || strings.Contains(q, "Which store?") {
		t.Errorf("question = %q", q)
	}
	if r.StatusLabel() != "Needs decision" {
		t.Errorf("label = %q", r.StatusLabel())
	}
	body := r.Render()
	for _, want := range []string{"Second?", "X", "reqs", "plan", "https://github.com/o/r/pull/7", "found"} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered record lacks %q", want)
		}
	}
}

func TestIssueWithoutATaskIsInbox(t *testing.T) {
	in := fullInput()
	in.Task = nil
	in.Triage = ""
	r, err := Build(in)
	if err != nil || r.Status != StatusInbox || r.Work != nil || r.Issue == nil || r.QuestionText() != "" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestGitHubTaskUsesItsRepoNumberID(t *testing.T) {
	rec := fullTask()
	rec.Key = task.Key{Repo: "o/r", Number: 9, Source: task.GitHub}
	rec.Identifier = "o/r#9"
	rec.State = task.ReadyForHuman
	r, err := Build(Input{Task: rec, Triage: "t"})
	if err != nil || r.ID != "o/r#9" || r.Source != task.GitHub || r.Issue != nil {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := Parse(mustEncode(t, r)); err != nil {
		t.Fatal(err)
	}
}

func mustEncode(t *testing.T, r Record) []byte {
	t.Helper()
	raw, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestHydrateRebuildsAnIdleTaskByTheImportRules(t *testing.T) {
	r, _ := Build(fullInput())
	rec, err := r.Hydrate(t0.Add(48 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != task.Discovered || rec.Delegation != nil {
		t.Errorf("a rebuilt task must be idle: %+v", rec)
	}
	if rec.Reset == nil || rec.Reset.PR != 7 || rec.Reset.Branch != "feat/x" {
		t.Errorf("an open PR becomes a reset mark: %+v", rec.Reset)
	}
	if rec.Imported == nil || rec.Imported.Documents[task.ImplementationPlan] != "plan" || rec.Imported.Fingerprint != "fp1" {
		t.Errorf("the saved plan must seed the next start: %+v", rec.Imported)
	}
	if len(rec.Decisions) != 3 || len(rec.Reviews) != 1 {
		t.Errorf("decisions %d reviews %d", len(rec.Decisions), len(rec.Reviews))
	}
}

func TestHydrateKeepsAFinishedRunDone(t *testing.T) {
	in := fullInput()
	in.Task.State = task.Done
	r, _ := Build(in)
	rec, err := r.Hydrate(t0)
	if err != nil || rec.State != task.Done || rec.Reset != nil {
		t.Fatalf("%+v %v", rec, err)
	}
}

func TestHydrateWithoutWorkBuildsNoTask(t *testing.T) {
	in := fullInput()
	in.Task = nil
	r, _ := Build(in)
	rec, err := r.Hydrate(t0)
	if err != nil || rec != nil {
		t.Fatalf("%+v %v", rec, err)
	}
}

func TestAFailedTaskSharesNoHostPathOrAccount(t *testing.T) {
	in := fullInput()
	in.Task.Delegation.Account, in.Task.Delegation.AccountEmail = "octo-work", "42+octo-work@users.noreply.github.com"
	for name, summary := range map[string]string{
		"a launch failure naming the log":     "open /home/dev/.local/share/foreman/logs/o-r-1.log: permission denied",
		"a worktree path":                     "git failed in " + in.Task.Delegation.Worktree,
		"the account check":                   "task is pinned to GitHub account octo-work, but gh is using octo-home. Run `gh auth switch --user octo-work`, then restart the task",
		"an SSH host check":                   "task is pinned to GitHub account octo-work, but SSH host e2e-host pushes as octo-home.",
		"the pinned account on its own":       "pushed as octo-work",
		"the pinned account email on its own": "committed as 42+octo-work@users.noreply.github.com",
	} {
		t.Run(name, func(t *testing.T) {
			in.Task.LastOutcome = &task.Outcome{Status: "failed", Summary: summary, At: t0}
			r, err := Build(in)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := r.Encode()
			text := string(raw) + r.RenderText()
			for _, leak := range []string{"/home/dev", "octo-work", "octo-home", "e2e-host", "WORKTREE-SECRET", "RUNNERLOG-SECRET"} {
				if strings.Contains(text, leak) {
					t.Errorf("the shared record leaks %q", leak)
				}
			}
		})
	}
}

func TestScrubKeepsURLsAndOrdinaryText(t *testing.T) {
	const s = "PR https://github.com/o/r/pull/7 and/or 1/2 stay"
	if got := scrubHost(s, nil); got != s {
		t.Fatalf("scrubHost changed %q to %q", s, got)
	}
}

func TestAPlanHoldingAFencedExampleRoundTripsThroughASection(t *testing.T) {
	in := fullInput()
	plan := "1. Add the loop.\n\n```go\nfor range 3 {\n}\n```\n\n2. Test it.\n```\nplain fence\n```\n"
	in.Task.Delegation.LocalPlan.Documents[task.ImplementationPlan] = plan
	r, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseSection("intro\n```yaml\n" + string(raw) + "```\nafter\n")
	if err != nil {
		t.Fatalf("a plan with a fence could not be read back: %v", err)
	}
	if got.Work == nil || got.Work.Plan != strings.TrimSpace(plan) && got.Work.Plan != plan {
		t.Fatalf("plan came back as %q", got.Work.Plan)
	}
}
