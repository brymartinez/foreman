package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// TestPromptSizes prints the size of each agent prompt, built with an empty issue
// body and the shipped default stage files, as the baseline for issue #90. A row
// marked fixed overhead leaves out text a real call always carries. It asserts
// nothing about sizes, so prompt edits do not break it.
//
//	FOREMAN_PROMPT_SIZES=1 go test ./internal/app -run PromptSizes -v
func TestPromptSizes(t *testing.T) {
	if os.Getenv("FOREMAN_PROMPT_SIZES") != "1" {
		t.Skip("set FOREMAN_PROMPT_SIZES=1 to print prompt sizes")
	}
	root := filepath.Join("..", "..")
	stage := func(name string) string {
		b, err := os.ReadFile(filepath.Join(root, "defaults", "stages", name+".md"))
		if err != nil {
			t.Fatalf("read default stage file: %v", err)
		}
		return string(b)
	}
	implement, plan, review, pr := stage("implement"), stage("plan"), stage("review"), stage("pr")

	key := task.Key{Repo: "owner/repo", Number: 1}
	brief := agent.TaskBrief{
		Key: key, Title: "Example issue", URL: "https://github.com/owner/repo/issues/1",
		Branch: "feat/1-example-issue", BaseBranch: "main", Worktree: "/tmp/worktree",
		StageInstructions: implement, PRInstructions: pr,
	}
	planBrief := brief
	planBrief.Planning, planBrief.PlanInstructions = true, plan
	localBrief := brief
	localBrief.LocalReview = true

	guide, err := readBranchGuide(root)
	if err != nil {
		t.Fatalf("read this repository's branch guide: %v", err)
	}
	issue := Issue{Key: key, Identifier: "#1", Title: "Example issue"}

	// The routing prompt comes from the shipped rules and an issue read through the
	// GitHub port, so it is built through App with the test fakes.
	f := newFixture()
	f.app.Routing = starterRouting(t)
	f.gh.issue = Issue{Title: issue.Title}
	route, err := f.app.routePrompt(context.Background(), RouteRequest{Repo: key.Repo, Issue: key.Number})
	if err != nil {
		t.Fatalf("build the routing prompt: %v", err)
	}

	// App.review refuses an empty PR description, so a real review always carries
	// one. This sample is the size of a short Problem, Fix, Risk description.
	const sampleDescription = `## Problem

Retries hide provider outages. A request that fails with a 5xx is retried forever,
so the caller never sees the error and the queue backs up.

## Fix

Retry a failed request at most three times with backoff, and only on a 5xx. Return
the last error to the caller after the third failure.

## Risk

Low. Callers already handle the error path; only the retry count changes.

Closes #1
`
	reviewBrief := agent.ReviewBrief{Key: key, PR: 2, Branch: brief.Branch, BaseBranch: "main", StageInstructions: review}
	sampleReview := reviewBrief
	sampleReview.PRBody = sampleDescription

	rows := []struct{ name, prompt string }{
		{"plan turn", agent.ImplementationPrompt(planBrief)},
		{"implement turn", agent.ImplementationPrompt(brief)},
		{"implement turn, local review", agent.ImplementationPrompt(localBrief)},
		{"routing, shipped rules", route},
		{"review, fixed overhead only", agent.ReviewPrompt(reviewBrief)},
		{"review, sample description", agent.ReviewPrompt(sampleReview)},
		{"post-review PR turn", agent.OpenPRPrompt(agent.OpenPRBrief{
			Branch: brief.Branch, BaseBranch: "main", Head: "0123456789abcdef0123456789abcdef01234567",
			Title: "feat: example", BodyFile: "/tmp/body.md",
			Validation: []task.Check{{Command: "go test ./...", Passed: true}},
		})},
		{"merge repair resume", agent.MergeMessage(brief.Branch, "main", []string{"a.go"})},
		{"evaluation, empty evidence", agent.EvaluationPrompt(agent.EvaluationBrief{Repo: key.Repo, Title: "Example issue", Outcome: "merged"})},
		{"knowledge merge, no items", agent.MergePrompt(string(key.Repo), nil)},
		{"branch, no guide", branchPrompt(issue, brief.Branch, "")},
		{"branch, this repository's guide", branchPrompt(issue, brief.Branch, guide)},
		{"stage file: implement", implement},
		{"stage file: plan", plan},
		{"stage file: review", review},
		{"stage file: pr", pr},
	}
	for _, r := range rows {
		t.Logf("%-34s %6d chars  %5d tokens (chars/4)", r.name, len(r.prompt), len(r.prompt)/4)
	}
}
