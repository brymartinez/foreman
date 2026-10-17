package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/routing"
	"github.com/brymartinez/foreman/internal/task"
)

type fakeSelector struct {
	sel   agent.Selection
	err   error
	calls []agent.SelectInput
}

func (f *fakeSelector) Select(_ context.Context, in agent.SelectInput) (agent.Selection, error) {
	f.calls = append(f.calls, in)
	return f.sel, f.err
}

type fakeQuota struct {
	q   map[task.Agent]Quota
	err error
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

var known = fakeQuota{q: map[task.Agent]Quota{task.Claude: {Status: QuotaAvailable, Percent: 50}, task.Codex: {Status: QuotaAvailable, Percent: 50}}}

func routed(t *testing.T, sel agent.Selection, quota QuotaChecker) (fixture, *fakeSelector) {
	f := newFixture()
	s := &fakeSelector{sel: sel}
	f.app.Routing, f.app.Selector, f.app.Quota = starterRouting(t), s, quota
	f.app.RoutingDefaults = routing.Defaults{task.Claude: {Model: "opus", PlanModel: "sonnet"}}
	return f, s
}

var routeReq = RouteRequest{Repo: "testorg/example", Issue: 825}

func TestRoutePicksRuleProfile(t *testing.T) {
	cases := []struct {
		title       string
		rule        int
		profile     task.Agent
		planModel   string
		reasonStart string
	}{
		{"Refactor the task store into packages", 1, task.Claude, "sonnet", "broad or ambiguous design work"},
		{"Fix typo in README", 2, task.Codex, "gpt-6-luna", "narrow, well-specified change"},
	}
	for _, c := range cases {
		t.Run(c.title, func(t *testing.T) {
			f, s := routed(t, agent.Selection{Rule: c.rule, Reason: "fits"}, known)
			f.gh.issue.Title = c.title
			d, err := f.app.Route(context.Background(), routeReq)
			if err != nil {
				t.Fatal(err)
			}
			if d.Profile == nil || d.Profile.Agent != c.profile || d.Profile.PlanModel != c.planModel || !strings.HasPrefix(d.Reason, c.reasonStart) || d.Wait != "" {
				t.Fatalf("decision = %+v / %+v", d, d.Profile)
			}
			if len(s.calls) != 1 || s.calls[0].Agent != task.Claude || s.calls[0].Model != "sonnet" || !strings.Contains(s.calls[0].Prompt, c.title) {
				t.Errorf("selector calls = %+v", s.calls)
			}
		})
	}
}

func TestRoutePromptListsRulesAndIssue(t *testing.T) {
	f, s := routed(t, agent.Selection{Rule: 1}, known)
	f.gh.issue.Labels = []string{"refactor", "area/store"}
	if _, err := f.app.Route(context.Background(), routeReq); err != nil {
		t.Fatal(err)
	}
	p := s.calls[0].Prompt
	for _, want := range []string{"1. Broad or ambiguous", "2. Narrow, well-specified", "3. Any other work.", "testorg/example", "#825 Provider retries", "refactor, area/store", "Retry failed requests."} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
}

func TestRoutePromptIsBounded(t *testing.T) {
	f, s := routed(t, agent.Selection{Rule: 1}, known)
	f.gh.issue.Body = strings.Repeat("b", 9000) + "BODYEND"
	for i := range 15 {
		f.gh.issueComments = append(f.gh.issueComments, IssueComment{ID: int64(i), Body: fmt.Sprintf("comment-%02d ", i) + strings.Repeat("c", 1500)})
	}
	f.gh.issueComments = append(f.gh.issueComments, IssueComment{ID: 99, Body: "<!-- foreman:analysis -->\n## Analysis\nPLANTEXT"})
	ctxFile := filepath.Join(t.TempDir(), "ctx.md")
	os.WriteFile(ctxFile, []byte(strings.Repeat("x", 6000)+"CTXEND"), 0o644)
	req := routeReq
	req.ContextFile = ctxFile
	if _, err := f.app.Route(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	p := s.calls[0].Prompt
	if strings.Contains(p, "BODYEND") || strings.Contains(p, "CTXEND") || strings.Contains(p, "PLANTEXT") || strings.Contains(p, strings.Repeat("c", 1001)) {
		t.Error("body, context file, or comments were not cut, or a foreman comment was included")
	}
	if strings.Contains(p, "comment-09") || !strings.Contains(p, "comment-10") || !strings.Contains(p, "comment-14") {
		t.Error("want the most recent comments that fit the comment budget")
	}
	if n := utf8.RuneCountInString(p); n >= 20000 {
		t.Errorf("prompt is %d runes", n)
	}
}

func TestRoutePromptKeepsEveryInputWhenAllAreAtTheirLimits(t *testing.T) {
	f, s := routed(t, agent.Selection{Rule: 1}, known)
	f.app.Routing.Rules = nil
	for range 40 {
		f.app.Routing.Rules = append(f.app.Routing.Rules, routing.Rule{When: strings.Repeat("r", 55), Profiles: []string{"claude"}})
	}
	if err := f.app.Routing.Validate(); err != nil {
		t.Fatalf("rules near the limit must be valid: %v", err)
	}
	f.gh.issue.Title = strings.Repeat("t", 1000)
	for i := range 100 {
		f.gh.issue.Labels = append(f.gh.issue.Labels, fmt.Sprintf("label-%02d-%s", i, strings.Repeat("l", 20)))
	}
	f.gh.issue.Body = strings.Repeat("b", 9000)
	for i := range 15 {
		f.gh.issueComments = append(f.gh.issueComments, IssueComment{ID: int64(i), Body: fmt.Sprintf("comment-%02d ", i) + strings.Repeat("c", 1500)})
	}
	ctxFile := filepath.Join(t.TempDir(), "ctx.md")
	os.WriteFile(ctxFile, []byte(strings.Repeat("x", 6000)), 0o644)
	req := routeReq
	req.ContextFile = ctxFile
	if _, err := f.app.Route(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	p := s.calls[0].Prompt
	if n := utf8.RuneCountInString(p); n >= 20000 {
		t.Errorf("prompt is %d runes", n)
	}
	for name, want := range map[string]string{
		"last rule":    "40. " + strings.Repeat("r", 55),
		"body":         strings.Repeat("b", 5999) + "…",
		"newest":       "comment-14",
		"context file": strings.Repeat("x", 3999) + "…",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lost the %s", name)
		}
	}
}

func TestRouteCommentReadErrorIsReturnedWithoutSelecting(t *testing.T) {
	f, s := routed(t, agent.Selection{Rule: 2}, known)
	f.gh.issueCommentsErr = errors.New("gh: HTTP 502")
	d, err := f.app.Route(context.Background(), routeReq)
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") || d.Profile != nil {
		t.Fatalf("decision %+v, err %v; want the read error and no profile", d, err)
	}
	if len(s.calls) != 0 {
		t.Error("the selector ran without the issue's comments")
	}
}

func TestRouteUncertainUsesDefault(t *testing.T) {
	f, _ := routed(t, agent.Selection{Rule: 0, Reason: "unclear"}, known)
	d, err := f.app.Route(context.Background(), routeReq)
	if err != nil || d.Profile == nil || d.Profile.Name != "claude" || !strings.Contains(d.Reason, "default") {
		t.Fatalf("decision = %+v, %v", d, err)
	}
}

func TestRouteSelectorErrorUsesDefault(t *testing.T) {
	cases := map[string]struct {
		sel  agent.Selection
		err  error
		want string
	}{
		"call fails":   {err: errors.New("claude exited"), want: "selector failed"},
		"bad reply":    {err: errors.New("selection is not valid JSON"), want: "selector failed"},
		"unknown rule": {sel: agent.Selection{Rule: 9}, want: "unknown rule 9"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f, s := routed(t, c.sel, known)
			s.err = c.err
			d, err := f.app.Route(context.Background(), routeReq)
			if err != nil || d.Profile == nil || d.Profile.Name != "claude" || !strings.Contains(d.Reason, c.want) || !strings.Contains(d.Reason, "default") {
				t.Fatalf("decision = %+v, %v", d, err)
			}
		})
	}
}

