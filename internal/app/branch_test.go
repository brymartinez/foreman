package app

import (
	"context"
	"errors"
	"fmt"
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

// managedCheckout points the fixture's managed repository at a temp directory whose
// AGENTS.md holds stale guidance.
func managedCheckout(t *testing.T, f *fixture) string {
	t.Helper()
	f.app.RepositoriesDir = t.TempDir()
	dir := task.RepositoryPath(f.app.RepositoriesDir, f.repos.info.Repo)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("Branch naming: stale/<slug>."), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestNewTaskProposalReadsGuidanceAfterSyncingTheManagedCheckout(t *testing.T) {
	f := newFixture()
	dir := managedCheckout(t, &f)
	f.wt.onSync = func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("Branch naming: current/<slug>."), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	proposer := &fakeBranchProposer{out: agent.Outcome{Status: agent.BranchProposed, BranchName: "current/825-provider-retries"}}
	f.app.BranchProposer = proposer
	got, err := f.app.ProposeBranch(context.Background(), StartRequest{Repo: f.repos.info.Repo, Issue: 825, Agent: task.Claude})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "current/825-provider-retries" {
		t.Fatalf("proposal = %+v", got)
	}
	if len(f.wt.syncs) != 1 || f.wt.syncs[0] != dir+" main" {
		t.Fatalf("syncs = %v", f.wt.syncs)
	}
	if !strings.Contains(proposer.in.Prompt, "current/<slug>") || strings.Contains(proposer.in.Prompt, "stale/<slug>") {
		t.Fatalf("prompt did not carry the synced guidance: %q", proposer.in.Prompt)
	}
}

func TestSyncFailureStopsManualAndAutoStartsBeforeTheProposer(t *testing.T) {
	f := newFixture()
	managedCheckout(t, &f)
	f.wt.syncErr = errors.New("local main has diverged from origin/main")
	f.gh.issue.Labels = []string{"triaged"}
	proposer := &fakeBranchProposer{}
	f.app.BranchProposer = proposer
	ctx := context.Background()
	req := StartRequest{Repo: f.repos.info.Repo, Issue: 825, Agent: task.Claude}
	if _, err := f.app.ProposeBranch(ctx, req); err == nil || !strings.Contains(err.Error(), "refresh ridezoomo/armada-backend default branch: local main has diverged") {
		t.Fatalf("propose error = %v", err)
	}
	auto := req
	auto.NewOnly = true
	for _, r := range []StartRequest{req, auto} {
		if _, err := f.app.Start(ctx, r); err == nil || !strings.Contains(err.Error(), "diverged") {
			t.Fatalf("start (new only %v) error = %v", r.NewOnly, err)
		}
	}
	if proposer.in.Prompt != "" {
		t.Fatal("proposer ran on stale guidance")
	}
	if len(f.wt.reqs) != 0 {
		t.Fatal("a failed refresh created a worktree")
	}
}

func TestStoredBranchSkipsTheCheckoutSync(t *testing.T) {
	f := newFixture()
	managedCheckout(t, &f)
	key := task.Key{Repo: f.repos.info.Repo, Number: 825}
	f.store.recs[key] = &task.Record{Key: key, State: task.Failed, Delegation: &task.Delegation{Branch: "feat/825-provider-retries"}}
	f.wt.syncErr = errors.New("offline")
	got, err := f.app.ProposeBranch(context.Background(), StartRequest{Repo: key.Repo, Issue: 825, Agent: task.Claude})
	if err != nil || got.Name != "feat/825-provider-retries" {
		t.Fatalf("proposal = %+v, %v", got, err)
	}
	if len(f.wt.syncs) != 0 {
		t.Fatalf("syncs = %v", f.wt.syncs)
	}
}

func TestCallerSuppliedCheckoutIsNotSynced(t *testing.T) {
	f := newFixture()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("No branch rule."), 0o644); err != nil {
		t.Fatal(err)
	}
	f.app.BranchProposer = &fakeBranchProposer{out: agent.Outcome{Status: agent.BranchProposed, BranchName: "feat/825-provider-retries"}}
	if _, err := f.app.ProposeBranch(context.Background(), StartRequest{Dir: root, Issue: 825, Agent: task.Claude}); err != nil {
		t.Fatal(err)
	}
	if len(f.wt.syncs) != 0 {
		t.Fatalf("syncs = %v", f.wt.syncs)
	}
}

// writeRepo writes files under a new temp root and returns the root.
func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, text := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestBranchGuideSkipsGeneralAndHistoricalDocuments(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"AGENTS.md": "Read `docs/architecture.md` for the design.\n" +
			"Past choices, including branch naming, live in the [decision log](docs/decisions.md).\n",
		"docs/architecture.md": "Each task gets its own branch.",
		"docs/decisions.md":    "2024: branch names were old/<slug>." + strings.Repeat("x", 40<<10),
	})
	guide, err := readBranchGuide(root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(guide, "own branch") || strings.Contains(guide, "old/<slug>") {
		t.Fatalf("guide read a general document: %q", guide)
	}
	if !strings.Contains(guide, "## AGENTS.md") {
		t.Fatalf("guide = %q, want AGENTS.md", guide)
	}
}

func TestBranchGuideFollowsExplicitBranchNamingDocuments(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"AGENTS.md":           "Branch naming convention: see [branches](docs/branches.md).\n",
		"docs/branches.md":    "Use <owner>/<slug>. Workstream branch rules: `workstreams.md`.\n",
		"docs/workstreams.md": "Workstreams: db, web.",
	})
	guide, err := readBranchGuide(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(guide, "<owner>/<slug>") || !strings.Contains(guide, "db, web") {
		t.Fatalf("guide = %q", guide)
	}
}

