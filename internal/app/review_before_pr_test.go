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

func TestStartCapturesReviewBeforePR(t *testing.T) {
	for _, on := range []bool{false, true} {
		f := newFixture()
		f.app.ReviewBeforePR = on
		rec, err := f.app.Start(context.Background(), startReq(t))
		if err != nil {
			t.Fatal(err)
		}
		if rec.Delegation.ReviewBeforePR != on {
			t.Errorf("config %t: task captured %t", on, rec.Delegation.ReviewBeforePR)
		}
	}
}

func TestRestartCapturesTheCurrentReviewBeforePR(t *testing.T) {
	for _, on := range []bool{false, true} {
		f := newFixture()
		f.app.ReviewBeforePR = on
		key := task.Key{Repo: "ridezoomo/armada-backend", Number: 825}
		f.store.recs[key] = &task.Record{Key: key, Kind: task.Issue, State: task.Failed,
			Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Branch: "feat/825-provider-retries", ReviewBeforePR: !on}}
		rec, err := f.app.Start(context.Background(), startReq(t))
		if err != nil {
			t.Fatal(err)
		}
		if rec.Delegation.ReviewBeforePR != on {
			t.Errorf("config %t: restarted task captured %t", on, rec.Delegation.ReviewBeforePR)
		}
	}
}

func TestRestartKeepsTheLocalCommitAndProposalAtTheSameHead(t *testing.T) {
	f := newFixture()
	f.app.ReviewBeforePR = true
	head, _ := f.wt.Head(context.Background(), "")
	key := task.Key{Repo: "ridezoomo/armada-backend", Number: 825}
	f.store.recs[key] = &task.Record{Key: key, Kind: task.Issue, State: task.Failed,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Branch: "feat/825-provider-retries",
			ReviewBeforePR: true, LocalHead: head, ProposedTitle: "feat: retries", ProposedBody: "Closes #825"},
		Reviews: []task.Review{{Head: head, Clean: true}}}
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	d := rec.Delegation
	if d.LocalHead != head || d.ProposedTitle != "feat: retries" || d.ProposedBody != "Closes #825" || !rec.OpenPending() {
		t.Fatalf("delegation = %+v; the restart lost the reviewed commit", d)
	}
}

func TestRestartDropsTheLocalCommitWhenTheHeadMoved(t *testing.T) {
	f := newFixture()
	f.app.ReviewBeforePR = true
	key := task.Key{Repo: "ridezoomo/armada-backend", Number: 825}
	f.store.recs[key] = &task.Record{Key: key, Kind: task.Issue, State: task.Failed,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Branch: "feat/825-provider-retries",
			ReviewBeforePR: true, LocalHead: "0ld0001", ProposedTitle: "feat: retries", ProposedBody: "Closes #825"}}
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if d := rec.Delegation; d.LocalHead != "" || d.ProposedBody != "" {
		t.Fatalf("delegation = %+v; a commit that is not the head was kept", d)
	}
}

func reviewReadyTurn(id, body string) agent.Turn {
	return agent.Turn{Session: task.Session{Agent: task.Claude, ID: id}, Outcome: agent.Outcome{
		Status: agent.ReviewReady, Summary: "committed", PRTitle: "feat: retry 5xx", PRBody: body,
		Validation: []task.Check{{Command: "go test ./...", Passed: true}},
	}}
}

// localTask starts a task with review_before_pr on and its plan posted.
func localTask(t *testing.T, f fixture) task.Key {
	t.Helper()
	f.app.ReviewBeforePR = true
	key := started(t, f, startReq(t))
	postedPlan(f.store.recs[key])
	return key
}

func TestReviewReadyTurnRecordsTheCommitAndItsProposalTogether(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	f.wt.checkpointHeads = []string{"commit1"}
	f.claude.turn = reviewReadyTurn("sid-1", "Retry 5xx only.\n\nCloses #825")
	rec := f.store.recs[key]
	res := f.app.runTurn(context.Background(), rec, turnRequest{}, f.app.verifiedPR(io.Discard), io.Discard)
	if res.err != nil {
		t.Fatal(res.err)
	}
	applyTurnResult(rec, res, now)
	d := rec.Delegation
	if rec.State != task.Working || d.LocalHead != "commit1" || d.ProposedTitle != "feat: retry 5xx" || d.ProposedBody != "Retry 5xx only.\n\nCloses #825" || d.PR != 0 {
		t.Fatalf("state %s delegation %+v", rec.State, d)
	}
	if len(rec.Validation) != 1 || !rec.ReviewPending() {
		t.Fatalf("validation %v pending %t", rec.Validation, rec.ReviewPending())
	}
	p := f.claude.prompts[0]
	if !strings.Contains(p, `"review_ready"`) || strings.Contains(p, "gh pr create") {
		t.Fatalf("the implementer was told to open the PR:\n%s", p)
	}
}

