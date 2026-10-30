package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// selfFindings is a self-review finding worded unlike findings(), so a test can tell
// which reviewer's text reached a prompt.
func selfFindings() agent.ReviewOutcome {
	return agent.ReviewOutcome{Summary: "own check", Findings: []task.Finding{{Severity: "blocking", File: "retry_test.go", Summary: "no test covers the 4xx path"}}}
}

func kinds(rec *task.Record) string {
	var out []string
	for _, r := range rec.Reviews {
		k := "competing"
		if r.Kind == task.SelfReview {
			k = "self"
		}
		if !r.Clean {
			k += "!"
		}
		out = append(out, k)
	}
	return strings.Join(out, " ")
}

func TestSelfReviewRunsBeforeTheCompetingReviewOfAPR(t *testing.T) {
	f := newFixture()
	f.app.ReviewerBudgetUSD = 1.5
	f.app.ReviewRounds = 2
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if kinds(got) != "self competing" || got.State != task.WaitingForCI {
		t.Fatalf("state %s reviews %s", got.State, kinds(got))
	}
	self := got.Reviews[0]
	pr := f.gh.prs[912]
	if self.Reviewer != task.Claude || self.PR != 912 || self.Head != "c0ffee1" || self.Proposal != task.ProposalDigest(pr.Title, pr.Body) {
		t.Errorf("self-review = %+v", self)
	}
	if len(f.claude.reviewed) != 1 || len(f.codex.reviewed) != 1 {
		t.Fatalf("reviews: claude %d codex %d, want one each", len(f.claude.reviewed), len(f.codex.reviewed))
	}
	if f.claude.reviewBudgets[0] != 1.5 || f.claude.steps[len(f.claude.steps)-1] != agent.StepReview {
		t.Errorf("self-review budget %v steps %v", f.claude.reviewBudgets, f.claude.steps)
	}
	p := f.claude.reviewed[0]
	if strings.Contains(p, "> Retry 5xx only.") || !strings.Contains(p, "git diff origin/main...HEAD") || !strings.Contains(p, "Read the complete diff 2 times") {
		t.Errorf("self-review prompt must use the diff and pass count without the PR body:\n%s", p)
	}
	for _, leak := range []string{"sid-1", "Pull request comments", "Earlier review rounds"} {
		if strings.Contains(p, leak) {
			t.Errorf("self-review prompt carries %q:\n%s", leak, p)
		}
	}
}

func TestSelfReviewFindingsGoToTheImplementerAndAFreshSelfReviewFollows(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{prTurn("sid-1"), prTurn("sid-1")}
	f.claude.reviews = []agent.ReviewOutcome{selfFindings()}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if kinds(got) != "self! self competing" || got.State != task.WaitingForCI {
		t.Fatalf("state %s reviews %s", got.State, kinds(got))
	}
	if f.claude.starts != 2 || !strings.Contains(f.claude.prompts[1], "no test covers the 4xx path") {
		t.Errorf("correction not started fresh with self-review findings: %q", f.claude.prompts)
	}
	if len(f.claude.reviewed) != 2 || strings.Contains(f.claude.reviewed[1], "no test covers the 4xx path") {
		t.Errorf("the second self-review must start fresh, without earlier findings: %q", f.claude.reviewed)
	}
	if strings.Contains(f.codex.reviewed[0], "no test covers the 4xx path") || strings.Contains(f.codex.reviewed[0], "Earlier review rounds") {
		t.Errorf("the competing review saw the self-review:\n%s", f.codex.reviewed[0])
	}
}

func TestCompetingFindingsAreFixedThenSelfReviewedBeforeTheNextCompetingReview(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{prTurn("sid-1"), prTurn("sid-1"), prTurn("sid-1")}
	f.claude.reviews = []agent.ReviewOutcome{{Clean: true}, selfFindings()}
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking")}
	// Each fix pushes a new head, so the change the reviewers read is new.
	heads := []string{"c0ffee2", "c0ffee3"}
	f.claude.during = func() {
		if f.claude.starts > 1 {
			pr := f.gh.prs[912]
			pr.HeadSHA, heads = heads[0], heads[1:]
			f.gh.prs[912] = pr
		}
	}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if kinds(got) != "self competing! self! self competing" {
		t.Fatalf("reviews %s", kinds(got))
	}
	last := f.codex.reviewed[1]
	if strings.Contains(last, "Round 2 (one problem)") || strings.Contains(last, "no test covers the 4xx path") || strings.Contains(last, "own check") {
		t.Errorf("the competing re-review must start from the diff alone:\n%s", last)
	}
}

func TestSelfReviewFailureStopsTheTaskBeforeTheCompetingReview(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	f.claude.reviewErr = errors.New("claude exited 1")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || len(f.codex.reviewed) != 0 || len(got.Reviews) != 0 {
		t.Fatalf("state %s codex reviews %d reviews %s", got.State, len(f.codex.reviewed), kinds(got))
	}
	if !strings.Contains(got.LastOutcome.Summary, "claude self-review failed: claude exited 1") {
		t.Errorf("outcome = %+v", got.LastOutcome)
	}
}

