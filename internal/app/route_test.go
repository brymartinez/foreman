package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/routing"
	"github.com/brymartinez/foreman/internal/task"
)

type fakeQuota struct {
	q   map[task.Agent]Quota
	err error
}

type fakeSelector struct {
	sel   agent.Selection
	err   error
	calls []agent.SelectInput
}

func (f *fakeSelector) Select(_ context.Context, in agent.SelectInput) (agent.Selection, error) {
	f.calls = append(f.calls, in)
	return f.sel, f.err
}

var known = fakeQuota{q: map[task.Agent]Quota{task.Claude: {Status: QuotaAvailable, Percent: 50}, task.Codex: {Status: QuotaAvailable, Percent: 50}}}

func routed(t *testing.T, sel agent.Selection, quota QuotaChecker) (fixture, *fakeSelector) {
	f := newFixture()
	s := &fakeSelector{sel: sel}
	f.app.Routing, f.app.Selector, f.app.Quota = starterRouting(t), s, quota
	f.app.RoutingDefaults = routing.Defaults{task.Claude: {Model: "opus", PlanModel: "sonnet"}}
	return f, s
}

func (f fakeQuota) Quota(context.Context) (map[task.Agent]Quota, error) { return f.q, f.err }

func starterRouting(t *testing.T) *routing.Config {
	t.Helper()
	c, err := routing.Load("../../defaults/routing.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRouteUsesConfiguredDefaultWithoutSelector(t *testing.T) {
	f := newFixture()
	f.app.Routing = starterRouting(t)
	f.app.RoutingDefaults = routing.Defaults{task.Claude: {Model: "opus", PlanModel: "sonnet"}}
	d, err := f.app.Route(context.Background(), RouteRequest{Repo: "testorg/example", Issue: 825})
	if err != nil || d.Profile == nil || d.Profile.Agent != task.Claude || d.Profile.Name != "claude" || !strings.Contains(d.Reason, "default") {
		t.Fatalf("decision = %+v, %v", d, err)
	}
	if f.app.Selector != nil {
		t.Fatal("routing should not require or call a selector")
	}
}

func TestRouteWaitsWhenConfiguredDefaultIsUnavailable(t *testing.T) {
	f := newFixture()
	f.app.Routing = starterRouting(t)
	f.app.Quota = fakeQuota{q: map[task.Agent]Quota{task.Claude: {Status: QuotaExhausted}}}
	d, err := f.app.Route(context.Background(), RouteRequest{})
	if err != nil || d.Profile != nil || !strings.Contains(d.Wait, "quota exhausted") {
		t.Fatalf("decision = %+v, %v", d, err)
	}
}

func TestRouteOffWhenNoConfig(t *testing.T) {
	if _, err := (App{}).Route(context.Background(), RouteRequest{}); !errors.Is(err, ErrRoutingOff) {
		t.Fatalf("got %v, want ErrRoutingOff", err)
	}
}

func TestRouteInvalidConfigIsError(t *testing.T) {
	f := newFixture()
	f.app.RoutingErr = errors.New("routing.yaml: default: unknown profile \"gpt\"")
	if _, err := f.app.Route(context.Background(), RouteRequest{}); err == nil || !strings.Contains(err.Error(), "routing.yaml") {
		t.Fatalf("got %v, want the load error", err)
	}
}