func TestReviewReadyFailsWithoutAVerifiedCommit(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	f.wt.checkpointErr = errors.New("checkpoint has uncommitted or untracked files")
	f.claude.turn = reviewReadyTurn("sid-1", "Closes #825")
	rec := f.store.recs[key]
	res := f.app.runTurn(context.Background(), rec, turnRequest{}, f.app.verifiedPR(io.Discard), io.Discard)
	if res.err == nil || !strings.Contains(res.err.Error(), "uncommitted") {
		t.Fatalf("err = %v", res.err)
	}
	applyTurnResult(rec, res, now)
	if rec.State != task.Failed || rec.Delegation.LocalHead != "" {
		t.Fatalf("state %s local head %q", rec.State, rec.Delegation.LocalHead)
	}
}

func TestReviewReadyIsRejectedOutsideALocalReview(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	postedPlan(f.store.recs[key])
	f.wt.checkpointHeads = []string{"commit1"}
	f.claude.turn = reviewReadyTurn("sid-1", "Closes #825")
	res := f.app.runTurn(context.Background(), f.store.recs[key], turnRequest{}, f.app.verifiedPR(io.Discard), io.Discard)
	if res.err == nil || !strings.Contains(res.err.Error(), "review_ready") {
		t.Fatalf("err = %v", res.err)
	}
}

func TestPROpenedBeforeACleanLocalReviewFailsTheTurn(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	f.claude.turn = prTurn("sid-1")
	res := f.app.runTurn(context.Background(), f.store.recs[key], turnRequest{}, f.app.verifiedPR(io.Discard), io.Discard)
	if res.err == nil || !strings.Contains(res.err.Error(), "before a clean review") {
		t.Fatalf("err = %v", res.err)
	}
}

const (
	proposedTitle = "feat: retry 5xx"
	proposedBody  = "Retry 5xx only.\n\nCloses #825"
)

// opensPR makes the PR appear on GitHub only during the implementer's next resumed
// turn, as the agent's push and gh pr create would. reviews is how many reviews the
// reviewer must have finished by then.
func opensPR(t *testing.T, f fixture, head, title, body string, reviews int) {
	t.Helper()
	pr := f.gh.prs[912]
	delete(f.gh.prs, 912)
	pr.HeadSHA, pr.Title, pr.Body = head, title, body
	f.claude.onResume = func() {
		if !strings.Contains(f.claude.resumed[len(f.claude.resumed)-1], "gh pr create") {
			return
		}
		if got := len(f.codex.reviewed); got != reviews {
			t.Errorf("the PR opened after %d reviews, want %d", got, reviews)
		}
		f.gh.prs[912] = pr
	}
}

func openTurns(f fixture) int {
	n := 0
	for _, r := range f.claude.resumed {
		if strings.Contains(r, "gh pr create") {
			n++
		}
	}
	for _, p := range f.claude.prompts {
		if strings.Contains(p, "gh pr create") && strings.Contains(p, "Push it and open the pull request now") {
			n++
		}
	}
	return n
}

func TestReviewBeforePRReviewsTheLocalCommitThenOpensThePR(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	f.wt.checkpointHeads = []string{"commit1"}
	f.claude.turns = []agent.Turn{reviewReadyTurn("sid-1", proposedBody), prTurn("sid-1")}
	opensPR(t, f, "commit1", proposedTitle, proposedBody, 1)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if len(f.codex.reviewed) != 1 || !strings.Contains(f.codex.reviewed[0], "Retry 5xx only.") || !strings.Contains(f.codex.reviewed[0], "proposed pull request") {
		t.Fatalf("reviews = %q", f.codex.reviewed)
	}
	if openTurns(f) != 1 {
		t.Fatalf("open turns = %d, want 1", openTurns(f))
	}
	d := got.Delegation
	if got.State != task.Reviewing || d.PR != 912 || d.PRHead != "commit1" || d.LocalHead != "" || d.ProposedBody != "" {
		t.Fatalf("state %s delegation %+v", got.State, d)
	}
	if r := got.LatestReview(); r == nil || !r.Clean || r.PR != 912 || r.Head != "commit1" || len(got.Reviews) != 1 {
		t.Fatalf("reviews = %+v", got.Reviews)
	}
	if got.ReviewPending() || got.PhaseOf() != task.PhaseCI {
		t.Fatalf("pending %t phase %s; the clean local review must cover the PR head", got.ReviewPending(), got.PhaseOf())
	}
}

