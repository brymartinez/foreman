package agent

import (
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func localBrief() TaskBrief {
	return TaskBrief{Key: task.Key{Repo: "a/b", Number: 7}, IssueBody: "Retry 5xx", Branch: "feat/7", BaseBranch: "main", Worktree: "/wt", LocalReview: true}
}

func TestImplementationPromptBeforeReviewAsksForACommitAndAProposedPR(t *testing.T) {
	p := ImplementationPrompt(localBrief())
	for _, want := range []string{`"review_ready"`, "prTitle", "prBody", "Do not push the branch or open a pull request", `"Closes #7"`} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	for _, unwanted := range []string{"git push -u origin", "gh pr create", `"pr_opened" once`, `report "pr_opened"`} {
		if strings.Contains(p, unwanted) {
			t.Errorf("prompt still says %q", unwanted)
		}
	}
	if !strings.Contains(p, `status "checkpoint"`) {
		t.Error("a local review prompt lost checkpoint guidance")
	}
}

func TestImplementationPromptWithoutLocalReviewStillOpensThePR(t *testing.T) {
	b := localBrief()
	b.LocalReview = false
	p := ImplementationPrompt(b)
	if strings.Contains(p, "review_ready") || !strings.Contains(p, "gh pr create") {
		t.Fatal("the PR workflow prompt changed")
	}
}

func TestImplementationPromptMergeBeforeReviewDoesNotPush(t *testing.T) {
	b := localBrief()
	b.Merge = &MergeBrief{Base: "main", Files: []string{"go.mod"}}
	p := ImplementationPrompt(b)
	if strings.Contains(p, "git push") || !strings.Contains(p, "git merge origin/main") || !strings.Contains(p, `"review_ready"`) {
		t.Fatalf("local merge prompt:\n%s", p)
	}
}

func TestLocalFindingsMessageKeepsTheFixLocalAndAsksForTheUpdatedDescription(t *testing.T) {
	msg := LocalFindingsMessage(task.Review{Round: 2, Reviewer: task.Codex, Summary: "one bug", Findings: []task.Finding{{Severity: "blocking", Summary: "4xx retried"}}})
	for _, want := range []string{"1. [blocking] 4xx retried", "Do not push", "review_ready", "prBody"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "PR #") || strings.Contains(msg, "pr_opened") {
		t.Errorf("message names a PR:\n%s", msg)
	}
}

func TestLocalMergeMessageMergesWithoutPushing(t *testing.T) {
	msg := LocalMergeMessage("feat/7", "main", []string{"go.mod"})
	if strings.Contains(msg, "git push") || strings.Contains(msg, "pr_opened") || !strings.Contains(msg, "review_ready") || !strings.Contains(msg, "Never rebase") {
		t.Fatalf("message:\n%s", msg)
	}
}

func TestResumeMessageBeforeReviewEndsWithReviewReady(t *testing.T) {
	decs := []task.Decision{{Question: task.Question{Issue: "Q?"}, Answer: "A"}}
	open := &task.Review{Round: 4, Findings: []task.Finding{{Severity: "blocking", Summary: "4xx retried"}}}
	msg := ResumeMessage(decs, open, false, true)
	if !strings.Contains(msg, "review_ready, blocked, or failed") || strings.Contains(msg, "PR #") || !strings.Contains(msg, "Do not push") {
		t.Fatalf("message:\n%s", msg)
	}
}