func TestFixesFromBothReviewersShareTheFixLimit(t *testing.T) {
	f := newFixture()
	f.app.FixRounds = 1
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	f.claude.reviews = []agent.ReviewOutcome{selfFindings()}
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking")}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.NeedsDecision || kinds(got) != "self! self competing!" || f.claude.starts != 2 {
		t.Fatalf("state %s reviews %s fresh turns %d", got.State, kinds(got), f.claude.starts)
	}
}

func TestRestartAfterACleanSelfReviewGoesStraightToTheCompetingReview(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	postedPlan(f, f.store.recs[key])
	f.claude.turn = prTurn("sid-1")
	f.codex.reviewErr = errors.New("codex exited 1")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	// A runner that died after the self-review: the task is still under review.
	rec := f.store.recs[key]
	rec.State, rec.LastOutcome = task.WaitingForCI, nil
	if kinds(rec) != "self" {
		t.Fatalf("reviews %s", kinds(rec))
	}
	if gated(rec) {
		t.Fatal("the gate would hand over a task with only a self-review")
	}
	f.codex.reviewErr = nil
	rec.State = task.Implementing
	if err := f.run(context.Background(), key, 4243, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; kinds(got) != "self competing" || len(f.claude.reviewed) != 1 {
		t.Fatalf("reviews %s self-reviews %d", kinds(got), len(f.claude.reviewed))
	}
}

func TestRestartSelfReviewsAgainWhenTheHeadOrDescriptionChanged(t *testing.T) {
	for name, change := range map[string]func(pr *PullRequest){
		"new head":        func(pr *PullRequest) { pr.HeadSHA = "c0ffee2" },
		"new description": func(pr *PullRequest) { pr.Body = "Retry 5xx and 429.\n\nCloses #825" },
	} {
		f := newFixture()
		key := started(t, f, startReq(t))
		postedPlan(f, f.store.recs[key])
		f.claude.turn = prTurn("sid-1")
		f.codex.reviewErr = errors.New("codex exited 1")
		if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
			t.Fatal(err)
		}
		rec := f.store.recs[key]
		rec.State = task.Implementing
		pr := f.gh.prs[912]
		change(&pr)
		f.gh.prs[912] = pr
		f.codex.reviewErr = nil
		if err := f.run(context.Background(), key, 4243, io.Discard); err != nil {
			t.Fatal(err)
		}
		got := f.store.recs[key]
		if kinds(got) != "self self competing" || len(f.claude.reviewed) != 2 {
			t.Errorf("%s: reviews %s self-reviews %d", name, kinds(got), len(f.claude.reviewed))
		}
		if last := got.Reviews[1]; last.Head != pr.HeadSHA || last.Proposal != task.ProposalDigest(pr.Title, pr.Body) {
			t.Errorf("%s: the second self-review recorded %+v", name, last)
		}
	}
}

func TestLocalChangeIsSelfReviewedBeforeTheCompetingReviewAndThePR(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	f.wt.checkpointHeads = []string{"commit1", "commit2"}
	f.claude.turns = []agent.Turn{reviewReadyTurn("sid-1", proposedBody), reviewReadyTurn("sid-1", proposedBody)}
	f.claude.reviews = []agent.ReviewOutcome{selfFindings()}
	opensPR(t, f, "commit2", proposedTitle, proposedBody, 1)
	f.claude.turn = prTurn("sid-1")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if kinds(got) != "self! self competing" || got.Delegation.PR != 912 {
		t.Fatalf("reviews %s pr %d state %s", kinds(got), got.Delegation.PR, got.State)
	}
	self := got.Reviews[1]
	if self.Head != "commit2" || self.Proposal != task.ProposalDigest(proposedTitle, proposedBody) || self.PR != 0 {
		t.Errorf("local self-review = %+v", self)
	}
	p := f.claude.reviewed[0]
	if strings.Contains(p, "Proposed pull request description") || strings.Contains(p, "Title: feat: retry 5xx") || strings.Contains(p, "## Confirmed triage scope") || !strings.Contains(p, "## Checked work") {
		t.Errorf("local self-review prompt:\n%s", p)
	}
	if !strings.Contains(f.claude.prompts[1], "no test covers the 4xx path") {
		t.Errorf("local correction turn lacks the self-review findings: %q", f.claude.prompts[1])
	}
}

func TestLocalRestartSelfReviewsAProposalThatChangedOnTheSameCommit(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	rec := f.store.recs[key]
	d := rec.Delegation
	d.LocalHead, d.ProposedTitle, d.ProposedBody = "commit1", proposedTitle, "Retry 5xx and 429.\n\nCloses #825"
	rec.Reviews = []task.Review{{Round: 1, Kind: task.SelfReview, Reviewer: task.Claude, Head: "commit1", Proposal: task.ProposalDigest(proposedTitle, proposedBody), Clean: true, At: now}}
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking")}
	f.claude.turn = agent.Turn{Session: task.Session{Agent: task.Claude, ID: "sid-1"}, Outcome: agent.Outcome{Status: agent.Blocked, Questions: []task.Question{{Issue: "?", Options: []task.Option{{Label: "A"}, {Label: "B"}}}}}}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; kinds(got) != "self self competing!" || len(f.claude.reviewed) != 1 {
		t.Fatalf("reviews %s self-reviews %d", kinds(got), len(f.claude.reviewed))
	}
}