func TestReviewBeforePRFixesFindingsLocallyAndOpensWithTheUpdatedDescription(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	updated := "Retry 5xx only, never 4xx.\n\nCloses #825"
	f.wt.checkpointHeads = []string{"commit1", "commit2"}
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking")}
	f.claude.turns = []agent.Turn{reviewReadyTurn("sid-1", proposedBody), reviewReadyTurn("sid-1", updated), prTurn("sid-1")}
	opensPR(t, f, "commit2", proposedTitle, updated, 2)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	fix := f.claude.resumed[0]
	if !strings.Contains(fix, "4xx retried") || !strings.Contains(fix, "review_ready") || strings.Contains(fix, "pr_opened") {
		t.Fatalf("fix turn = %q", fix)
	}
	if len(f.codex.reviewed) != 2 || !strings.Contains(f.codex.reviewed[1], "never 4xx") {
		t.Fatalf("the fix was not reviewed with its description: %q", f.codex.reviewed)
	}
	if got.State != task.Reviewing || got.Delegation.PR != 912 || got.Delegation.PRHead != "commit2" || openTurns(f) != 1 {
		t.Fatalf("state %s delegation %+v open turns %d", got.State, got.Delegation, openTurns(f))
	}
	if got.Reviews[0].Head != "commit1" || got.Reviews[0].PR != 0 || got.Reviews[1].Head != "commit2" || got.Reviews[1].PR != 912 {
		t.Fatalf("reviews = %+v", got.Reviews)
	}
	// Local history may be rewritten before the push, so each commit only has to
	// extend the commit the work started from.
	if p := f.wt.checkpointPrior; len(p) != 2 || p[0] != "basehead" || p[1] != "basehead" {
		t.Fatalf("checkpoint priors = %v", p)
	}
}

// pausedAtFixLimit runs a local task whose reviewer still has findings after one fix.
func pausedAtFixLimit(t *testing.T) (fixture, task.Key) {
	t.Helper()
	f := newFixture()
	f.app.FixRounds = 1
	key := localTask(t, f)
	f.wt.checkpointHeads = []string{"commit1", "commit2"}
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking"), findings("blocking")}
	f.claude.turns = []agent.Turn{reviewReadyTurn("sid-1", proposedBody), reviewReadyTurn("sid-1", proposedBody)}
	opensPR(t, f, "commit3", proposedTitle, proposedBody, 3)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.NeedsDecision || got.Delegation.PR != 0 || openTurns(f) != 0 {
		t.Fatalf("state %s pr %d open turns %d", got.State, got.Delegation.PR, openTurns(f))
	}
	q := got.Pending().Question
	if strings.Contains(q.Context, "PR #") || len(q.Options) != 2 || !strings.Contains(q.Options[1].Description, "without opening a pull request") {
		t.Fatalf("question = %+v", q)
	}
	return f, key
}

func answer(f fixture, key task.Key, text string) {
	rec := f.store.recs[key]
	p := rec.Pending()
	p.Answer, p.AnsweredAt = text, now
	rec.State = task.Working
}

func TestReviewBeforePRPausesAtTheFixLimitWithoutOpeningAPR(t *testing.T) {
	pausedAtFixLimit(t)
}

func TestReviewBeforePRAnotherRoundResetsTheLimitAndContinues(t *testing.T) {
	f, key := pausedAtFixLimit(t)
	answer(f, key, optionText(task.Option{Label: "A", Description: f.store.recs[key].Pending().Question.Options[0].Description}))
	f.wt.checkpointHeads = []string{"commit3"}
	f.claude.turns = []agent.Turn{reviewReadyTurn("sid-1", proposedBody), prTurn("sid-1")}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if !strings.Contains(f.claude.resumed[1], "Decision: A.") || !strings.Contains(f.claude.resumed[1], "review_ready") {
		t.Fatalf("answer turn = %q", f.claude.resumed[1])
	}
	if got.State != task.Reviewing || got.Delegation.PR != 912 || got.Delegation.PRHead != "commit3" || openTurns(f) != 1 {
		t.Fatalf("state %s delegation %+v", got.State, got.Delegation)
	}
}

