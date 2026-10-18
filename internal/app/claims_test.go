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

func TestStartTurnsOnClaimVerificationForATaskThatPlans(t *testing.T) {
	for _, on := range []bool{false, true} {
		f := newFixture()
		f.app.ClaimVerification = on
		rec, err := f.app.Start(context.Background(), startReq(t))
		if err != nil {
			t.Fatal(err)
		}
		if rec.Delegation.ClaimVerification != on {
			t.Errorf("app %t: task captured %t", on, rec.Delegation.ClaimVerification)
		}
	}
}

func TestStartAfterResetThatContinuesAPRLeavesClaimVerificationOff(t *testing.T) {
	f := newFixture()
	f.app.ClaimVerification = true
	resetTask(t, f, "open")
	got, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if d := got.Delegation; d.PR != 912 || d.ClaimVerification {
		t.Fatalf("delegation = %+v; a task that continues a PR has no plan to verify claims against", d)
	}
}

func TestRestartKeepsTheClaimVerificationTheTaskStartedWith(t *testing.T) {
	for _, on := range []bool{false, true} {
		f := newFixture()
		f.app.ClaimVerification = on
		key := task.Key{Repo: "testorg/example", Number: 825}
		f.store.recs[key] = &task.Record{Key: key, Kind: task.Issue, State: task.Failed,
			Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Branch: "feat/825-provider-retries", ClaimVerification: !on}}
		rec, err := f.app.Start(context.Background(), startReq(t))
		if err != nil {
			t.Fatal(err)
		}
		if rec.Delegation.ClaimVerification != !on {
			t.Errorf("app %t: the restart replaced the task's %t", on, !on)
		}
	}
}

func TestRestartKeepsTheClaimsWithTheKeptPR(t *testing.T) {
	f := newFixture()
	f.app.ClaimVerification = true
	key := task.Key{Repo: "testorg/example", Number: 825}
	claims := []task.Claim{{Criteria: []string{"AC1"}, Behavior: "retries 5xx", Evidence: "retry.go", Check: "go test ./..."}}
	f.store.recs[key] = &task.Record{Key: key, Kind: task.Issue, State: task.Failed,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Branch: "feat/825-provider-retries", PR: 912, PRHead: "abc",
			ClaimVerification: true, Claims: claims, PRDDigest: task.Digest("the PRD")}}
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if d := rec.Delegation; d.PR != 912 || task.ClaimsDigest(d.Claims) != task.ClaimsDigest(claims) || d.PRDDigest != task.Digest("the PRD") {
		t.Fatalf("delegation = %+v; the restart lost the claims of the kept PR", d)
	}
}

var retryClaims = []task.Claim{{Criteria: []string{"R1. Retry 5xx."}, Behavior: "the client retries 5xx responses", Evidence: "retry.go:40, retry_test.go TestRetry5xx", Check: "go test ./... passed"}}

// claimedTurn is prTurn with a claim set.
func claimedTurn(id string, claims []task.Claim) agent.Turn {
	t := prTurn(id)
	t.Outcome.Claims = claims
	return t
}

// claimTask starts #825 with claim verification on.
func claimTask(t *testing.T, f fixture) task.Key {
	t.Helper()
	f.app.ClaimVerification = true
	return started(t, f, startReq(t))
}

func TestAChangeWithClaimsStoresThemInTheTaskFile(t *testing.T) {
	f := newFixture()
	key := claimTask(t, f)
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), claimedTurn("sid-2", retryClaims)}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if task.ClaimsDigest(got.Delegation.Claims) != task.ClaimsDigest(retryClaims) {
		t.Fatalf("claims = %+v", got.Delegation.Claims)
	}
	p := f.claude.prompts[1]
	if !strings.Contains(p, "## Claims") || !strings.Contains(p, "files in the worktree") {
		t.Errorf("the implementation prompt does not ask for claims or keep them private:\n%s", p)
	}
	for _, c := range f.gh.comments {
		if strings.Contains(c.Body, "the client retries 5xx responses") {
			t.Errorf("a claim reached an issue comment: %q", c.Body)
		}
	}
}

