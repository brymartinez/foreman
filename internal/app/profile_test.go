package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

var codexProfile = task.Profile{Name: "codex", Agent: task.Codex, PlanModel: "gpt-6-luna", PlanReasoningEffort: "xhigh", Model: "gpt-6-sol", ReasoningEffort: "medium"}

func routedReq(t *testing.T) StartRequest {
	req := startReq(t)
	p := codexProfile
	req.Agent, req.Profile, req.RouteReason = "", &p, "narrow, well-specified change"
	return req
}

func TestStartSavesProfileAndReason(t *testing.T) {
	f := newFixture()
	rec, err := f.app.Start(context.Background(), routedReq(t))
	if err != nil {
		t.Fatal(err)
	}
	d := rec.Delegation
	if d.Implementer.Agent != task.Codex || d.Reviewer != task.Claude {
		t.Errorf("implementer %s reviewer %s, want the profile's agent and its competitor", d.Implementer.Agent, d.Reviewer)
	}
	if d.Profile == nil || *d.Profile != codexProfile || d.RouteReason != "narrow, well-specified change" || d.RouteLogged {
		t.Errorf("profile %+v reason %q logged %v", d.Profile, d.RouteReason, d.RouteLogged)
	}
}

func TestStartRejectsProfileAgentMismatch(t *testing.T) {
	f := newFixture()
	req := routedReq(t)
	req.Agent = task.Claude
	if _, err := f.app.Start(context.Background(), req); err == nil || !strings.Contains(err.Error(), "profile") {
		t.Fatalf("got %v, want a profile mismatch error", err)
	}
}

func failedRouted(f fixture) task.Key {
	key := task.Key{Repo: "testorg/example", Number: 825}
	p := codexProfile
	f.store.recs[key] = &task.Record{Key: key, Kind: task.Issue, State: task.Failed,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Codex, ID: "th-1"}, Profile: &p, RouteReason: "narrow", RouteLogged: true, Branch: "feat/825-provider-retries"}}
	return key
}

func TestRestartKeepsProfileForSameAgent(t *testing.T) {
	f := newFixture()
	failedRouted(f)
	req := startReq(t)
	req.Agent = task.Codex
	rec, err := f.app.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if d := rec.Delegation; d.Profile == nil || *d.Profile != codexProfile || d.RouteReason != "narrow" || !d.RouteLogged {
		t.Errorf("profile %+v reason %q logged %v, want the stored route kept", d.Profile, d.RouteReason, d.RouteLogged)
	}
}

func TestRestartWithOtherAgentDropsProfile(t *testing.T) {
	f := newFixture()
	failedRouted(f)
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if d := rec.Delegation; d.Implementer.Agent != task.Claude || d.Profile != nil || d.RouteReason != "" {
		t.Errorf("implementer %s profile %+v reason %q, want claude with no profile", d.Implementer.Agent, d.Profile, d.RouteReason)
	}
}

func TestRunTaskWritesRouteLineOnce(t *testing.T) {
	f := newFixture()
	rec, err := f.app.Start(context.Background(), routedReq(t))
	if err != nil {
		t.Fatal(err)
	}
	f.codex.turn = agent.Turn{Session: task.Session{Agent: task.Codex, ID: "th-1"}, Outcome: agent.Outcome{Status: agent.FailedRun, Summary: "stuck"}}
	var log bytes.Buffer
	if err := f.run(context.Background(), rec.Key, 4242, &log); err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(log.String(), "\n")
	if first != "route:codex:codex:narrow, well-specified change" {
		t.Errorf("first log line = %q", first)
	}
	if !f.store.recs[rec.Key].Delegation.RouteLogged {
		t.Error("route_logged not saved")
	}

	// A retry with the same agent keeps the profile and does not log it again.
	req := startReq(t)
	req.Agent = task.Codex
	if _, err := f.app.Start(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	log.Reset()
	if err := f.run(context.Background(), rec.Key, 4242, &log); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(log.String(), "route:") {
		t.Errorf("retry logged the route again: %q", log.String())
	}
}

func TestRouteLineKeepsTheReasonOnOneLine(t *testing.T) {
	f := newFixture()
	req := routedReq(t)
	req.RouteReason = "first\nsecond"
	rec, err := f.app.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	f.codex.turn = agent.Turn{Outcome: agent.Outcome{Status: agent.FailedRun, Summary: "stuck"}}
	var log bytes.Buffer
	if err := f.run(context.Background(), rec.Key, 4242, &log); err != nil {
		t.Fatal(err)
	}
	if first, _, _ := strings.Cut(log.String(), "\n"); first != "route:codex:codex:first second" {
		t.Errorf("first log line = %q", first)
	}
}

func TestImplementTurnCarriesProfile(t *testing.T) {
	f := newFixture()
	key := failedRouted(f)
	f.store.recs[key].State = task.Working
	f.store.recs[key].Decisions = []task.Decision{{Question: task.Question{Issue: "q"}, Answer: "A", AnsweredAt: now}}
	f.codex.turn = agent.Turn{Outcome: agent.Outcome{Status: agent.FailedRun, Summary: "stuck"}}
	if err := f.run(context.Background(), key, 4242, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(f.codex.resumed) != 1 || len(f.codex.profiles) != 1 || f.codex.profiles[0] == nil || *f.codex.profiles[0] != codexProfile {
		t.Errorf("resumed %v with profiles %v, want the stored profile", f.codex.resumed, f.codex.profiles)
	}
}
