package app

import (
	"context"
	"testing"

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