func TestRouteSkipsExhaustedProfile(t *testing.T) {
	f, _ := routed(t, agent.Selection{Rule: 3}, fakeQuota{q: map[task.Agent]Quota{task.Claude: {Status: QuotaExhausted}, task.Codex: {Status: QuotaAvailable, Percent: 40}}})
	d, err := f.app.Route(context.Background(), routeReq)
	if err != nil || d.Profile == nil || d.Profile.Name != "codex" || !strings.Contains(d.Reason, "claude quota exhausted") {
		t.Fatalf("decision = %+v, %v", d, err)
	}
}

func TestRouteSkipsNotSetUpAgentForAvailableFallback(t *testing.T) {
	f, _ := routed(t, agent.Selection{Rule: 3}, fakeQuota{q: map[task.Agent]Quota{task.Claude: {Status: QuotaNotSetUp}, task.Codex: {Status: QuotaAvailable, Percent: 40}}})
	d, err := f.app.Route(context.Background(), routeReq)
	if err != nil || d.Profile == nil || d.Profile.Name != "codex" || !strings.Contains(d.Reason, "claude not set up") {
		t.Fatalf("decision = %+v, %v", d, err)
	}
}

func TestRouteWaitsWhenOnlyRuleProfileIsNotSetUp(t *testing.T) {
	f, _ := routed(t, agent.Selection{Rule: 2}, fakeQuota{q: map[task.Agent]Quota{task.Codex: {Status: QuotaNotSetUp}, task.Claude: {Status: QuotaAvailable, Percent: 90}}})
	d, err := f.app.Route(context.Background(), routeReq)
	if err != nil || d.Profile != nil || d.Wait != "codex not set up" || d.Selection.Rule != 2 {
		t.Fatalf("decision = %+v, %v", d, err)
	}
}