func TestAChangeWithoutClaimsIsAskedForThemOnce(t *testing.T) {
	f := newFixture()
	key := claimTask(t, f)
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2"), claimedTurn("sid-2", retryClaims)}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if task.ClaimsDigest(got.Delegation.Claims) != task.ClaimsDigest(retryClaims) || got.State == task.Failed {
		t.Fatalf("state %s claims %+v", got.State, got.Delegation.Claims)
	}
	if len(f.claude.resumed) != 1 || !strings.HasPrefix(f.claude.resumed[0], "sid-2: ") || !strings.Contains(f.claude.resumed[0], "had no claims") {
		t.Fatalf("resumed = %q, want one request for the claims", f.claude.resumed)
	}
}

func TestAChangeStillWithoutClaimsFailsTheTask(t *testing.T) {
	f := newFixture()
	key := claimTask(t, f)
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2"), prTurn("sid-2")}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "without the claim set") {
		t.Fatalf("state %s outcome %+v", got.State, got.LastOutcome)
	}
	if len(f.claude.reviewed)+len(f.codex.reviewed) != 0 {
		t.Error("a change without claims was reviewed")
	}
}

func TestAFixReplacesTheClaimSetAndIsAskedForTheFullSet(t *testing.T) {
	f := newFixture()
	key := claimTask(t, f)
	fixed := []task.Claim{retryClaims[0], {Criteria: []string{"R2. Never retry 4xx."}, Behavior: "4xx is returned at once", Evidence: "retry.go:52", Check: "go test ./... passed"}}
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), claimedTurn("sid-2", retryClaims), claimedTurn("sid-2", fixed)}
	f.claude.reviews = []agent.ReviewOutcome{findings("blocking")}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if task.ClaimsDigest(got.Delegation.Claims) != task.ClaimsDigest(fixed) {
		t.Fatalf("claims = %+v, want the fix's full set", got.Delegation.Claims)
	}
	if len(f.claude.resumed) != 1 || !strings.Contains(f.claude.resumed[0], "full updated set of claims") {
		t.Fatalf("resumed = %q, want the findings with a request for the full claim set", f.claude.resumed)
	}
}

func TestATaskWithoutClaimVerificationIgnoresClaims(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2")}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || got.Delegation.Claims != nil || len(f.claude.resumed) != 0 || strings.Contains(f.claude.prompts[1], "## Claims") {
		t.Fatalf("state %s claims %+v resumed %q", got.State, got.Delegation.Claims, f.claude.resumed)
	}
}

// prdComment finds the PRD comment the planning turn posted on the fixture's issue.
func prdComment(t *testing.T, f fixture) IssueComment {
	t.Helper()
	for _, c := range f.gh.comments {
		if doc, _, ok := task.ParsePlanMarker(c.Body); ok && doc == task.PRD {
			return c
		}
	}
	t.Fatal("no PRD comment on the issue")
	return IssueComment{}
}

// editPRD replaces the PRD comment's text, keeping its marker and heading.
func editPRD(t *testing.T, f fixture, text string) {
	t.Helper()
	c := prdComment(t, f)
	marker, _, _ := strings.Cut(c.Body, "\n")
	c.Body = marker + "\n## PRD\n\n" + text
	f.gh.comments[c.ID] = c
}

