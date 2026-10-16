package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

var codexRoute = app.Decision{
	Profile:   &task.Profile{Name: "codex", Agent: task.Codex, PlanModel: "gpt-6-luna", PlanReasoningEffort: "xhigh", Model: "gpt-6-sol", ReasoningEffort: "medium"},
	Reason:    "narrow, well-specified change",
	Selection: agent.Selection{Rule: 2},
}

func routingModel() Model {
	m := model()
	m.RouteOn = true
	return m
}

// routeTo presses Enter on the first inbox issue, then 3, then Enter in the context prompt.
func routeTo(t *testing.T, m *Model) Action {
	t.Helper()
	press(m, Key{Kind: KeyEnter}, rune_('3'))
	m.Input = "ctx.md"
	a := press(m, Key{Kind: KeyEnter})
	if a.Kind != ActionRoute || m.Mode != Routing {
		t.Fatalf("action %+v mode %v; want a route request", a, m.Mode)
	}
	return a
}

func TestChoosingAgentOffersRouteForNewIssue(t *testing.T) {
	m := routingModel()
	press(&m, Key{Kind: KeyEnter})
	if got := footer(m); !strings.Contains(got, "[3] Route") {
		t.Fatalf("footer = %q; want the route choice", got)
	}
}

func TestRouteNotOfferedForDelegatedIssueOrWhenRoutingIsOff(t *testing.T) {
	m := routingModel()
	m.Inbox[0].Delegated, m.Inbox[0].State = true, task.Failed
	press(&m, Key{Kind: KeyEnter})
	if got := footer(m); strings.Contains(got, "[3]") {
		t.Errorf("footer = %q; a delegated issue keeps its stored profile", got)
	}
	if press(&m, rune_('3')); m.Mode != ChoosingAgent {
		t.Errorf("3 on a delegated issue moved to mode %v", m.Mode)
	}

	off := model()
	press(&off, Key{Kind: KeyEnter})
	if got := footer(off); strings.Contains(got, "[3]") {
		t.Errorf("footer = %q with routing off", got)
	}
}

func TestKeys1And2StartWithoutRouting(t *testing.T) {
	for r, want := range map[rune]task.Agent{'1': task.Claude, '2': task.Codex} {
		m := routingModel()
		a := confirmBranch(t, &m, press(&m, Key{Kind: KeyEnter}, rune_(r), Key{Kind: KeyEnter}))
		if a.Kind != ActionStart || a.Start.Agent != want || a.Start.Profile != nil {
			t.Errorf("%c: action = %+v; want a start with %s and no profile", r, a, want)
		}
	}
}

func TestRouteKeyAsksForRouteAfterContext(t *testing.T) {
	m := routingModel()
	a := routeTo(t, &m)
	if a.Route.Repo != "a/b" || a.Route.Issue != 1 || a.Route.ContextFile != "ctx.md" {
		t.Errorf("route request = %+v", a.Route)
	}
	if got := footer(m); !strings.Contains(got, "Choosing a profile for a/b#1") {
		t.Errorf("footer = %q", got)
	}
}

func TestRoutedResultShowsProfileAndReason(t *testing.T) {
	m := routingModel()
	m.Width = 200
	a := routeTo(t, &m)
	m.Routed(a.RouteSeq, codexRoute, nil)
	if m.Mode != ConfirmingRoute {
		t.Fatalf("mode = %v", m.Mode)
	}
	got := footer(m)
	for _, want := range []string{"a/b#1", "codex", "plan gpt-6-luna xhigh", "implement gpt-6-sol medium", "narrow, well-specified change", "enter start", "esc back"} {
		if !strings.Contains(got, want) {
			t.Errorf("footer %q lacks %q", got, want)
		}
	}
}

func TestEnterConfirmsRouteAndStarts(t *testing.T) {
	m := routingModel()
	a := routeTo(t, &m)
	m.Routed(a.RouteSeq, codexRoute, nil)
	a = confirmBranch(t, &m, press(&m, Key{Kind: KeyEnter}))
	if a.Kind != ActionStart || a.Start.Profile != codexRoute.Profile || a.Start.RouteReason != codexRoute.Reason || a.Start.Agent != "" || a.Start.ContextFile != "ctx.md" {
		t.Fatalf("action = %+v", a)
	}
	if m.Mode != Browsing || !m.starting[key(1)] || !m.tried[key(1)] {
		t.Errorf("mode %v starting %v tried %v", m.Mode, m.starting, m.tried)
	}
}

