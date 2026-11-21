package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/store"
	"github.com/brymartinez/foreman/internal/task"
)

const localID = "L-0000002A"

// fakeLocal is a LocalIssues store in memory.
type fakeLocal struct {
	items     []InboxItem
	err       error
	issue     Issue
	reads     []task.Key
	completed []task.Key
}

func (f *fakeLocal) OpenIssues(context.Context) ([]InboxItem, error) { return f.items, f.err }
func (f *fakeLocal) GetIssue(_ context.Context, key task.Key) (Issue, error) {
	f.reads = append(f.reads, key)
	return f.issue, f.err
}
func (f *fakeLocal) Complete(_ context.Context, key task.Key) error {
	f.completed = append(f.completed, key)
	return nil
}

func localFixture(t *testing.T) (fixture, task.Key, *fakeLocal, store.Triage) {
	t.Helper()
	f := newFixture()
	key := task.Key{Repo: f.gh.repo.Repo, Source: task.Local, ExternalID: localID}
	local := &fakeLocal{issue: Issue{Key: key, Identifier: localID, Title: "Fix cache", Body: "Use bounded retries.", URL: "https://tracker.example/WEB-42", State: "open"}}
	triage := store.Triage{Dir: t.TempDir()}
	f.app.LocalIssues, f.app.Triage = local, triage
	return f, key, local, triage
}

func TestLocalStartNeedsConfirmedTriageBeforeAnyWorktree(t *testing.T) {
	f, key, _, triage := localFixture(t)
	req := StartRequest{Repo: key.Repo, Key: key, Agent: task.Claude}
	if _, err := f.app.Start(context.Background(), req); !errors.Is(err, ErrAwaitingTriage) {
		t.Fatalf("start without triage = %v, want ErrAwaitingTriage", err)
	}
	if len(f.wt.reqs) != 0 || len(f.runners.spawned) != 0 {
		t.Fatal("a start prepared work for an issue awaiting triage")
	}
	// A tag that spells the GitHub label cannot stand in for confirmed triage.
	f.app.LocalIssues.(*fakeLocal).issue.Labels = []string{"triaged"}
	if _, err := f.app.Start(context.Background(), req); !errors.Is(err, ErrAwaitingTriage) {
		t.Fatalf("start with a triaged label = %v, want ErrAwaitingTriage", err)
	}
	if err := triage.Save(key, "Keep the cache API."); err != nil {
		t.Fatal(err)
	}
	rec, err := f.app.Start(context.Background(), req)
	if err != nil || rec.Key != key || rec.Identifier != localID {
		t.Fatalf("start = %+v, %v", rec, err)
	}
	if want := "feat/local-l-0000002a-fix-cache"; rec.Delegation.Branch != want {
		t.Fatalf("branch = %q, want %q", rec.Delegation.Branch, want)
	}
}

func TestLocalIssuePlansAndRunsWithoutTrackerWrites(t *testing.T) {
	f, key, local, triage := localFixture(t)
	if err := triage.Save(key, "Keep the cache API. Implement in https://github.com/testorg/example."); err != nil {
		t.Fatal(err)
	}
	unbound := key
	unbound.Repo = ""
	proposal, err := f.app.ProposeRepository(context.Background(), unbound)
	if err != nil || proposal.Repo != key.Repo {
		t.Fatalf("repository = %+v, %v", proposal, err)
	}
	started(t, f, StartRequest{Repo: key.Repo, Key: key, Agent: task.Claude, BranchName: f.gh.prs[912].HeadRefName})
	f.claude.turns = []agent.Turn{plannedTurn("planner"), prTurn("implementer")}
	got := run(t, f, key)
	if got.State != task.WaitingForCI || !got.PlanPosted() || len(f.gh.comments) != 0 {
		t.Fatalf("state %s (%+v), plan saved %t, GitHub comments %d", got.State, got.LastOutcome, got.PlanPosted(), len(f.gh.comments))
	}
	if !strings.Contains(f.claude.prompts[0], "Keep the cache API.") {
		t.Fatal("planner did not receive the confirmed triage")
	}
	if len(local.completed) != 0 {
		t.Fatalf("an unfinished task completed its issue: %v", local.completed)
	}
}

func TestLocalPullRequestRulesKeepTheSourceLinkOutOfGitHub(t *testing.T) {
	f, key, _, triage := localFixture(t)
	if err := triage.Save(key, "Keep the cache API."); err != nil {
		t.Fatal(err)
	}
	started(t, f, StartRequest{Repo: key.Repo, Key: key, Agent: task.Claude})
	f.claude.turns = []agent.Turn{plannedTurn("planner"), prTurn("implementer")}
	run(t, f, key)
	for _, p := range f.claude.prompts {
		if strings.Contains(p, "Closes #") && !strings.Contains(p, "Do not put") {
			t.Errorf("a local task's prompt tells the agent to close a GitHub issue: %q", p)
		}
		if strings.Contains(p, "pull request body must link") {
			t.Errorf("a local task's prompt tells the agent to link the source in the PR: %q", p)
		}
	}
}

func TestFinishedLocalTaskClosesItsIssue(t *testing.T) {
	f, key, local, triage := localFixture(t)
	if err := triage.Save(key, "Keep the cache API."); err != nil {
		t.Fatal(err)
	}
	started(t, f, StartRequest{Repo: key.Repo, Key: key, Agent: task.Claude})
	if _, err := f.app.update(context.Background(), key, existing(func(cur *task.Record) { cur.State = task.Done })); err != nil {
		t.Fatal(err)
	}
	if len(local.completed) != 1 || local.completed[0] != key {
		t.Fatalf("completed = %v, want [%v]", local.completed, key)
	}
}

func TestClosedLocalIssueFinishesDelegatedTaskInGitHubOnlyView(t *testing.T) {
	f, key, local, triage := localFixture(t)
	if err := triage.Save(key, "Keep the cache API."); err != nil {
		t.Fatal(err)
	}
	started(t, f, StartRequest{Repo: key.Repo, Key: key, Agent: task.Claude})
	f.store.recs[key].State = task.Failed
	f.store.recs[key].Delegation.RunnerPID = 0
	local.issue.State = "closed"
	report, err := f.app.InboxSources(context.Background(), task.GitHub)
	if err != nil {
		t.Fatal(err)
	}
	if f.store.recs[key].State != task.Done {
		t.Fatalf("state %s, want DONE", f.store.recs[key].State)
	}
	if len(report.Current) != 0 || len(Delegable(report)) != 0 {
		t.Fatalf("current %+v", report.Current)
	}
}

func TestLocalTaskResumesAfterRestart(t *testing.T) {
	f, key, _, triage := localFixture(t)
	if err := triage.Save(key, "Keep the cache API."); err != nil {
		t.Fatal(err)
	}
	started(t, f, StartRequest{Repo: key.Repo, Key: key, Agent: task.Claude})
	rec := f.store.recs[key]
	rec.State = task.Reviewed
	rec.Delegation.PR = 912
	rec.Delegation.RunnerPID = 0
	tasks, err := f.app.Tasks(context.Background())
	if err != nil || len(tasks) != 1 || tasks[0].Key != key {
		t.Fatalf("delegated tasks = %+v, %v", tasks, err)
	}
	resumed, err := f.app.Resume(context.Background(), tasks[0].Key)
	if err != nil || resumed.State != task.Implementing || len(f.runners.spawned) != 2 || f.runners.spawned[1] != key {
		t.Fatalf("resume = %+v, %v; spawned %+v", resumed, err, f.runners.spawned)
	}
}

var _ = io.Discard
