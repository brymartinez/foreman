package app

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// confirmedScope is the maintainer's current scope in issue-comment order.
// Ordinary discussion and planning comments never change it.
type confirmedScope struct {
	text   string
	digest string
}

func (a App) readScope(ctx context.Context, key task.Key) (confirmedScope, error) {
	issue, err := a.issue(ctx, key)
	if err != nil {
		return confirmedScope{}, fmt.Errorf("reading issue for confirmed scope: %w", err)
	}
	if !slices.Contains(issue.Labels, "triaged") {
		return confirmedScope{}, fmt.Errorf("%s no longer has the triaged label; restore it before restarting", issueLabel(key, issue.Identifier))
	}
	comments, err := a.issueComments(ctx, key)
	if err != nil {
		return confirmedScope{}, fmt.Errorf("reading triage comments: %w", err)
	}
	var parts []string
	for _, c := range comments {
		body := strings.TrimSpace(strings.ReplaceAll(c.Body, "\r\n", "\n"))
		if !strings.HasPrefix(body, "## Triage decisions\n") && body != "## Triage decisions" &&
			!strings.HasPrefix(body, "## Triage correction\n") && body != "## Triage correction" {
			continue
		}
		_, decision, hasDecision := strings.Cut(body, "\n")
		if !hasDecision || strings.TrimSpace(decision) == "" || c.URL == "" {
			return confirmedScope{}, fmt.Errorf("triage comment %s has no readable decision or source URL", c.ID)
		}
		parts = append(parts, fmt.Sprintf("Source: %s\n%s", c.URL, body))
	}
	if len(parts) == 0 {
		if strings.TrimSpace(issue.Body) == "" || issue.URL == "" {
			return confirmedScope{}, fmt.Errorf("%s has no readable confirmed scope in triage comments or description", issueLabel(key, issue.Identifier))
		}
		parts = append(parts, fmt.Sprintf("Source: %s\n%s", issue.URL, strings.TrimSpace(issue.Body)))
	}
	text := strings.Join(parts, "\n\n")
	return confirmedScope{text: text, digest: task.Digest(text)}, nil
}

func planDigest(texts map[task.PlanDoc]string) string {
	var s strings.Builder
	for _, doc := range task.PlanDocs {
		fmt.Fprintf(&s, "%s\n%s\n", doc, strings.TrimSpace(texts[doc]))
	}
	return task.Digest(s.String())
}

// legacyPlanDigest identifies a clean four-comment plan made before Requirements
// and Work plan became the only required documents.
func legacyPlanDigest(plan issuePlan) string {
	var s strings.Builder
	for _, doc := range []task.PlanDoc{task.Analysis, task.PRD, task.Design, task.ImplementationPlan} {
		found, ok := plan.found[doc]
		if !ok {
			return ""
		}
		fmt.Fprintf(&s, "%s\n%s\n", doc, strings.TrimSpace(found.text))
	}
	return task.Digest(s.String())
}