func TestEscFromConfirmReturnsToAgentChoice(t *testing.T) {
	m := routingModel()
	a := routeTo(t, &m)
	m.Routed(a.RouteSeq, codexRoute, nil)
	if a := press(&m, Key{Kind: KeyEscape}); a.Kind != ActionNone || m.Mode != ChoosingAgent {
		t.Fatalf("action %+v mode %v; want the agent choice", a, m.Mode)
	}
	if a := confirmBranch(t, &m, press(&m, rune_('1'), Key{Kind: KeyEnter})); a.Kind != ActionStart || a.Start.Agent != task.Claude || a.Start.Profile != nil {
		t.Errorf("override after esc = %+v", a)
	}
}

func TestEscWhileRoutingIgnoresLateResult(t *testing.T) {
	m := routingModel()
	a := routeTo(t, &m)
	press(&m, Key{Kind: KeyEscape})
	if m.Mode != ChoosingAgent {
		t.Fatalf("mode = %v", m.Mode)
	}
	m.Routed(a.RouteSeq, codexRoute, nil)
	if m.Mode != ChoosingAgent {
		t.Errorf("a late route result moved the footer to mode %v", m.Mode)
	}
}

func TestWaitResultShowsReasonAndOnlyEscWorks(t *testing.T) {
	m := routingModel()
	a := routeTo(t, &m)
	m.Routed(a.RouteSeq, app.Decision{Wait: "claude and codex quota exhausted"}, nil)
	if got := footer(m); !strings.Contains(got, "claude and codex quota exhausted") || strings.Contains(got, "enter") {
		t.Errorf("footer = %q", got)
	}
	if a := press(&m, Key{Kind: KeyEnter}); a.Kind != ActionNone || m.Mode != ConfirmingRoute {
		t.Errorf("enter on a wait = %+v, mode %v", a, m.Mode)
	}
	if press(&m, Key{Kind: KeyEscape}); m.Mode != ChoosingAgent {
		t.Errorf("mode = %v", m.Mode)
	}
}

func TestRouteErrorShownAndEscReturns(t *testing.T) {
	m := routingModel()
	a := routeTo(t, &m)
	m.Routed(a.RouteSeq, app.Decision{}, errors.New(`routing.yaml: default: unknown profile "gpt"`))
	if got := footer(m); !strings.Contains(got, "unknown profile") {
		t.Errorf("footer = %q", got)
	}
	if a := press(&m, Key{Kind: KeyEnter}); a.Kind != ActionNone {
		t.Errorf("enter on an error = %+v", a)
	}
	if press(&m, Key{Kind: KeyEscape}); m.Mode != ChoosingAgent {
		t.Errorf("mode = %v", m.Mode)
	}
}

func TestRouteResultReachesTheModel(t *testing.T) {
	m := routingModel()
	a := routeTo(t, &m)
	r := route(context.Background(), &routeSource{d: codexRoute}, a.Route, a.RouteSeq)
	apply(&m, r)
	if m.Mode != ConfirmingRoute || !strings.Contains(footer(m), "codex") {
		t.Fatalf("mode %v footer %q", m.Mode, footer(m))
	}
}

type routeSource struct {
	stubSource
	d        app.Decision
	err      error
	proposal app.BranchProposal
	routed   []app.RouteRequest
	starts   []app.StartRequest
}

func (s *routeSource) ProposeBranch(_ context.Context, _ app.StartRequest) (app.BranchProposal, error) {
	if s.proposal.Name != "" {
		return s.proposal, nil
	}
	return app.BranchProposal{Name: "feat/1-x"}, nil
}

func (s *routeSource) Route(_ context.Context, req app.RouteRequest) (app.Decision, error) {
	s.routed = append(s.routed, req)
	return s.d, s.err
}

func (s *routeSource) Start(_ context.Context, req app.StartRequest) (*task.Record, error) {
	s.starts = append(s.starts, req)
	return &task.Record{Key: task.Key{Repo: req.Repo, Number: req.Issue}, State: task.Working,
		Delegation: &task.Delegation{Branch: "feat/1-x", Profile: req.Profile}}, nil
}

// settleAuto applies an auto start's result as the driver does.
func settleAuto(m *Model, r result) {
	m.Done(r.key)
	apply(m, r)
}