func TestBranchGuideReportsAMissingExplicitDocument(t *testing.T) {
	root := writeRepo(t, map[string]string{"AGENTS.md": "Branch naming rules: `docs/branches.md`.\n"})
	_, err := readBranchGuide(root)
	if err == nil || !strings.Contains(err.Error(), "docs/branches.md") || !strings.Contains(err.Error(), "AGENTS.md") {
		t.Fatalf("err = %v", err)
	}
}

func TestBranchGuideReportsAnOversizeExplicitDocument(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"AGENTS.md":        "Branch naming rules: `docs/branches.md`.\n",
		"docs/branches.md": strings.Repeat("x", 33<<10),
	})
	_, err := readBranchGuide(root)
	if err == nil || !strings.Contains(err.Error(), "too large") || !strings.Contains(err.Error(), "docs/branches.md") {
		t.Fatalf("err = %v", err)
	}
}

func TestConfirmedBranchStillStopsWhenTheManagedCheckoutCannotSync(t *testing.T) {
	f := newFixture()
	managedCheckout(t, &f)
	f.wt.syncErr = errors.New("managed checkout has uncommitted changes on main")
	_, err := f.app.Start(context.Background(), StartRequest{Repo: f.repos.info.Repo, Issue: 825, Agent: task.Claude, BranchName: "feat/825-provider-retries"})
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("start error = %v", err)
	}
	if len(f.wt.reqs) != 0 {
		t.Fatal("a failed refresh created a worktree")
	}
}

func TestBranchGuideReportsAMissingNinthGuide(t *testing.T) {
	files := map[string]string{}
	var agents strings.Builder
	for i := 1; i <= 8; i++ {
		name := fmt.Sprintf("docs/branches%d.md", i)
		files[name] = "rule"
		fmt.Fprintf(&agents, "Branch naming rules: `%s`.\n", name)
	}
	agents.WriteString("Branch naming rules: `docs/missing.md`.\n")
	files["AGENTS.md"] = agents.String()
	_, err := readBranchGuide(writeRepo(t, files))
	if err == nil || !strings.Contains(err.Error(), "docs/missing.md") {
		t.Fatalf("err = %v", err)
	}
}

func TestBranchGuideReadsEveryNamedGuide(t *testing.T) {
	files := map[string]string{}
	var agents strings.Builder
	for i := 1; i <= 9; i++ {
		name := fmt.Sprintf("docs/branches%d.md", i)
		files[name] = fmt.Sprintf("rule %d", i)
		fmt.Fprintf(&agents, "Branch naming rules: `%s`.\n", name)
	}
	files["AGENTS.md"] = agents.String()
	guide, err := readBranchGuide(writeRepo(t, files))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(guide, "rule 9") {
		t.Fatalf("guide = %q, want the ninth guide", guide)
	}
}

func TestBranchGuideSkipsOtherLinksOnABranchRuleLine(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"AGENTS.md":            "Branch naming rules: [current](docs/branches.md); background: [architecture](docs/architecture.md)\n",
		"docs/branches.md":     "Use <owner>/<slug>.",
		"docs/architecture.md": "Each task gets its own branch." + strings.Repeat("x", 40<<10),
	})
	guide, err := readBranchGuide(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(guide, "<owner>/<slug>") || strings.Contains(guide, "own branch") {
		t.Fatalf("guide = %q", guide)
	}
}

func TestBranchGuideSkipsAContextLinkInTheSameClause(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"AGENTS.md":            "Branch naming rules: [current](docs/branches.md) and see [architecture](docs/architecture.md) for context.\n",
		"docs/branches.md":     "Use <owner>/<slug>.",
		"docs/architecture.md": "Each task gets its own branch." + strings.Repeat("x", 40<<10),
	})
	guide, err := readBranchGuide(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(guide, "<owner>/<slug>") || strings.Contains(guide, "own branch") {
		t.Fatalf("guide = %q", guide)
	}
}

func TestBranchGuideFollowsAListOfNamedGuides(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"AGENTS.md":      "Branch naming rules: `docs/owners.md`, `docs/slugs.md` and [workstreams](docs/work.md).\n",
		"docs/owners.md": "owner rule",
		"docs/slugs.md":  "slug rule",
		"docs/work.md":   "workstream rule",
	})
	guide, err := readBranchGuide(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"owner rule", "slug rule", "workstream rule"} {
		if !strings.Contains(guide, want) {
			t.Fatalf("guide = %q, want %q", guide, want)
		}
	}
}

func TestBranchGuideSkipsAHistoricalLinkInAList(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"AGENTS.md":         "Branch naming rules: [current](docs/branches.md) and [decision log](docs/decisions.md)\n",
		"docs/branches.md":  "Use <owner>/<slug>.",
		"docs/decisions.md": "2024: branch names were old/<slug>." + strings.Repeat("x", 40<<10),
	})
	guide, err := readBranchGuide(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(guide, "<owner>/<slug>") || strings.Contains(guide, "old/<slug>") {
		t.Fatalf("guide = %q", guide)
	}
}

func TestBranchGuideReadsACurrentGuideListedAfterAHistoricalOne(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"AGENTS.md": "Branch naming rules: [decision log](old.md) and [current](current.md)\n",
		"old.md":    "2024: branch names were old/<slug>.",
	})
	_, err := readBranchGuide(root)
	if err == nil || !strings.Contains(err.Error(), "current.md") {
		t.Fatalf("err = %v, want the missing current guide reported", err)
	}
}

func TestBranchGuideSkipsAListUnderAHistoricalHeading(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"AGENTS.md": "Branch naming history: [2023](a.md) and [2024](b.md)\n",
	})
	if _, err := readBranchGuide(root); err != nil {
		t.Fatalf("err = %v, want historical links skipped", err)
	}
}