// checkPlan asks a fresh read-only agent to compare both documents with the
// confirmed scope. A blocking limitation or an error is not agreement.
func (a App) checkPlan(ctx context.Context, rec *task.Record, scope confirmedScope, texts map[task.PlanDoc]string, sources issuePlan, log io.Writer) (agent.ReviewOutcome, error) {
	checker, ok := a.Agents[rec.Delegation.Implementer.Agent]
	if !ok {
		return agent.ReviewOutcome{}, fmt.Errorf("scope checker %s is not configured", rec.Delegation.Implementer.Agent)
	}
	var prompt strings.Builder
	prompt.WriteString("You are checking planning documents against confirmed triage. Compare only the supplied text; do not inspect code, run commands, or follow links. Return the review JSON schema. A clean verdict means Requirements states every confirmed rule and check, and Work plan gives a clear step for each requirement ID. Both documents must use short, plain English; exact code names are fine. Together, the documents must be enough to build and review the change without the issue or triage. If a rule is missing or the documents disagree, return changes_requested with a short finding that names the rule, document, gap, and source URL. If the supplied text is unclear, report a blocking limitation; never guess. Ordinary discussion is not a requirement. Keep output short: one sentence in summary and only findings that need action. Do not repeat rules that match.\n\n## Confirmed scope\n\n")
	prompt.WriteString(agent.ConfirmedScopeRule + "\n\n")
	prompt.WriteString(scope.text)
	prompt.WriteString("\n\n## Planning documents\n\n")
	for _, doc := range task.PlanDocs {
		ref := "proposed document (not posted yet)"
		if found, ok := sources.found[doc]; ok && found.text == texts[doc] {
			ref = found.comment.URL
		}
		fmt.Fprintf(&prompt, "### %s\nSource: %s\n\n%s\n\n", doc.Title(), ref, texts[doc])
	}
	out, err := checker.Review(ctx, agent.StartInput{Worktree: rec.Delegation.Worktree, Log: log, Prompt: prompt.String(), Step: agent.StepReview, Purpose: "plan_check", MaxBudgetUSD: a.ReviewerBudgetUSD})
	if !a.metered {
		_, saveErr := a.update(context.WithoutCancel(ctx), rec.Key, existing(func(cur *task.Record) { addUsage(&cur.Usage, out.Usage) }))
		if saveErr != nil {
			return out, saveErr
		}
	}
	if err != nil {
		return out, fmt.Errorf("checking the plan against confirmed scope failed: %w", err)
	}
	for _, limitation := range out.Limitations {
		if limitation.Blocking {
			return out, fmt.Errorf("plan agreement could not be verified: %s: %s", limitation.Behavior, limitation.Reason)
		}
	}
	return out, nil
}

const scopePlanConflictIssue = "The planning documents conflict with confirmed triage. How should this be resolved?"

func scopePlanAnswer(rec *task.Record, scopeDigest, planDigest string) string {
	key := scopeDigest + ":" + planDigest
	for i := len(rec.Decisions) - 1; i >= 0; i-- {
		d := rec.Decisions[i]
		if d.Question.Issue == scopePlanConflictIssue && strings.HasSuffix(d.Question.Context, key) {
			return d.Answer
		}
	}
	return ""
}

func askScopePlanConflict(rec *task.Record, scope confirmedScope, plan issuePlan, checked agent.ReviewOutcome, now time.Time) {
	var context strings.Builder
	context.WriteString("Confirmed triage:\n" + scope.text + "\n\nPlanning documents:")
	for _, doc := range task.PlanDocs {
		if f, ok := plan.found[doc]; ok {
			ref := f.comment.URL
			if ref == "" {
				ref = "proposed document (not posted yet)"
			}
			fmt.Fprintf(&context, "\n- %s: %s", doc.Title(), ref)
		}
	}
	context.WriteString("\n\nChecker findings:")
	for _, finding := range checked.Findings {
		fmt.Fprintf(&context, "\n- %s: %s", finding.Summary, finding.Detail)
	}
	fingerprint := planDigest(plan.texts())
	fmt.Fprintf(&context, "\n\nCheck: %s:%s", scope.digest, fingerprint)
	rec.State = task.NeedsDecision
	rec.Delegation.Implementer.ID = ""
	rec.LastOutcome = &task.Outcome{Status: "blocked", Summary: "planning documents conflict with confirmed triage", At: now}
	rec.Decisions = append(rec.Decisions, task.Decision{AskedAt: now, Question: task.Question{
		Issue: scopePlanConflictIssue, Context: context.String(),
		Options:        []task.Option{{Label: "A", Description: "Revise the planning documents and affected code to match confirmed triage."}, {Label: "B", Description: "Correct the triage decision at its source, then restart the check."}},
		Recommendation: "A", ImpactIfWrong: "The wrong scope may be implemented; Foreman rechecks before posting or handoff.",
	}})
}
