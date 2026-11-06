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
