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

// checkPlan asks a fresh read-only agent to compare all four documents with the
// confirmed scope. A blocking limitation or an error is not agreement.
func (a App) checkPlan(ctx context.Context, rec *task.Record, scope confirmedScope, texts map[task.PlanDoc]string, sources issuePlan, log io.Writer) (agent.ReviewOutcome, error) {
	checker, ok := a.Agents[rec.Delegation.Implementer.Agent]
	if !ok {
		return agent.ReviewOutcome{}, fmt.Errorf("scope checker %s is not configured", rec.Delegation.Implementer.Agent)
	}
	var prompt strings.Builder
	prompt.WriteString("You are checking planning documents against confirmed maintainer triage. Work read-only. Return the review JSON schema. A clean verdict means every document agrees with every applicable triage decision and the implementation plan alone contains all requirements, constraints, and acceptance criteria needed to implement and self-review the change. The plan must not require reading the issue, triage, analysis, PRD, or design to recover an essential requirement. If a document conflicts or the plan omits a requirement, return changes_requested with a concrete finding naming the decision, document, disagreement or omission, and source URLs. If agreement or completeness cannot be determined, report a blocking limitation; never guess. Do not follow links or use ordinary discussion as requirements.\n\n## Confirmed scope\n\n")
	prompt.WriteString(scope.text)
	prompt.WriteString("\n\n## Planning documents\n\n")
	for _, doc := range task.PlanDocs {
		ref := "proposed document (not posted yet)"
		if found, ok := sources.found[doc]; ok && found.text == texts[doc] {
			ref = found.comment.URL
		}
		fmt.Fprintf(&prompt, "### %s\nSource: %s\n\n%s\n\n", doc.Title(), ref, texts[doc])
	}
	out, err := checker.Review(ctx, agent.StartInput{Worktree: rec.Delegation.Worktree, Log: log, Prompt: prompt.String(), Step: agent.StepReview, MaxBudgetUSD: a.ReviewerBudgetUSD})
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