func TestReviewBeforePRStopAnswerFailsWithoutAPR(t *testing.T) {
	f, key := pausedAtFixLimit(t)
	stop := f.store.recs[key].Pending().Question.Options[1]
	answer(f, key, optionText(stop))
	turns := len(f.claude.resumed) + f.claude.starts
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "no PR opened") || got.Delegation.PR != 0 {
		t.Fatalf("state %s outcome %+v", got.State, got.LastOutcome)
	}
	if len(f.claude.resumed)+f.claude.starts != turns || len(got.Undelivered()) != 0 {
		t.Fatalf("the stop ran an agent turn or left the answer undelivered")
	}
}

func TestReviewBeforePRFreeTextAnswerReachesTheImplementer(t *testing.T) {
	f, key := pausedAtFixLimit(t)
	answer(f, key, "Keep 4xx retries; fix only the logging")
	f.wt.checkpointHeads = []string{"commit3"}
	f.claude.turns = []agent.Turn{reviewReadyTurn("sid-1", proposedBody), prTurn("sid-1")}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.claude.resumed[1], "Keep 4xx retries; fix only the logging") {
		t.Fatalf("answer turn = %q", f.claude.resumed[1])
	}
	if got := f.store.recs[key]; got.Delegation.PR != 912 {
		t.Fatalf("state %s pr %d", got.State, got.Delegation.PR)
	}
}

func TestReviewBeforePRMergesTheBaseLocallyBeforeReview(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	f.wt.checkpointHeads = []string{"commit1", "commit2"}
	f.wt.conflicts = [][]string{{"go.mod"}}
	f.claude.turns = []agent.Turn{reviewReadyTurn("sid-1", proposedBody), reviewReadyTurn("sid-1", proposedBody), prTurn("sid-1")}
	opensPR(t, f, "commit2", proposedTitle, proposedBody, 1)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(f.wt.checks) == 0 || !f.wt.checks[0].Local || f.wt.checks[0].Base != "main" {
		t.Fatalf("checks = %+v", f.wt.checks)
	}
	merge := f.claude.resumed[0]
	if !strings.Contains(merge, "go.mod") || strings.Contains(merge, "git push") || !strings.Contains(merge, "review_ready") {
		t.Fatalf("merge turn = %q", merge)
	}
	got := f.store.recs[key]
	if len(f.codex.reviewed) != 1 || got.Delegation.PRHead != "commit2" || got.State != task.Reviewing {
		t.Fatalf("reviews %d state %s delegation %+v", len(f.codex.reviewed), got.State, got.Delegation)
	}
}

func TestReviewBeforePRAsksTheDeveloperWhenTheLocalMergeKeepsConflicting(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	f.wt.checkpointHeads = []string{"commit1", "commit2", "commit3"}
	f.wt.conflicts = [][]string{{"go.mod"}, {"go.mod"}, {"go.mod"}}
	f.claude.turns = []agent.Turn{reviewReadyTurn("sid-1", proposedBody), reviewReadyTurn("sid-1", proposedBody), reviewReadyTurn("sid-1", proposedBody)}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.NeedsDecision || len(f.codex.reviewed) != 0 {
		t.Fatalf("state %s reviews %d", got.State, len(f.codex.reviewed))
	}
	q := got.Pending().Question
	if strings.Contains(q.Context, "PR #") || strings.Contains(q.Options[0].Description, "push") {
		t.Fatalf("question = %+v", q)
	}
}

func TestReviewBeforePRResumesAStoredCommitAtItsReview(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	d := f.store.recs[key].Delegation
	d.LocalHead, d.ProposedTitle, d.ProposedBody = "commit1", proposedTitle, proposedBody
	f.claude.turns = []agent.Turn{prTurn("sid-1")}
	opensPR(t, f, "commit1", proposedTitle, proposedBody, 1)
	f.claude.during = func() {
		if len(f.codex.reviewed) != 1 {
			t.Error("the implementer ran before the stored commit was reviewed")
		}
		f.gh.prs[912] = PullRequest{Key: task.Key{Repo: "ridezoomo/armada-backend", Number: 912}, URL: "https://github.com/ridezoomo/armada-backend/pull/912",
			State: "open", HeadRefName: "feat/825-provider-retries", BaseRefName: "main", HeadSHA: "commit1", Title: proposedTitle, Body: proposedBody}
	}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.Reviewing || got.Delegation.PR != 912 || len(f.codex.reviewed) != 1 {
		t.Fatalf("state %s pr %d reviews %d", got.State, got.Delegation.PR, len(f.codex.reviewed))
	}
}