func TestAutoModeRoutesWithoutConfirmation(t *testing.T) {
	m := autoModel(3, assigned("a/b", 1, 0))
	m.RouteOn = true
	reqs := m.AutoStarts()
	if len(reqs) != 1 || reqs[0].Agent != "" {
		t.Fatalf("auto starts = %+v; want a request left for routing", reqs)
	}
	s := &routeSource{d: codexRoute}
	r := autoStart(context.Background(), s, reqs[0], nil)
	if r.err != nil || len(s.starts) != 1 || s.starts[0].Profile != codexRoute.Profile || s.starts[0].RouteReason != codexRoute.Reason || !s.starts[0].NewOnly {
		t.Fatalf("result %+v starts %+v", r, s.starts)
	}
	if !strings.Contains(r.message, "auto:") || !strings.Contains(r.message, "codex") {
		t.Errorf("message = %q", r.message)
	}
}

func TestAutoModeWaitsWhenRepositoryBranchIsUncertain(t *testing.T) {
	m := autoModel(3, assigned("a/b", 1, 0))
	reqs := m.AutoStarts()
	s := &routeSource{proposal: app.BranchProposal{Name: "bryan/db-1-x", Reason: "workstream unclear", Uncertain: true}}
	r := autoStart(context.Background(), s, reqs[0], nil)
	settleAuto(&m, r)
	if len(s.starts) != 0 || m.starting[key(1)] || !strings.Contains(m.Message, "workstream unclear") {
		t.Fatalf("result %+v starts %+v message %q", r, s.starts, m.Message)
	}
}

func TestAutoModeWaitReleasesSlotAndRetriesNextRefresh(t *testing.T) {
	m := autoModel(3, assigned("a/b", 1, 0))
	m.RouteOn = true
	reqs := m.AutoStarts()
	s := &routeSource{d: app.Decision{Wait: "codex quota exhausted", Selection: agent.Selection{Rule: 2}}}
	r := autoStart(context.Background(), s, reqs[0], nil)
	if len(s.starts) != 0 || r.wait != "codex quota exhausted" || !r.finished {
		t.Fatalf("result %+v starts %d", r, len(s.starts))
	}
	settleAuto(&m, r)
	if m.starting[key(1)] || !strings.Contains(m.Message, "codex quota exhausted") {
		t.Errorf("starting %v message %q", m.starting, m.Message)
	}
	if rows := inboxPane(m).rows; !strings.Contains(strings.Join(rows[0].lines, " "), "waiting: codex quota exhausted") {
		t.Errorf("row = %q", rows[0].lines)
	}
	if got := m.AutoStarts(); len(got) != 0 {
		t.Fatalf("retried %v before the next GitHub refresh", got)
	}
	apply(&m, result{report: &app.InboxReport{Current: []app.InboxEntry{assigned("a/b", 1, 0)}, SourcesFresh: true}})
	got := m.AutoStarts()
	if len(got) != 1 {
		t.Fatalf("auto starts after refresh = %v", got)
	}
	if prior := m.prior(key(1)); prior == nil || prior.Rule != 2 {
		t.Errorf("prior = %+v; want the earlier selection reused", prior)
	}
	s.d = codexRoute
	settleAuto(&m, autoStart(context.Background(), s, got[0], m.prior(key(1))))
	if s.routed[1].Prior == nil || s.routed[1].Prior.Rule != 2 {
		t.Errorf("second route = %+v; want the prior selection", s.routed[1])
	}
	if _, ok := m.waiting[key(1)]; ok {
		t.Error("a started issue is still marked waiting")
	}
}

func TestAutoModeRouteErrorIsShownAndNotRetried(t *testing.T) {
	m := autoModel(3, assigned("a/b", 1, 0))
	m.RouteOn = true
	reqs := m.AutoStarts()
	s := &routeSource{err: errors.New("routing.yaml: rules[0].when: empty")}
	r := autoStart(context.Background(), s, reqs[0], nil)
	settleAuto(&m, r)
	if len(s.starts) != 0 || !strings.Contains(m.Message, "rules[0].when") {
		t.Fatalf("starts %d message %q", len(s.starts), m.Message)
	}
	apply(&m, result{report: &app.InboxReport{Current: []app.InboxEntry{assigned("a/b", 1, 0)}}})
	if got := m.AutoStarts(); len(got) != 0 {
		t.Errorf("auto retried %v after a routing error", got)
	}
}

func TestAutoModeRoutingOffUsesAutoAgent(t *testing.T) {
	m := autoModel(3, assigned("a/b", 1, 0))
	reqs := m.AutoStarts()
	if len(reqs) != 1 || reqs[0].Agent != task.Claude {
		t.Fatalf("auto starts = %+v", reqs)
	}
	s := &routeSource{}
	autoStart(context.Background(), s, reqs[0], nil)
	if len(s.routed) != 0 || len(s.starts) != 1 || s.starts[0].Agent != task.Claude {
		t.Errorf("routed %d starts %+v", len(s.routed), s.starts)
	}
}
