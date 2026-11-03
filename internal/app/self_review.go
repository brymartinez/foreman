package app

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// selfReview completes every assigned area before returning findings to the implementer.
func (a App) selfReview(ctx context.Context, rec *task.Record, log io.Writer) (agent.ReviewOutcome, error) {
	d := rec.Delegation
	if _, _, err := a.reviewRequirements(ctx, rec); err != nil {
		return agent.ReviewOutcome{}, err
	}
	adapter, ok := a.Agents[d.Implementer.Agent]
	if !ok {
		return agent.ReviewOutcome{}, fmt.Errorf("agent %s is not configured", d.Implementer.Agent)
	}
	instructions, err := a.Stages.Resolve(d.Worktree, task.StageReview)
	if err != nil {
		return agent.ReviewOutcome{}, err
	}
	head, _ := rec.CurrentChange()
	brief := agent.ReviewBrief{Key: rec.Key, Branch: d.Branch, BaseBranch: strings.TrimPrefix(d.BaseRef, "origin/"), Head: head, StageInstructions: instructions.Content(), Passes: a.reviewRounds(), Self: true}
	brief = a.prepareReview(ctx, rec, task.SelfReview, d.Implementer.Agent, brief, log)
	base := brief.BaseHead
	if base == "" {
		base = d.BaseRef
	}
	files, err := a.Worktrees.ReviewChanges(ctx, d.Worktree, base, head, brief.PreviousHead)
	if err != nil {
		return agent.ReviewOutcome{}, fmt.Errorf("read self-review changes: %w", err)
	}
	seen := map[string]bool{}
	for _, f := range files {
		seen[f.Path] = true
	}
	for _, f := range brief.PreviousFindings {
		p := reviewFindingPath(d.Worktree, f.File)
		if p != "" && !seen[p] {
			files = append(files, ReviewFile{Path: p})
			seen[p] = true
		}
	}
	limit := maxSelfReviewAreas
	if a.ReviewerBudgetUSD > 0 {
		limit = min(limit, max(1, int(a.ReviewerBudgetUSD*100)))
	}
	areas := planReviewAreas(files, limit)
	if len(areas) == 0 {
		if len(brief.PreviousFindings) == 0 {
			return reviewContext(agent.ReviewOutcome{Clean: true, Summary: "No changed paths to review", Areas: []task.ReviewArea{{Name: "empty diff"}}}, brief), nil
		}
		areas = []task.ReviewArea{{Name: "previous code findings"}}
	}
	if err := a.startAttempt(ctx, rec); err != nil {
		return agent.ReviewOutcome{}, err
	}
	budget := a.ReviewerBudgetUSD
	if len(areas) > 1 && budget > 0 {
		budget = float64(int(budget*100)/len(areas)) / 100
	}
	if brief.PreviousHead == "" {
		fmt.Fprintf(log, "foreman: self-review scope: full branch, %d assigned areas\n", len(areas))
	} else {
		fmt.Fprintf(log, "foreman: self-review scope: fixes since %s, %d assigned areas\n", short(brief.PreviousHead), len(areas))
	}
	combined := agent.ReviewOutcome{Clean: true}
	var summaries []string
	for i := range areas {
		part := brief
		part.Area = &areas[i]
		part.PreviousFindings = areaFindings(brief.PreviousFindings, areas[i], d.Worktree, i == 0)
		fmt.Fprintf(log, "foreman: %s self-reviewing %s (round %d), area %s, %d files\n", adapter.Name(), reviewTarget(rec), len(rec.Reviews)+1, areas[i].Name, len(areas[i].Paths))
		out, err := adapter.Review(ctx, agent.StartInput{Worktree: d.Worktree, Log: log, MaxBudgetUSD: budget, Prompt: agent.ReviewPrompt(part), Step: agent.StepReview, Purpose: "self_review"})
		addUsage(&combined.Usage, out.Usage)
		if err != nil {
			return combined, fmt.Errorf("self-review area %s: %w", areas[i].Name, err)
		}
		combined.Clean = combined.Clean && out.Clean
		combined.Findings = append(combined.Findings, out.Findings...)
		combined.Limitations = append(combined.Limitations, out.Limitations...)
		combined.Advice = append(combined.Advice, out.Advice...)
		summaries = append(summaries, areas[i].Name+": "+out.Summary)
	}
	combined.Findings = uniqueReviewFindings(combined.Findings)
	combined.Summary = strings.Join(summaries, "\n")
	combined.Areas = areas
	fmt.Fprintf(log, "foreman: self-review completed %d areas: %d findings, %d limitations\n", len(areas), len(combined.Findings), len(combined.Limitations))
	return reviewContext(combined, brief), nil
}

func reviewFindingPath(root, file string) string {
	if file == "" {
		return ""
	}
	if filepath.IsAbs(file) {
		rel, err := filepath.Rel(root, file)
		if err != nil {
			return ""
		}
		file = rel
	}
	file = filepath.Clean(file)
	if file == "." || file == ".." || strings.HasPrefix(file, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(file)
}

func areaFindings(findings []task.Finding, area task.ReviewArea, root string, first bool) []task.Finding {
	paths := map[string]bool{}
	for _, p := range area.Paths {
		paths[p] = true
	}
	var out []task.Finding
	for _, f := range findings {
		p := reviewFindingPath(root, f.File)
		if paths[p] || (p == "" && first) {
			out = append(out, f)
		}
	}
	return out
}

func uniqueReviewFindings(findings []task.Finding) []task.Finding {
	seen := map[task.Finding]int{}
	var out []task.Finding
	for _, f := range findings {
		key := f
		key.Severity = ""
		if i, ok := seen[key]; ok {
			if f.Severity == "blocking" {
				out[i].Severity = f.Severity
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, f)
	}
	return out
}