func TestRouteWaitsWhenAllCandidatesAreUnavailableForDifferentReasons(t *testing.T) {
	f, _ := routed(t, agent.Selection{Rule: 3}, fakeQuota{q: map[task.Agent]Quota{task.Claude: {Status: QuotaNotSetUp}, task.Codex: {Status: QuotaExhausted}}})
	d, err := f.app.Route(context.Background(), routeReq)
	if err != nil || d.Profile != nil || !strings.Contains(d.Wait, "claude not set up") || !strings.Contains(d.Wait, "codex quota exhausted") {
		t.Fatalf("decision = %+v, %v", d, err)
	}
}

func TestRouteWaitsWhenAllCandidatesExhausted(t *testing.T) {
	f, _ := routed(t, agent.Selection{Rule: 3}, fakeQuota{q: map[task.Agent]Quota{task.Claude: {Status: QuotaExhausted}, task.Codex: {Status: QuotaExhausted}}})
	d, err := f.app.Route(context.Background(), routeReq)
	if err != nil || d.Profile != nil || !strings.Contains(d.Wait, "claude") || !strings.Contains(d.Wait, "codex") {
		t.Fatalf("decision = %+v, %v", d, err)
	}
	if d.Selection.Rule != 3 {
		t.Errorf("selection = %+v, want it kept for the retry", d.Selection)
	}
}

func TestRouteNoRuleAndDefaultExhaustedWaits(t *testing.T) {
	f, _ := routed(t, agent.Selection{Rule: 0}, fakeQuota{q: map[task.Agent]Quota{task.Claude: {Status: QuotaExhausted}, task.Codex: {Status: QuotaAvailable, Percent: 90}}})
	d, err := f.app.Route(context.Background(), routeReq)
	if err != nil || d.Profile != nil || !strings.Contains(d.Wait, "claude") {
		t.Fatalf("decision = %+v, %v; other profiles are tried only when a rule lists them", d, err)
	}
}

