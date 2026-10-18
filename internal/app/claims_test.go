package app

import (
	"context"
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