func TestBothReviewsReadThePRDAndTheClaims(t *testing.T) {
	f := newFixture()
	key := claimTask(t, f)
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), claimedTurn("sid-2", retryClaims)}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if kinds(got) != "self competing" || got.State != task.WaitingForCI {
		t.Fatalf("state %s reviews %s", got.State, kinds(got))
	}
	for name, p := range map[string]string{"self-review": f.claude.reviewed[0], "competing review": f.codex.reviewed[0]} {
		for _, want := range []string{"## PRD", "> R1. Retry 5xx.", "## Implementer's claims", "the client retries 5xx responses", "retry.go:40, retry_test.go TestRetry5xx", "counterexample", "> Retry 5xx only."} {
			if !strings.Contains(p, want) {
				t.Errorf("%s prompt lacks %q:\n%s", name, want, p)
			}
		}
		if strings.Contains(p, "foreman:prd") {
			t.Errorf("%s prompt carries the PRD comment's marker", name)
		}
	}
	prd, cd := task.Digest("R1. Retry 5xx."), task.ClaimsDigest(retryClaims)
	if got.Delegation.PRDDigest != prd {
		t.Errorf("PRD digest = %q, want the digest of the PRD text", got.Delegation.PRDDigest)
	}
	for _, r := range got.Reviews {
		if r.Claims != cd || r.PRD != prd {
			t.Errorf("review round %d covers claims %q PRD %q", r.Round, r.Claims, r.PRD)
		}
	}
}

func TestTheCompetingReviewOfClaimsDoesNotSeeSelfReviewRounds(t *testing.T) {
	f := newFixture()
	key := claimTask(t, f)
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), claimedTurn("sid-2", retryClaims), claimedTurn("sid-2", retryClaims)}
	f.claude.reviews = []agent.ReviewOutcome{selfFindings()}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(f.codex.reviewed) != 1 || strings.Contains(f.codex.reviewed[0], "no test covers the 4xx path") {
		t.Fatalf("competing reviews %d; a self-review finding reached the competing review", len(f.codex.reviewed))
	}
}

func TestAReviewStopsWhenThePRDCannotBeRead(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(t *testing.T, f fixture)
		want   string
	}{
		"deleted": {func(t *testing.T, f fixture) { delete(f.gh.comments, prdComment(t, f).ID) }, "was deleted"},
		"emptied": {func(t *testing.T, f fixture) { editPRD(t, f, "  ") }, "is empty"},
		"unmarked": {func(t *testing.T, f fixture) {
			c := prdComment(t, f)
			c.Body = "Our requirements are elsewhere now."
			f.gh.comments[c.ID] = c
		}, "no longer marked as the PRD"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			key := claimTask(t, f)
			f.claude.turns = []agent.Turn{plannedTurn("sid-1"), claimedTurn("sid-2", retryClaims)}
			starts := 0
			f.claude.during = func() {
				if starts++; starts == 2 {
					tc.change(t, f)
				}
			}
			if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
				t.Fatal(err)
			}
			got := f.store.recs[key]
			if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "no PRD to review against") || !strings.Contains(got.LastOutcome.Summary, tc.want) {
				t.Fatalf("state %s outcome %+v", got.State, got.LastOutcome)
			}
			if len(f.claude.reviewed)+len(f.codex.reviewed) != 0 || len(got.Reviews) != 0 {
				t.Errorf("a review ran without the PRD")
			}
		})
	}
}

func TestReviewPRDReadsTheCurrentTextFromEitherSource(t *testing.T) {
	f := newFixture()
	body := task.PlanMarker(task.PRD, "0123456789ab") + "\n## PRD\n\nR1. Retry 5xx.\r\n"
	f.gh.comments = map[int64]IssueComment{77: {ID: 77, URL: "https://x/77", Body: body}}
	gh := &task.Record{Key: task.Key{Repo: "testorg/example", Number: 825}, Plan: []task.PlanComment{{Doc: task.PRD, CommentID: 77, URL: "https://x/77"}}}
	if got, err := f.app.reviewPRD(context.Background(), gh); err != nil || got != "R1. Retry 5xx." {
		t.Fatalf("GitHub PRD = %q, %v", got, err)
	}
	tracker := &lifecycleTracker{comments: []TrackerComment{{ID: "c-1", URL: "https://linear.app/comment/c-1", Body: body}}, missing: map[string]bool{}}
	f.app.Linear = tracker
	lin := &task.Record{Key: task.Key{Repo: "testorg/example", Source: task.Linear, ExternalID: "uuid-42"}, Identifier: "WEB-42",
		Plan: []task.PlanComment{{Doc: task.PRD, ExternalCommentID: "c-1", URL: "https://linear.app/comment/c-1"}}}
	if got, err := f.app.reviewPRD(context.Background(), lin); err != nil || got != "R1. Retry 5xx." {
		t.Fatalf("Linear PRD = %q, %v", got, err)
	}
	tracker.missing["c-1"] = true
	if _, err := f.app.reviewPRD(context.Background(), lin); !errors.Is(err, errPRDMissing) {
		t.Fatalf("deleted Linear PRD err = %v", err)
	}
	if _, err := f.app.reviewPRD(context.Background(), &task.Record{Key: gh.Key}); !errors.Is(err, errPRDMissing) || !strings.Contains(err.Error(), "no PRD comment on #825") {
		t.Fatalf("no recorded PRD err = %v", err)
	}
}

