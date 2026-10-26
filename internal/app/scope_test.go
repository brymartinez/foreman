package app

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

func TestConfirmedScopeUsesOrderedTriageCommentsForBothSources(t *testing.T) {
	for _, source := range []task.Source{task.GitHub, task.Linear} {
		t.Run(string(source), func(t *testing.T) {
			f := newFixture()
			key := task.Key{Repo: f.gh.repo.Repo, Number: 825, Source: source, ExternalID: "uuid-42"}
			comments := []TrackerComment{
				{ID: "1", URL: "https://example.test/triage/1", Body: "## Triage decisions\nUse the activity table."},
				{ID: "2", URL: "https://example.test/discussion", Body: "Use the recipients table instead."},
				{ID: "3", URL: "https://example.test/triage/3", Body: "## Triage correction\nInclude the date filter."},
			}
			if source == task.Linear {
				f.app.Linear = &lifecycleTracker{issue: Issue{Key: key, Identifier: "WEB-42", Body: "Old description", URL: "https://linear.app/issue/WEB-42", Labels: []string{"triaged"}}, comments: comments}
			} else {
				for _, c := range comments {
					f.gh.issueComments = append(f.gh.issueComments, IssueComment{URL: c.URL, Body: c.Body})
				}
			}
			scope, err := f.app.readScope(context.Background(), key)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(scope.text, "Use the activity table") || !strings.Contains(scope.text, "Include the date filter") || strings.Contains(scope.text, "recipients table") || strings.Index(scope.text, "/triage/1") > strings.Index(scope.text, "/triage/3") {
				t.Fatalf("scope = %q", scope.text)
			}
		})
	}
}

func TestConflictingNewPlanIsNotPosted(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.gh.issueComments = []IssueComment{{ID: 1, URL: "https://example.test/triage", Body: "## Triage decisions\nUse the activity table."}}
	f.claude.turns = []agent.Turn{plannedTurn("sid-1")}
	f.claude.scopeReviews = []agent.ReviewOutcome{{Clean: false, Findings: []task.Finding{{Severity: "blocking", Summary: "PRD selects recipients", Detail: "The PRD selects the recipients table; triage https://example.test/triage selects activity."}}}}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	rec := f.store.recs[key]
	if rec.State != task.NeedsDecision || rec.Pending() == nil || len(f.gh.comments) != 0 || len(f.claude.scopeReviewed) != 1 {
		t.Fatalf("state %s pending %+v posted %d checks %d", rec.State, rec.Pending(), len(f.gh.comments), len(f.claude.scopeReviewed))
	}
}

func TestRemovedLabelStopsBeforeNextStage(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{plannedTurn("sid-1")}
	f.claude.during = func() { f.gh.issue.Labels = nil }
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	rec := f.store.recs[key]
	if rec.State != task.Failed || !strings.Contains(rec.LastOutcome.Summary, "triaged label") || len(f.gh.comments) != 0 {
		t.Fatalf("state %s outcome %+v posted %d", rec.State, rec.LastOutcome, len(f.gh.comments))
	}
}

func TestExistingPlanConflictIsNotAdoptedForEitherTracker(t *testing.T) {
	for _, source := range []task.Source{task.GitHub, task.Linear} {
		t.Run(string(source), func(t *testing.T) {
			f := newFixture()
			var key task.Key
			if source == task.Linear {
				var tracker *lifecycleTracker
				key, tracker = linearTask(t, &f)
				tracker.comments = append([]TrackerComment{{ID: "triage", URL: "https://linear.app/comment/triage", Body: "## Triage decisions\nUse activity."}}, linearPlanComments("", task.PlanDocs...)...)
			} else {
				key = started(t, f, startReq(t))
				f.gh.issueComments = append([]IssueComment{{ID: 400, URL: "https://example.test/triage", Body: "## Triage decisions\nUse activity."}}, githubPlanComments(f, "", task.PlanDocs...)...)
			}
			f.claude.scopeReviews = []agent.ReviewOutcome{{Clean: false, Findings: []task.Finding{{Severity: "blocking", Summary: "PRD uses recipients", Detail: "Triage selects activity; the PRD selects recipients."}}}}
			if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
				t.Fatal(err)
			}
			rec := f.store.recs[key]
			if rec.State != task.NeedsDecision || rec.PlanPosted() || f.claude.starts != 0 {
				t.Fatalf("state %s plan %+v starts %d", rec.State, rec.Plan, f.claude.starts)
			}
		})
	}
}

func TestCorrectionWhileWaitingForCIPreventsHandoff(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2")}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	rec := f.store.recs[key]
	rec.Delegation.RunnerPID = 0
	setCI(f, task.CIStatus{State: task.CIPassing})
	f.gh.issueComments = append(f.gh.issueComments, IssueComment{ID: 50, URL: "https://example.test/correction", Body: "## Triage correction\nUse activity, not recipients."})
	if _, _, err := f.app.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec = f.store.recs[key]
	if rec.State != task.Implementing || rec.Delegation.PlanScopeDigest != "" || len(f.runners.spawned) != 2 {
		t.Fatalf("state %s plan scope %q launches %d", rec.State, rec.Delegation.PlanScopeDigest, len(f.runners.spawned))
	}
}

func TestInconclusivePlanCheckStopsWithoutPosting(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{plannedTurn("sid-1")}
	f.claude.scopeReviews = []agent.ReviewOutcome{{Clean: false, Limitations: []task.Limitation{{Behavior: "scope agreement", Reason: "document is ambiguous", Blocking: true}}}}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	rec := f.store.recs[key]
	if rec.State != task.Failed || !strings.Contains(rec.LastOutcome.Summary, "could not be verified") || len(f.gh.comments) != 0 {
		t.Fatalf("state %s outcome %+v posted %d", rec.State, rec.LastOutcome, len(f.gh.comments))
	}
}

func TestReviewFindsBehaviorThatFollowsPRButConflictsWithTriage(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.gh.issueComments = []IssueComment{{ID: 7, URL: "https://example.test/triage", Body: "## Triage decisions\nUse the campaign Recent Activity table."}}
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2"), {Session: task.Session{Agent: task.Claude, ID: "sid-2"}, Outcome: agent.Outcome{
		Status: agent.Blocked, Summary: "Need scope decision", Questions: []task.Question{{Issue: "How should the table be changed?", Options: []task.Option{{Label: "A", Description: "Use activity."}, {Label: "B", Description: "Correct triage."}}, Recommendation: "A"}},
	}}}
	f.codex.reviews = []agent.ReviewOutcome{{Clean: false, Summary: "wrong table", Findings: []task.Finding{{Severity: "blocking", Summary: "Implementation uses recipients", Detail: "The code follows the PR description but triage requires Recent Activity at https://example.test/triage."}}}}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	rec := f.store.recs[key]
	if rec.State != task.NeedsDecision || len(f.codex.reviewed) != 1 || strings.Contains(f.codex.reviewed[0], "Recent Activity") || len(f.claude.prompts) < 3 || !strings.Contains(f.claude.prompts[2], "Implementation uses recipients") {
		t.Fatalf("state %s review %d prompts %+v", rec.State, len(f.codex.reviewed), f.claude.prompts)
	}
}
