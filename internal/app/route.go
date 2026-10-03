package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// ErrRoutingOff means there is no routing.yaml, so dashboard starts use the agent
// the developer or auto.agent names.
var ErrRoutingOff = errors.New("routing is off")

// Limits on what the selector reads, in runes. Each input has its own budget, and
// with routing.MaxRulesRunes they sum to under 20000, so no input crowds out another
// and the prompt needs no overall cut.
const (
	routeTitleRunes    = 300
	routeLabelsRunes   = 500
	routeBodyRunes     = 6000
	routeComments      = 10
	routeCommentRunes  = 1000
	routeCommentsRunes = 5000 // all kept comments together
	routeContextRunes  = 4000
	routeReasonRunes   = 120
)

type RouteRequest struct {
	Repo        task.Repo
	Issue       int
	ContextFile string
	// Prior reuses an earlier selection, so a route retried after a wait does not
	// ask the selector again.
	Prior *agent.Selection
}

// Decision is a routing result. Exactly one of Profile and Wait is set.
type Decision struct {
	Profile *task.Profile
	Reason  string
	// Wait says why no suitable profile can run now.
	Wait string
	// Selection is the selector's answer, kept for a retry.
	Selection agent.Selection
}

// Route picks the profile a new task starts with. Fit comes first: the selector
// picks a rule, and only that rule's profiles are candidates, in the order the rule
// lists them. Quota then drops candidates whose agent is confirmed exhausted. When
// quota is unknown the default wins if it is a candidate.
func (a App) Route(ctx context.Context, req RouteRequest) (Decision, error) {
	if a.RoutingErr != nil {
		return Decision{}, a.RoutingErr
	}
	cfg := a.Routing
	if cfg == nil || a.Selector == nil {
		return Decision{}, ErrRoutingOff
	}
	sel, note := agent.Selection{}, ""
	if req.Prior != nil {
		sel = *req.Prior
	} else {
		prompt, err := a.routePrompt(ctx, req)
		if err != nil {
			return Decision{}, err
		}
		s := cfg.SelectorFor(a.RoutingDefaults)
		sel, err = a.Selector.Select(ctx, agent.SelectInput{Agent: s.Agent, Model: s.Model, ReasoningEffort: s.ReasoningEffort, Prompt: prompt})
		if err != nil {
			sel, note = agent.Selection{}, "selector failed: "+err.Error()
		}
	}
	if sel.Rule > len(cfg.Rules) {
		note = fmt.Sprintf("selector named unknown rule %d", sel.Rule)
		sel.Rule = 0
	}
	candidates, reason := []string{cfg.Default}, "default"
	switch {
	case sel.Rule > 0:
		rule := cfg.Rules[sel.Rule-1]
		candidates, reason = rule.Profiles, rule.Reason()
	case note != "":
		reason = note + "; default"
	default:
		reason = "no rule fits; default"
	}

	quotas := map[task.Agent]Quota{}
	if a.Quota != nil {
		if q, err := a.Quota.Quota(ctx); err == nil {
			quotas = q
		}
	}
	var runnable []string
	var exhausted []task.Agent
	unknown := false
	for _, name := range candidates {
		ag := cfg.Profiles[name].Agent
		switch quotas[ag].Status {
		case QuotaExhausted:
			if !slices.Contains(exhausted, ag) {
				exhausted = append(exhausted, ag)
			}
			continue
		case QuotaUnknown:
			unknown = true
		}
		runnable = append(runnable, name)
	}
	if len(runnable) == 0 {
		return Decision{Wait: quotaNote(exhausted), Reason: clip(reason, routeReasonRunes), Selection: sel}, nil
	}
	pick := runnable[0]
	if unknown && slices.Contains(runnable, cfg.Default) {
		pick = cfg.Default
	}
	if len(exhausted) > 0 {
		reason += "; " + quotaNote(exhausted)
	}
	if quotas[cfg.Profiles[pick].Agent].Status == QuotaUnknown {
		reason += "; quota unknown"
	}
	p := cfg.Resolve(pick, a.RoutingDefaults)
	return Decision{Profile: &p, Reason: clip(reason, routeReasonRunes), Selection: sel}, nil
}

func quotaNote(agents []task.Agent) string {
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = string(a)
	}
	return strings.Join(names, " and ") + " quota exhausted"
}

// routePrompt asks the selector for a rule number, giving it the rules, the issue,
// its recent comments, and the context file, each cut to its own budget.
func (a App) routePrompt(ctx context.Context, req RouteRequest) (string, error) {
	key := task.Key{Repo: req.Repo, Number: req.Issue}
	issue, err := a.GitHub.GetIssue(ctx, key)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("Pick the routing rule that fits this GitHub issue. The rules are in order. Answer with the number of the first rule whose description fits the work, or 0 if none fits or you are unsure. Give a reason of at most ten words. Do not use any tools.\n\nRules:\n")
	b.WriteString(a.Routing.RulesText())
	fmt.Fprintf(&b, "\nRepository: %s\nIssue: #%d %s\n", req.Repo, req.Issue, clip(oneLine(issue.Title), routeTitleRunes))
	if len(issue.Labels) > 0 {
		fmt.Fprintf(&b, "Labels: %s\n", clip(strings.Join(issue.Labels, ", "), routeLabelsRunes))
	}
	fmt.Fprintf(&b, "\nBody:\n%s\n", clip(issue.Body, routeBodyRunes))
	// A failed comment read leaves the selector with the issue alone.
	comments, _ := a.GitHub.IssueComments(ctx, key)
	comments = slices.DeleteFunc(comments, func(c IssueComment) bool {
		return strings.HasPrefix(strings.TrimSpace(c.Body), "<!-- foreman:")
	})
	// The newest comments are kept, as many as fit the count and the budget.
	var kept []string
	used := 0
	for i := len(comments) - 1; i >= 0 && len(kept) < routeComments; i-- {
		body := clip(comments[i].Body, routeCommentRunes)
		if used += utf8.RuneCountInString(body); used > routeCommentsRunes {
			break
		}
		kept = append(kept, body)
	}
	if len(kept) > 0 {
		b.WriteString("\nComments, oldest first:\n")
		for _, body := range slices.Backward(kept) {
			fmt.Fprintf(&b, "---\n%s\n", body)
		}
	}
	if req.ContextFile != "" {
		raw, err := os.ReadFile(req.ContextFile)
		if err != nil {
			return "", fmt.Errorf("context file: %w", err)
		}
		fmt.Fprintf(&b, "\nContext from the developer:\n%s\n", clip(string(raw), routeContextRunes))
	}
	return b.String(), nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// clip cuts s to at most n runes, ending in "…" when it was cut.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
