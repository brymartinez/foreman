package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

type fakeBranchProposer struct {
	in  agent.BranchInput
	out agent.Outcome
}

func (f *fakeBranchProposer) Propose(_ context.Context, in agent.BranchInput) (agent.Outcome, error) {
	f.in = in
	return f.out, nil
}

func TestRepositoryBranchGuidanceWinsOverLinearSuggestion(t *testing.T) {
	f := newFixture()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Follow [branch guidance](docs/branches.md)."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs/branches.md"), []byte("Branch naming: <owner>/<workstream>-<short-slug>."), 0o644); err != nil {
		t.Fatal(err)
	}
	f.gh.issue.Identifier = "KUB-189"
	f.gh.issue.BranchName = "bryan/kub-189-project-comms"
	f.gh.issue.Title = "DB project comms thread"
	f.gh.issue.Body = "Workstream: db"
	f.app.Linear = &lifecycleTracker{issue: f.gh.issue}
	proposer := &fakeBranchProposer{out: agent.Outcome{Status: agent.BranchProposed, BranchName: "bryan/db-kub-189-project-comms", Summary: "DB workstream"}}
	f.app.BranchProposer = proposer
	got, err := f.app.ProposeBranch(context.Background(), StartRequest{Dir: root, Key: task.Key{Source: task.Linear, Repo: f.gh.repo.Repo, ExternalID: "issue-id"}, Agent: task.Claude})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "bryan/db-kub-189-project-comms" || got.Uncertain {
		t.Fatalf("proposal = %+v", got)
	}
	if !strings.Contains(proposer.in.Prompt, "<owner>/<workstream>-<short-slug>") || !strings.Contains(proposer.in.Prompt, f.gh.issue.BranchName) {
		t.Fatalf("prompt did not carry both convention and suggestion: %q", proposer.in.Prompt)
	}
	if len(f.wt.reqs) != 0 {
		t.Fatal("proposal created a worktree")
	}
}

func TestUncertainRepositoryBranchStopsBeforeWorktree(t *testing.T) {
	f := newFixture()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Branch naming: <owner>/<workstream>-<short-slug>."), 0o644); err != nil {
		t.Fatal(err)
	}
	f.app.BranchProposer = &fakeBranchProposer{out: agent.Outcome{Status: agent.BranchProposed, BranchName: "bryan/db-825-provider-retries", Uncertain: true, Summary: "workstream unclear"}}
	_, err := f.app.Start(context.Background(), StartRequest{Dir: root, Issue: 825, Agent: task.Claude})
	if err == nil || !strings.Contains(err.Error(), "branch needs a choice") {
		t.Fatalf("start error = %v", err)
	}
	if len(f.wt.reqs) != 0 {
		t.Fatal("uncertain branch created a worktree")
	}
}

func TestConfirmedBranchIsUsedWithoutASecondProposal(t *testing.T) {
	f := newFixture()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Branch naming: <owner>/<workstream>-<short-slug>."), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, err := f.app.Start(context.Background(), StartRequest{Dir: root, Issue: 825, Agent: task.Claude, BranchName: "bryan/db-825-provider-retries"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Delegation.Branch != "bryan/db-825-provider-retries" || f.wt.reqs[0].Branch != rec.Delegation.Branch {
		t.Fatalf("branch was not kept: %+v", rec.Delegation)
	}
}
