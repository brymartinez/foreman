package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

func TestRunnerCountsPlanChecksAndRevisionsOnce(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{plannedTurn("sid"), plannedTurn("sid"), prTurn("next")}
	f.claude.turns[1].Outcome.Documents[task.PRD] += " Fixed rule."
	f.claude.scopeReviews = []agent.ReviewOutcome{
		{Usage: task.Usage{ClaudeInputTokens: 100, ClaudeOutputTokens: 20, ClaudeCostUSD: .1, ClaudeTurns: 1}, Findings: []task.Finding{{Summary: "missing rule"}}},
		{Clean: true, Usage: task.Usage{ClaudeInputTokens: 80, ClaudeCostUSD: .2, ClaudeTurns: 1}},
	}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	rec := f.store.recs[key]
	if rec.Usage.ClaudeInputTokens != 180 || rec.Usage.ClaudeOutputTokens != 20 || rec.Usage.ClaudeTurns != 4 {
		t.Fatalf("usage %+v", rec.Usage)
	}
	var checks, plans int
	for _, c := range rec.Calls {
		if c.Step == "plan_check" {
			checks++
		}
		if c.Step == "plan" {
			plans++
		}
	}
	if checks != 2 || plans != 2 {
		t.Fatalf("calls %+v", rec.Calls)
	}
}

func TestRunTokenLimitsStopBeforeAnotherCallAndSurviveRestart(t *testing.T) {
	for _, planning := range []bool{false, true} {
		t.Run(map[bool]string{false: "task", true: "planning"}[planning], func(t *testing.T) {
			f := newFixture()
			key := started(t, f, startReq(t))
			if planning {
				f.app.PlanningTokenLimit = 100
			} else {
				f.app.TaskTokenLimit = 100
			}
			f.claude.turns = []agent.Turn{plannedTurn("sid")}
			f.claude.turns[0].Usage = task.Usage{ClaudeInputTokens: 100, ClaudeTurns: 1}
			if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
				t.Fatal(err)
			}
			rec := f.store.recs[key]
			if rec.State != task.Failed || !strings.Contains(rec.LastOutcome.Summary, "token limit reached") || len(f.claude.scopeReviewed) != 0 || len(rec.Calls) != 1 {
				t.Fatalf("record %+v", rec)
			}
			a := f.app.meterAgents(key)
			_, _, err := a.callContext(context.Background(), key, "plan_check")
			if err == nil {
				t.Fatal("restart lost its token limit")
			}
		})
	}
}

type waitingAgent struct{ AgentAdapter }

func (waitingAgent) Review(ctx context.Context, in agent.StartInput) (agent.ReviewOutcome, error) {
	<-ctx.Done()
	return agent.ReviewOutcome{Usage: task.Usage{Model: "test", CodexInputTokens: 10, CodexTurns: 1}}, errors.Join(agent.ErrNoSession, ctx.Err())
}

func TestCallTimeoutPreservesFailedCallUsage(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.app.CallTimeout = time.Millisecond
	m := meteredAgent{AgentAdapter: waitingAgent{f.codex}, app: f.app, key: key}
	_, err := m.Review(context.Background(), agent.StartInput{Step: "review"})
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, agent.ErrNoSession) {
		t.Fatalf("error %v", err)
	}
	rec := f.store.recs[key]
	if rec.Usage.CodexInputTokens != 10 || len(rec.Calls) != 1 || rec.Calls[0].Error == "" || rec.Calls[0].Model != "test" {
		t.Fatalf("usage %+v calls %+v", rec.Usage, rec.Calls)
	}
}

func TestPlannerAndCheckerShareCorrectionPrecedence(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.gh.issueComments = []IssueComment{
		{ID: 1, URL: "https://example.test/1", Body: "## Triage decisions\nKeep data for 7 days. Preserve exports."},
		{ID: 2, URL: "https://example.test/2", Body: "## Triage correction\nKeep data for 14 days."},
	}
	f.claude.turns = []agent.Turn{plannedTurn("sid"), prTurn("next")}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{f.claude.prompts[0], f.claude.scopeReviewed[0]} {
		if !strings.Contains(prompt, agent.ConfirmedScopeRule) || !strings.Contains(prompt, "14 days") || !strings.Contains(prompt, "Preserve exports") {
			t.Fatalf("prompt %s", prompt)
		}
	}
}
