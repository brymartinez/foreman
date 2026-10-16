package app

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

type lifecycleTracker struct {
	issue        Issue
	comments     []TrackerComment
	reads        []task.Key
	commentReads []task.Key
	posted       []TrackerComment
	postIDs      []string
	missing      map[string]bool
	commentsErr  error
}

func (f *lifecycleTracker) AssignedIssues(context.Context, map[task.Repo]string) ([]InboxItem, error) {
	return nil, nil
}
func (f *lifecycleTracker) GetIssue(_ context.Context, key task.Key) (Issue, error) {
	f.reads = append(f.reads, key)
	return f.issue, nil
}
func (f *lifecycleTracker) IssueComments(_ context.Context, key task.Key) ([]TrackerComment, error) {
	f.commentReads = append(f.commentReads, key)
	if f.commentsErr != nil {
		return nil, f.commentsErr
	}
	out := slices.Clone(f.comments)
	for _, c := range f.posted {
		if i := slices.IndexFunc(out, func(o TrackerComment) bool { return o.ID == c.ID }); i >= 0 {
			out[i] = c
		} else {
			out = append(out, c)
		}
	}
	return out, nil
}
func (f *lifecycleTracker) GetIssueComment(ctx context.Context, id string) (TrackerComment, error) {
	comments, _ := f.IssueComments(ctx, task.Key{})
	if i := slices.IndexFunc(comments, func(c TrackerComment) bool { return c.ID == id }); i >= 0 && !f.missing[id] {
		return comments[i], nil
	}
	return TrackerComment{}, ErrGone
}
func (f *lifecycleTracker) PostIssueComment(_ context.Context, _ task.Key, id, body string) (TrackerComment, error) {
	f.postIDs = append(f.postIDs, id)
	if f.missing[id] {
		return TrackerComment{}, ErrGone
	}
	if id == "" {
		id = fmt.Sprintf("linear-comment-%d", len(f.posted)+1)
	}
	c := TrackerComment{ID: id, Body: body, URL: "https://linear.app/comment/" + id}
	f.posted = append(f.posted, c)
	return c, nil
}

func TestLinearAutoStartRechecksExactTriagedLabelBeforeWorktree(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: f.gh.repo.Repo, Source: task.Linear, ExternalID: "uuid-42"}
	tracker := &lifecycleTracker{issue: Issue{Key: key, Identifier: "WEB-42", Title: "Fix cache", URL: "https://linear.app/acme/issue/WEB-42", State: "open", Labels: []string{"needs-triaged"}}}
	f.app.Linear = tracker
	req := StartRequest{Repo: key.Repo, Key: key, Agent: task.Claude, NewOnly: true}
	if _, err := f.app.Start(context.Background(), req); err == nil || !strings.Contains(err.Error(), "triaged") {
		t.Fatalf("auto start = %v", err)
	}
	if len(f.wt.reqs) != 0 || len(f.runners.spawned) != 0 {
		t.Fatal("auto start prepared work for an untriaged issue")
	}
	tracker.issue.Labels = []string{"triaged"}
	rec, err := f.app.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Key != key || rec.Identifier != "WEB-42" || len(tracker.reads) != 2 || tracker.reads[0] != key || len(f.runners.spawned) != 1 || f.runners.spawned[0] != key {
		t.Fatalf("start = %+v, reads = %+v, spawned = %+v", rec, tracker.reads, f.runners.spawned)
	}
}

func TestLinearManualStartDoesNotRequireTriagedLabel(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: f.gh.repo.Repo, Source: task.Linear, ExternalID: "uuid-42"}
	f.app.Linear = &lifecycleTracker{issue: Issue{Key: key, Identifier: "WEB-42", Title: "Fix cache", URL: "https://linear.app/acme/issue/WEB-42", State: "open"}}
	rec, err := f.app.Start(context.Background(), StartRequest{Repo: key.Repo, Key: key, Agent: task.Claude})
	if err != nil || rec == nil || rec.Key != key || rec.Delegation.Branch != "feat/linear-web-42-fix-cache" {
		t.Fatalf("start = %+v, %v", rec, err)
	}
	if rec.Delegation.Worktree != "/wt/ridezoomo-armada-backend/feat/linear-web-42-fix-cache" {
		t.Fatalf("worktree = %q", rec.Delegation.Worktree)
	}
}

