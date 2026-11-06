package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func TestProposeBranchUsesIssueMetadata(t *testing.T) {
	for _, tc := range []struct {
		name       string
		linear     bool
		suggestion string
		want       string
		uncertain  bool
	}{
		{name: "GitHub", want: "feat/825-provider-retries"},
		{name: "Linear without suggestion", linear: true, want: "fix/linear-web-42-fix-cache"},
		{name: "Linear suggestion", linear: true, suggestion: "bryan/web-42-fix-cache", want: "bryan/web-42-fix-cache"},
		{name: "invalid Linear suggestion", linear: true, suggestion: "../outside", want: "../outside", uncertain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("Branch naming guidance: see `missing.md`."), 0o644); err != nil {
				t.Fatal(err)
			}
			req := StartRequest{Dir: dir, Issue: 825, Agent: task.Claude}
			if tc.linear {
				req.Repo = f.gh.repo.Repo
				req.Key = task.Key{Repo: req.Repo, Source: task.Linear, ExternalID: "uuid-42"}
				f.app.Linear = &lifecycleTracker{issue: Issue{Key: req.Key, Identifier: "WEB-42", Title: "Fix cache", Labels: []string{"bug"}, BranchName: tc.suggestion}}
			}
			got, err := f.app.ProposeBranch(context.Background(), req)
			if err != nil || got.Name != tc.want || got.Uncertain != tc.uncertain {
				t.Fatalf("proposal = %+v, %v; want %q (uncertain: %t)", got, err, tc.want, tc.uncertain)
			}
			if tc.uncertain && !strings.Contains(got.Reason, "invalid branch name") {
				t.Fatalf("proposal reason = %q", got.Reason)
			}
		})
	}
}

func TestProposeBranchKeepsStoredBranch(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: f.gh.repo.Repo, Number: 825}
	f.store.recs[key] = &task.Record{Key: key, State: task.Failed, Delegation: &task.Delegation{Branch: "fix/stored-branch"}}
	got, err := f.app.ProposeBranch(context.Background(), StartRequest{Dir: t.TempDir(), Issue: key.Number})
	if err != nil || got.Name != "fix/stored-branch" || got.Uncertain {
		t.Fatalf("proposal = %+v, %v", got, err)
	}
}
