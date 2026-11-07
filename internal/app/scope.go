package app

import (
	"context"
	"fmt"
	"slices"
	"strings"

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