func TestAnEditedPRDIsReviewedAgainBeforeHandover(t *testing.T) {
	f := newFixture()
	key := claimTask(t, f)
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), claimedTurn("sid-2", retryClaims)}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	f.store.recs[key].Delegation.RunnerPID = 0
	setCI(f, task.CIStatus{State: task.CIPassing})
	editPRD(t, f, "R1. Retry 5xx.\nR2. Never retry 4xx.")
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.SelfReviewing || got.Delegation.PRDDigest != task.Digest("R1. Retry 5xx.\nR2. Never retry 4xx.") || len(f.runners.spawned) != 2 {
		t.Fatalf("state %s prd %q spawned %d; an edited PRD must start the reviews again", got.State, got.Delegation.PRDDigest, len(f.runners.spawned))
	}
	got.Delegation.Launch.PID = 0
	if err := f.run(context.Background(), key, 4343, io.Discard); err != nil {
		t.Fatal(err)
	}
	got = f.store.recs[key]
	if kinds(got) != "self competing self competing" || got.State != task.WaitingForCI {
		t.Fatalf("state %s reviews %s", got.State, kinds(got))
	}
	if p := f.codex.reviewed[1]; !strings.Contains(p, "> R2. Never retry 4xx.") {
		t.Errorf("the re-review did not read the edited PRD:\n%s", p)
	}
}

func TestAWaitingTaskWithTheSamePRDIsHandedOver(t *testing.T) {
	f := newFixture()
	key := claimTask(t, f)
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), claimedTurn("sid-2", retryClaims)}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	f.store.recs[key].Delegation.RunnerPID = 0
	setCI(f, task.CIStatus{State: task.CIPassing})
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.recs[key]; got.State != task.ReadyForHuman {
		t.Fatalf("state %s outcome %+v", got.State, got.LastOutcome)
	}
}

func TestABlockingLimitationSendsTheChangeBackAndIsStored(t *testing.T) {
	f := newFixture()
	key := claimTask(t, f)
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), claimedTurn("sid-2", retryClaims), claimedTurn("sid-2", retryClaims)}
	blocked := findings("blocking")
	blocked.Limitations = []task.Limitation{{Behavior: "retry under a real 503", Reason: "no network in review", Blocking: true}}
	f.claude.reviews = []agent.ReviewOutcome{blocked, {Clean: true, Summary: "ok", Limitations: []task.Limitation{{Behavior: "retry timing", Reason: "no clock control"}}}}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if kinds(got) != "self! self competing" {
		t.Fatalf("reviews %s", kinds(got))
	}
	if l := got.Reviews[0].Limitations; len(l) != 1 || !l[0].Blocking {
		t.Errorf("round 1 limitations = %+v", l)
	}
	if l := got.Reviews[1].Limitations; len(l) != 1 || l[0].Blocking || l[0].Reason != "no clock control" {
		t.Errorf("round 2 limitations = %+v", l)
	}
}

// localClaimTask starts #825 with claim verification and review_before_pr, and queues
// the plan and a review_ready turn of commit1 with retryClaims, then open.
func localClaimTask(t *testing.T, f fixture, open ...agent.Turn) task.Key {
	t.Helper()
	f.app.ReviewBeforePR = true
	key := claimTask(t, f)
	delete(f.gh.prs, 912)
	f.wt.checkpointHeads = []string{"commit1"}
	ready := reviewReadyTurn("sid-2", proposedBody)
	ready.Outcome.Claims = retryClaims
	f.claude.turns = append([]agent.Turn{plannedTurn("sid-1"), ready}, open...)
	return key
}