func TestLinearSuggestionUsesFullBranchWorktreePath(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: f.gh.repo.Repo, Source: task.Linear, ExternalID: "uuid-42"}
	f.app.Linear = &lifecycleTracker{issue: Issue{Key: key, Identifier: "WEB-42", Title: "Fix cache", State: "open", BranchName: "bryan/web-42-fix-cache"}}
	rec, err := f.app.Start(context.Background(), StartRequest{Repo: key.Repo, Key: key, Agent: task.Claude})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Delegation.Branch != "bryan/web-42-fix-cache" || rec.Delegation.Worktree != "/wt/ridezoomo-armada-backend/bryan/web-42-fix-cache" {
		t.Fatalf("delegation = %+v", rec.Delegation)
	}
}

func TestLinearRouteReadsIssueAndCommentsFromLinear(t *testing.T) {
	f, selector := routed(t, agent.Selection{}, known)
	key := task.Key{Repo: f.gh.repo.Repo, Source: task.Linear, ExternalID: "uuid-42"}
	tracker := &lifecycleTracker{issue: Issue{Key: key, Identifier: "WEB-42", Title: "Fix cache", Body: "Use bounded retries.", State: "open"}, comments: []TrackerComment{{Body: "Keep the old TTL."}}}
	f.app.Linear = tracker
	if _, err := f.app.Route(context.Background(), RouteRequest{Repo: key.Repo, Key: key}); err != nil {
		t.Fatal(err)
	}
	if len(tracker.reads) != 1 || tracker.reads[0] != key || len(tracker.commentReads) != 1 || tracker.commentReads[0] != key {
		t.Fatalf("reads = %+v, comment reads = %+v", tracker.reads, tracker.commentReads)
	}
	if len(selector.calls) != 1 || !strings.Contains(selector.calls[0].Prompt, "WEB-42") || !strings.Contains(selector.calls[0].Prompt, "Keep the old TTL.") {
		t.Fatalf("route prompt = %+v", selector.calls)
	}
}

func TestLinearRunnerReadsStoredSourceAndPostsFourPlanningComments(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: f.gh.repo.Repo, Source: task.Linear, ExternalID: "uuid-42"}
	tracker := &lifecycleTracker{issue: Issue{Key: key, Identifier: "WEB-42", Title: "Fix cache", Body: "Use bounded retries.", URL: "https://linear.app/acme/issue/WEB-42", State: "open"}}
	f.app.Linear = tracker
	started(t, f, StartRequest{Repo: key.Repo, Key: key, Agent: task.Claude})
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	rec := f.store.recs[key]
	if !rec.PlanPosted() || len(tracker.posted) != 4 || len(f.gh.comments) != 0 {
		t.Fatalf("plan = %+v, Linear posts = %d, GitHub posts = %d", rec.Plan, len(tracker.posted), len(f.gh.comments))
	}
	for i, doc := range task.PlanDocs {
		if rec.Plan[i].Doc != doc || rec.Plan[i].ExternalCommentID != tracker.posted[i].ID || !strings.Contains(tracker.posted[i].Body, "## "+doc.Title()) {
			t.Errorf("comment %d = %+v, post %+v", i, rec.Plan[i], tracker.posted[i])
		}
	}
	if len(tracker.reads) < 2 || !strings.Contains(f.claude.prompts[0], "WEB-42") || !strings.Contains(f.claude.prompts[1], tracker.posted[3].URL) {
		t.Errorf("reads = %+v, prompts = %+v", tracker.reads, f.claude.prompts)
	}
}

