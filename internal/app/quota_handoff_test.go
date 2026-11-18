package app

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

func TestQuotaPreflightPreventsEveryRunnerCall(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.app.Quota = fakeQuota{q: map[task.Agent]Quota{task.Claude: {Status: QuotaExhausted}}}
	a := f.app.meterAgents(key)
	_, err := a.Agents[task.Claude].Start(context.Background(), agent.StartInput{Log: io.Discard}, nil)
	if err == nil || !strings.Contains(err.Error(), "quota exhausted") || f.claude.starts != 0 || len(f.store.recs[key].Calls) != 0 {
		t.Fatalf("quota preflight failed: err=%v starts=%d calls=%d", err, f.claude.starts, len(f.store.recs[key].Calls))
	}
}

func TestQuotaDoesNotSwitchTheImplementer(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.app.Quota = fakeQuota{q: map[task.Agent]Quota{task.Claude: {Status: QuotaExhausted}, task.Codex: {Status: QuotaAvailable}}}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.Delegation.Implementer.Agent != task.Claude || f.codex.starts != 0 || got.State != task.Failed {
		t.Fatalf("implementer %s Codex starts %d state %s", got.Delegation.Implementer.Agent, f.codex.starts, got.State)
	}
}

func TestConflictRepairWaitsForQuotaWithoutSpendingAttempt(t *testing.T) {
	f := newFixture()
	key := repairing(t, f)
	quotas := map[task.Agent]Quota{task.Claude: {Status: QuotaExhausted}}
	f.app.Quota = fakeQuota{q: quotas}
	calls := len(f.store.recs[key].Calls)
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.ReadyForHuman || got.Delegation.ConflictPending ||
		len(got.Delegation.ConflictAttempts) != 0 || len(got.Calls) != calls {
		t.Fatalf("quota-blocked repair: state=%s pending=%v attempts=%d calls=%d", got.State,
			got.Delegation.ConflictPending, len(got.Delegation.ConflictAttempts), len(got.Calls)-calls)
	}
	spawned := len(f.runners.spawned)
	refresh(t, f)
	if len(f.runners.spawned) != spawned {
		t.Fatal("started another repair while quota was exhausted")
	}
	quotas[task.Claude] = Quota{Status: QuotaAvailable}
	refresh(t, f)
	if got := f.store.recs[key]; got.State != task.Implementing || !got.Delegation.ConflictPending ||
		len(got.Delegation.ConflictAttempts) != 1 || len(f.runners.spawned) != spawned+1 {
		t.Fatalf("repair did not start after quota returned: state=%s, outcome=%+v", got.State, got.LastOutcome)
	}
}