func TestALocalChangeIsReviewedWithItsClaimsAndOpensWithThem(t *testing.T) {
	f := newFixture()
	key := localClaimTask(t, f, claimedTurn("sid-2", retryClaims))
	opensPR(t, f, "commit1", proposedTitle, proposedBody, 1)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || got.Delegation.PR != 912 || got.ReviewPending() {
		t.Fatalf("state %s pr %d pending %t outcome %+v", got.State, got.Delegation.PR, got.ReviewPending(), got.LastOutcome)
	}
	if task.ClaimsDigest(got.Delegation.Claims) != task.ClaimsDigest(retryClaims) {
		t.Fatalf("claims = %+v", got.Delegation.Claims)
	}
	if p := f.codex.reviewed[0]; !strings.Contains(p, "the client retries 5xx responses") || !strings.Contains(p, "## PRD") {
		t.Errorf("the local review lacks the claims or the PRD:\n%s", p)
	}
	if open := f.claude.resumed[len(f.claude.resumed)-1]; !strings.Contains(open, `"claims"`) || !strings.Contains(open, "the client retries 5xx responses") {
		t.Errorf("the open turn was not given the reviewed claims to return:\n%s", open)
	}
}

func TestAnOpenTurnWithoutClaimsIsAskedForThemOnce(t *testing.T) {
	f := newFixture()
	key := localClaimTask(t, f, prTurn("sid-2"), claimedTurn("sid-2", retryClaims))
	opensPR(t, f, "commit1", proposedTitle, proposedBody, 1)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || got.Delegation.PR != 912 {
		t.Fatalf("state %s pr %d outcome %+v", got.State, got.Delegation.PR, got.LastOutcome)
	}
	last := f.claude.resumed[len(f.claude.resumed)-1]
	if !strings.Contains(last, "had no claims") || !strings.Contains(last, "reviewed claims unchanged") || !strings.Contains(last, "the client retries 5xx responses") {
		t.Fatalf("resumed = %q, want a request for the reviewed claims unchanged", f.claude.resumed)
	}
	if strings.Contains(last, "full updated set") {
		t.Errorf("the open turn was asked to write a new claim set:\n%s", last)
	}
}

func TestAnOpenTurnWithOtherClaimsFailsTheTask(t *testing.T) {
	f := newFixture()
	other := []task.Claim{{Criteria: []string{"R1"}, Behavior: "something else", Evidence: "x.go", Check: "none"}}
	key := localClaimTask(t, f, claimedTurn("sid-2", other))
	opensPR(t, f, "commit1", proposedTitle, proposedBody, 1)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "not the reviewed claims") {
		t.Fatalf("state %s outcome %+v", got.State, got.LastOutcome)
	}
	if task.ClaimsDigest(got.Delegation.Claims) != task.ClaimsDigest(retryClaims) {
		t.Fatalf("claims = %+v; the open turn replaced the reviewed claims", got.Delegation.Claims)
	}
}

func TestAPRDEditedBeforeTheOpeningIsReviewedBeforeThePROpens(t *testing.T) {
	f := newFixture()
	key := localClaimTask(t, f, claimedTurn("sid-2", retryClaims))
	opensPR(t, f, "commit1", proposedTitle, proposedBody, 2)
	f.codex.onReview = func() {
		if len(f.codex.reviewed) == 1 {
			editPRD(t, f, "R1. Retry 5xx.\nR2. Never retry 4xx.")
		}
	}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if kinds(got) != "self competing self competing" || got.State != task.WaitingForCI || got.Delegation.PR != 912 {
		t.Fatalf("state %s pr %d reviews %s outcome %+v", got.State, got.Delegation.PR, kinds(got), got.LastOutcome)
	}
	if p := f.codex.reviewed[1]; !strings.Contains(p, "> R2. Never retry 4xx.") {
		t.Errorf("the re-review did not read the edited PRD:\n%s", p)
	}
}