func TestRouteSameRuleExhaustedOnlyCandidateWaits(t *testing.T) {
	f, _ := routed(t, agent.Selection{Rule: 2}, fakeQuota{q: map[task.Agent]Quota{task.Codex: {Status: QuotaExhausted}, task.Claude: {Status: QuotaAvailable, Percent: 90}}})
	d, err := f.app.Route(context.Background(), routeReq)
	if err != nil || d.Profile != nil || !strings.Contains(d.Wait, "codex") {
		t.Fatalf("decision = %+v, %v", d, err)
	}
}

func TestRouteQuotaUnknownUsesDefault(t *testing.T) {
	f, _ := routed(t, agent.Selection{Rule: 3}, fakeQuota{err: errors.New("quota-axi is not installed")})
	f.app.Routing.Rules[2].Profiles = []string{"codex", "claude"}
	d, err := f.app.Route(context.Background(), routeReq)
	if err != nil || d.Profile == nil || d.Profile.Name != "claude" || !strings.Contains(d.Reason, "quota unknown") {
		t.Fatalf("decision = %+v, %v", d, err)
	}
}

func TestRouteQuotaUnknownDefaultNotCandidateUsesFirst(t *testing.T) {
	f, _ := routed(t, agent.Selection{Rule: 2}, fakeQuota{q: map[task.Agent]Quota{}})
	d, err := f.app.Route(context.Background(), routeReq)
	if err != nil || d.Profile == nil || d.Profile.Name != "codex" || !strings.Contains(d.Reason, "quota unknown") {
		t.Fatalf("decision = %+v, %v", d, err)
	}
}

func TestRouteNoQuotaCheckerUsesDefault(t *testing.T) {
	f, _ := routed(t, agent.Selection{Rule: 3}, nil)
	f.app.Routing.Rules[2].Profiles = []string{"codex", "claude"}
	d, err := f.app.Route(context.Background(), routeReq)
	if err != nil || d.Profile == nil || d.Profile.Name != "claude" || !strings.Contains(d.Reason, "quota unknown") {
		t.Fatalf("decision = %+v, %v", d, err)
	}
}

func TestRouteReusesPriorSelection(t *testing.T) {
	f, s := routed(t, agent.Selection{Rule: 1}, known)
	req := routeReq
	req.Prior = &agent.Selection{Rule: 2, Reason: "earlier"}
	d, err := f.app.Route(context.Background(), req)
	if err != nil || len(s.calls) != 0 || d.Profile == nil || d.Profile.Name != "codex" {
		t.Fatalf("decision = %+v, %v, selector calls %d", d, err, len(s.calls))
	}
}

func TestRouteOffWhenNoConfig(t *testing.T) {
	f := newFixture()
	f.app.Selector = &fakeSelector{}
	if _, err := f.app.Route(context.Background(), routeReq); !errors.Is(err, ErrRoutingOff) {
		t.Fatalf("got %v, want ErrRoutingOff", err)
	}
}

func TestRouteInvalidConfigIsError(t *testing.T) {
	f, s := routed(t, agent.Selection{Rule: 1}, known)
	f.app.Routing, f.app.RoutingErr = nil, errors.New("routing.yaml: default: unknown profile \"gpt\"")
	if _, err := f.app.Route(context.Background(), routeReq); err == nil || errors.Is(err, ErrRoutingOff) || !strings.Contains(err.Error(), "routing.yaml") {
		t.Fatalf("got %v, want the load error", err)
	}
	if len(s.calls) != 0 {
		t.Error("selector called with an invalid config")
	}
}

func TestRouteReasonIsShort(t *testing.T) {
	f, _ := routed(t, agent.Selection{Rule: 1}, known)
	f.app.Routing.Rules[0].Why = strings.Repeat("w", 300)
	d, err := f.app.Route(context.Background(), routeReq)
	if err != nil || utf8.RuneCountInString(d.Reason) > 120 {
		t.Fatalf("reason %q, %v", d.Reason, err)
	}
}
