package app

import (
	"context"
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
	return f.comments, nil
}
func (f *lifecycleTracker) GetIssueComment(context.Context, string) (TrackerComment, error) {
	return TrackerComment{}, nil
}
func (f *lifecycleTracker) PostIssueComment(context.Context, task.Key, string, string) (TrackerComment, error) {
	return TrackerComment{}, nil
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
	if err != nil || rec == nil || rec.Key != key {
		t.Fatalf("start = %+v, %v", rec, err)
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