func TestADeletedPRDStopsTheOpening(t *testing.T) {
	f := newFixture()
	key := localClaimTask(t, f, claimedTurn("sid-2", retryClaims))
	opensPR(t, f, "commit1", proposedTitle, proposedBody, 1)
	f.codex.onReview = func() { delete(f.gh.comments, prdComment(t, f).ID) }
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || got.Delegation.PR != 0 || !strings.Contains(got.LastOutcome.Summary, "was deleted") {
		t.Fatalf("state %s pr %d outcome %+v", got.State, got.Delegation.PR, got.LastOutcome)
	}
	if openTurns(f) != 0 {
		t.Errorf("open turns = %d; the PR opened without its PRD", openTurns(f))
	}
}

func TestAClaimsRetryThatAsksAQuestionInsteadFailsTheTask(t *testing.T) {
	f := newFixture()
	key := claimTask(t, f)
	q := task.Question{Issue: "Retry 4xx?", Options: []task.Option{{Label: "A"}, {Label: "B"}}, Recommendation: "A"}
	blocked := agent.Turn{Session: task.Session{Agent: task.Claude, ID: "sid-2"}, Outcome: agent.Outcome{Status: agent.Blocked, Summary: "?", Questions: []task.Question{q}}}
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2"), blocked}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "without the claim set") || len(got.Decisions) != 0 {
		t.Fatalf("state %s outcome %+v decisions %d", got.State, got.LastOutcome, len(got.Decisions))
	}
}

func TestAWaitingTaskWhosePRDCannotBeReadFails(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(t *testing.T, f fixture)
		want   string
	}{
		"read error": {func(t *testing.T, f fixture) { f.gh.commentErr = errors.New("gh: HTTP 502") }, "HTTP 502"},
		"deleted":    {func(t *testing.T, f fixture) { delete(f.gh.comments, prdComment(t, f).ID) }, "was deleted"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			key := claimTask(t, f)
			f.claude.turns = []agent.Turn{plannedTurn("sid-1"), claimedTurn("sid-2", retryClaims)}
			if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
				t.Fatal(err)
			}
			f.store.recs[key].Delegation.RunnerPID = 0
			setCI(f, task.CIStatus{State: task.CIPassing})
			tc.change(t, f)
			if _, _, err := f.app.Status(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := f.store.recs[key]
			if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "PRD") || !strings.Contains(got.LastOutcome.Summary, tc.want) {
				t.Fatalf("state %s outcome %+v", got.State, got.LastOutcome)
			}
			if len(f.runners.spawned) != 1 {
				t.Errorf("spawned %d runners; a task without its PRD must not be reviewed again", len(f.runners.spawned))
			}
		})
	}
}

func TestAWaitingTaskChecksItsPRDWhenCICannotBeRead(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(t *testing.T, f fixture)
		want   task.State
	}{
		"deleted PRD": {func(t *testing.T, f fixture) { delete(f.gh.comments, prdComment(t, f).ID) }, task.Failed},
		"edited PRD":  {func(t *testing.T, f fixture) { editPRD(t, f, "R1. Retry 5xx.\nR2. Never retry 4xx.") }, task.SelfReviewing},
		"same PRD":    {func(t *testing.T, f fixture) {}, task.WaitingForCI},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			key := claimTask(t, f)
			f.claude.turns = []agent.Turn{plannedTurn("sid-1"), claimedTurn("sid-2", retryClaims)}
			if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
				t.Fatal(err)
			}
			f.store.recs[key].Delegation.RunnerPID = 0
			f.gh.ci = nil // every CI lookup fails
			tc.change(t, f)
			if _, _, err := f.app.Status(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := f.store.recs[key]; got.State != tc.want {
				t.Fatalf("state %s, want %s; outcome %+v", got.State, tc.want, got.LastOutcome)
			}
		})
	}
}