func TestReviewBeforePRResumesACleanCommitAtThePROpening(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	rec := f.store.recs[key]
	rec.Delegation.LocalHead, rec.Delegation.ProposedTitle, rec.Delegation.ProposedBody = "commit1", proposedTitle, proposedBody
	rec.Delegation.Implementer.ID = "sid-1"
	rec.Reviews = []task.Review{{Round: 1, Reviewer: task.Codex, Head: "commit1", Clean: true, At: now}}
	f.claude.turns = []agent.Turn{prTurn("sid-1")}
	opensPR(t, f, "commit1", proposedTitle, proposedBody, 0)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Reviewing || got.Delegation.PR != 912 || len(f.codex.reviewed) != 0 || openTurns(f) != 1 {
		t.Fatalf("state %s pr %d reviews %d open turns %d", got.State, got.Delegation.PR, len(f.codex.reviewed), openTurns(f))
	}
	if got.LatestReview().PR != 912 || got.PhaseOf() != task.PhaseCI {
		t.Fatalf("review %+v phase %s", got.LatestReview(), got.PhaseOf())
	}
}

func TestReviewBeforePRAdoptsAMatchingPRWithoutAnotherTurn(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	rec := f.store.recs[key]
	rec.Delegation.LocalHead, rec.Delegation.ProposedTitle, rec.Delegation.ProposedBody = "commit1", proposedTitle, proposedBody
	rec.Reviews = []task.Review{{Round: 1, Reviewer: task.Codex, Head: "commit1", Clean: true, At: now}}
	pr := f.gh.prs[912]
	pr.HeadSHA, pr.Title, pr.Body = "commit1", proposedTitle, proposedBody+"\n"
	f.gh.prs[912] = pr
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Reviewing || got.Delegation.PR != 912 || f.claude.starts != 0 || len(f.claude.resumed) != 0 {
		t.Fatalf("state %s pr %d starts %d resumes %d", got.State, got.Delegation.PR, f.claude.starts, len(f.claude.resumed))
	}
}

func TestReviewBeforePRFailsWhenThePRDoesNotMatchTheReview(t *testing.T) {
	for name, change := range map[string]func(pr *PullRequest){
		"another head":        func(pr *PullRequest) { pr.HeadSHA = "commit9" },
		"an edited title":     func(pr *PullRequest) { pr.Title = "feat: something else" },
		"an edited body":      func(pr *PullRequest) { pr.Body = "Retry everything.\n\nCloses #825" },
		"a closed PR (state)": func(pr *PullRequest) { pr.State = "closed" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			key := localTask(t, f)
			rec := f.store.recs[key]
			rec.Delegation.LocalHead, rec.Delegation.ProposedTitle, rec.Delegation.ProposedBody = "commit1", proposedTitle, proposedBody
			rec.Delegation.Implementer.ID = "sid-1"
			rec.Reviews = []task.Review{{Round: 1, Reviewer: task.Codex, Head: "commit1", Clean: true, At: now}}
			f.claude.turns = []agent.Turn{prTurn("sid-1")}
			pr := f.gh.prs[912]
			delete(f.gh.prs, 912)
			pr.HeadSHA, pr.Title, pr.Body = "commit1", proposedTitle, proposedBody
			change(&pr)
			f.claude.onResume = func() { f.gh.prs[912] = pr }
			if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
				t.Fatal(err)
			}
			got := f.store.recs[key]
			if got.State != task.Failed || got.Delegation.PR != 0 {
				t.Fatalf("state %s pr %d outcome %+v", got.State, got.Delegation.PR, got.LastOutcome)
			}
		})
	}
}

func TestATaskKeepsItsWorkflowWhenTheSettingChanges(t *testing.T) {
	f := newFixture()
	key := localTask(t, f)
	f.app.ReviewBeforePR = false
	f.wt.checkpointHeads = []string{"commit1"}
	f.claude.turns = []agent.Turn{reviewReadyTurn("sid-1", proposedBody), prTurn("sid-1")}
	opensPR(t, f, "commit1", proposedTitle, proposedBody, 1)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.claude.prompts[0], `"review_ready"`) || f.store.recs[key].Delegation.PR != 912 {
		t.Fatal("a task started with review_before_pr lost it when config changed")
	}

	f = newFixture()
	key = started(t, f, startReq(t))
	postedPlan(f.store.recs[key])
	f.app.ReviewBeforePR = true
	f.claude.turn = prTurn("sid-1")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.claude.prompts[0], "review_ready") || f.store.recs[key].State != task.Reviewing {
		t.Fatal("a task started without review_before_pr picked it up from config")
	}
}