func TestLinearReplanningRepostsDeletedCommentAndEditsOtherComments(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: f.gh.repo.Repo, Source: task.Linear, ExternalID: "uuid-42"}
	tracker := &lifecycleTracker{issue: Issue{Key: key, Identifier: "WEB-42", Title: "Fix cache", State: "open"}, missing: map[string]bool{"old-prd": true}}
	f.app.Linear = tracker
	started(t, f, StartRequest{Repo: key.Repo, Key: key, Agent: task.Claude})
	tracker.comments = []TrackerComment{
		{ID: "old-analysis", URL: "https://linear.app/comment/old-analysis", Body: planCommentBody(task.Analysis, "", "Earlier analysis.")},
		{ID: "old-prd", URL: "https://linear.app/comment/old-prd", Body: planCommentBody(task.PRD, "", "Earlier PRD.")},
	}
	f.store.recs[key].SetPlanComment(task.PlanComment{Doc: task.Analysis, ExternalCommentID: "old-analysis", URL: "https://linear.app/comment/old-analysis"})
	f.store.recs[key].SetPlanComment(task.PlanComment{Doc: task.PRD, ExternalCommentID: "old-prd", URL: "https://linear.app/comment/old-prd"})
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	// The deleted PRD is found missing before the edit, so it is posted anew.
	if !f.store.recs[key].PlanPosted() || !slices.Equal(tracker.postIDs, []string{"old-analysis", "", "", ""}) {
		t.Fatalf("plan = %+v, post IDs = %v", f.store.recs[key].Plan, tracker.postIDs)
	}
}

func TestClosedLinearIssueFinishesDelegatedTaskInGitHubOnlyView(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: f.gh.repo.Repo, Source: task.Linear, ExternalID: "uuid-42"}
	tracker := &lifecycleTracker{issue: Issue{Key: key, Identifier: "WEB-42", Title: "Fix cache", State: "open"}}
	f.app.Linear = tracker
	f.app.LinearProjects = map[task.Repo]string{key.Repo: "https://linear.app/acme/project/web-123"}
	started(t, f, StartRequest{Repo: key.Repo, Key: key, Agent: task.Claude})
	f.store.recs[key].State = task.Failed
	f.store.recs[key].Delegation.RunnerPID = 0
	tracker.issue.State = "closed"
	report, err := f.app.InboxSources(context.Background(), task.GitHub)
	if err != nil {
		t.Fatal(err)
	}
	if f.store.recs[key].State != task.Done || len(tracker.reads) != 2 || tracker.reads[1] != key {
		t.Fatalf("refresh: state %s, reads %+v", f.store.recs[key].State, tracker.reads)
	}
	if len(report.Current) != 0 || len(Delegable(report)) != 0 {
		t.Fatalf("refresh: current %+v", report.Current)
	}
}

func TestLinearTaskRefreshesAndResumesAfterGitHubOnlyLaunch(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: f.gh.repo.Repo, Source: task.Linear, ExternalID: "uuid-42"}
	tracker := &lifecycleTracker{issue: Issue{Key: key, Identifier: "WEB-42", Title: "Fix cache", State: "open"}}
	f.app.Linear = tracker
	f.app.LinearProjects = map[task.Repo]string{key.Repo: "https://linear.app/acme/project/web-123"}
	started(t, f, StartRequest{Repo: key.Repo, Key: key, Agent: task.Claude})
	rec := f.store.recs[key]
	rec.State = task.Reviewed
	rec.Delegation.PR = 912
	rec.Delegation.RunnerPID = 0
	report, err := f.app.InboxSources(context.Background(), task.GitHub)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Current) != 0 || len(tracker.reads) != 2 || tracker.reads[1] != key {
		t.Fatalf("GitHub-only refresh: current %+v, Linear reads %+v", report.Current, tracker.reads)
	}
	tasks, err := f.app.Tasks(context.Background())
	if err != nil || len(tasks) != 1 || tasks[0].Key != key {
		t.Fatalf("delegated tasks = %+v, %v", tasks, err)
	}
	resumed, err := f.app.Resume(context.Background(), tasks[0].Key)
	if err != nil || resumed.State != task.Working || len(f.runners.spawned) != 2 || f.runners.spawned[1] != key {
		t.Fatalf("resume = %+v, %v; spawned %+v", resumed, err, f.runners.spawned)
	}
}
