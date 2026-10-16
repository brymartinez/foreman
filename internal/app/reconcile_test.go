package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// planBody is a planning comment as foreman writes it. An empty fingerprint is the
// legacy marker from before plans recorded their description.
func planBody(doc task.PlanDoc, fingerprint string) string {
	return planCommentBody(doc, fingerprint, "Posted "+string(doc)+".")
}

// githubPlanComments are marked planning comments numbered from 501 in PlanDocs order.
func githubPlanComments(f fixture, fingerprint string, docs ...task.PlanDoc) []IssueComment {
	var out []IssueComment
	for i, doc := range docs {
		id := int64(501 + i)
		c := IssueComment{ID: id, URL: fmt.Sprintf("https://github.com/x/issues/825#issuecomment-%d", id), Body: planBody(doc, fingerprint)}
		out = append(out, c)
		if f.gh.comments == nil {
			f.gh.comments = map[int64]IssueComment{}
		}
		f.gh.comments[id] = c
	}
	return out
}

func linearPlanComments(fingerprint string, docs ...task.PlanDoc) []TrackerComment {
	var out []TrackerComment
	for _, doc := range docs {
		id := "lin-" + string(doc)
		out = append(out, TrackerComment{ID: id, URL: "https://linear.app/comment/" + id, Body: planBody(doc, fingerprint)})
	}
	return out
}

func linearTask(t *testing.T, f *fixture) (task.Key, *lifecycleTracker) {
	key := task.Key{Repo: f.gh.repo.Repo, Source: task.Linear, ExternalID: "uuid-42"}
	tracker := &lifecycleTracker{issue: Issue{Key: key, Identifier: "WEB-42", Title: "Fix cache", Body: "Use bounded retries.", URL: "https://linear.app/acme/issue/WEB-42", State: "open"}}
	f.app.Linear = tracker
	started(t, *f, StartRequest{Repo: key.Repo, Key: key, Agent: task.Claude})
	return key, tracker
}

// keepingTurn plans by keeping the documents in keep and writing the others.
func keepingTurn(keep ...task.PlanDoc) agent.Turn {
	turn := plannedTurn("sid-1")
	docs := map[task.PlanDoc]string{}
	for doc, text := range turn.Outcome.Documents {
		if !slices.Contains(keep, doc) {
			docs[doc] = text
		}
	}
	turn.Outcome.Documents = docs
	return turn
}

func TestAFullPlanOnTheGitHubIssueIsAdoptedWithoutPosting(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.gh.issueComments = append([]IssueComment{{ID: 400, Body: "Thanks, looking."}}, githubPlanComments(f, task.DescriptionFingerprint(f.gh.issue.Body), task.PlanDocs...)...)
	posted := len(f.gh.comments)
	f.claude.turns = []agent.Turn{prTurn("sid-1")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Reviewing || !got.PlanPosted() || len(f.gh.comments) != posted || f.gh.edits != 0 {
		t.Fatalf("state %s plan %+v comments %d edits %d", got.State, got.Plan, len(f.gh.comments), f.gh.edits)
	}
	for i, doc := range task.PlanDocs {
		if c := got.PlanComment(doc); c == nil || c.CommentID != int64(501+i) || c.URL != f.gh.issueComments[i+1].URL {
			t.Errorf("%s = %+v", doc, c)
		}
	}
	if !slices.Equal(f.claude.stages, []task.Stage{task.StageImplement}) || !strings.Contains(f.claude.prompt, "- Implementation plan: https://github.com/x/issues/825#issuecomment-504") {
		t.Errorf("the task must implement the adopted plan: stages %v prompt %q", f.claude.stages, f.claude.prompt)
	}
}

func TestAFullLegacyPlanOnTheLinearIssueIsAdoptedWithoutPosting(t *testing.T) {
	f := newFixture()
	key, tracker := linearTask(t, &f)
	tracker.comments = linearPlanComments("", task.PlanDocs...)
	f.claude.turns = []agent.Turn{prTurn("sid-1")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	// The fixture's PR is on another branch, so only the plan and the turn are checked.
	if !got.PlanPosted() || len(tracker.postIDs) != 0 || len(f.gh.comments) != 0 {
		t.Fatalf("plan %+v Linear posts %v", got.Plan, tracker.postIDs)
	}
	for _, doc := range task.PlanDocs {
		if c := got.PlanComment(doc); c == nil || c.ExternalCommentID != "lin-"+string(doc) || c.CommentID != 0 {
			t.Errorf("%s = %+v", doc, c)
		}
	}
	if !slices.Equal(f.claude.stages, []task.Stage{task.StageImplement}) {
		t.Errorf("stages = %v", f.claude.stages)
	}
}

func TestAPartialPlanOnTheGitHubIssueGoesToPlanningAndOnlyMissingDocumentsArePosted(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.gh.issueComments = githubPlanComments(f, "", task.Analysis, task.PRD)
	turn := keepingTurn(task.Analysis)
	f.claude.turns = []agent.Turn{turn, prTurn("sid-2")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if !got.PlanPosted() || got.PlanComment(task.Analysis).CommentID != 501 || got.PlanComment(task.PRD).CommentID != 502 {
		t.Fatalf("plan %+v", got.Plan)
	}
	// The PRD was revised in place; the design and plan are the only new comments.
	if f.gh.edits != 1 || len(f.gh.comments) != 4 || f.gh.comments[501].Body != planBody(task.Analysis, "") || !strings.Contains(f.gh.comments[502].Body, "R1. Retry 5xx.") {
		t.Errorf("edits %d comments %+v", f.gh.edits, f.gh.comments)
	}
	if p := f.claude.prompts[0]; !strings.Contains(p, "## Documents already on the issue") || !strings.Contains(p, "### Analysis\n\nPosted analysis.") || !strings.Contains(p, "Write every missing document: design, plan.") {
		t.Errorf("the planning prompt must carry the found documents:\n%s", p)
	}
}

func TestAPartialPlanOnTheLinearIssueKeepsValidCommentsAndPostsOnlyMissingOnes(t *testing.T) {
	f := newFixture()
	key, tracker := linearTask(t, &f)
	tracker.comments = linearPlanComments(task.DescriptionFingerprint(tracker.issue.Body), task.Analysis, task.PRD, task.Design)
	f.claude.turns = []agent.Turn{keepingTurn(task.Analysis, task.PRD, task.Design), prTurn("sid-2")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if !got.PlanPosted() || !slices.Equal(tracker.postIDs, []string{""}) || got.PlanComment(task.Design).ExternalCommentID != "lin-design" {
		t.Fatalf("plan %+v post IDs %v", got.Plan, tracker.postIDs)
	}
}

func TestAPlanningTurnThatLeavesAMissingDocumentEmptyFailsWithoutPosting(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.gh.issueComments = githubPlanComments(f, "", task.Analysis)
	f.claude.turns = []agent.Turn{keepingTurn(task.Analysis, task.Design)}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || got.LastOutcome.Summary != "the planning turn wrote no Design" || len(f.gh.comments) != 1 || f.gh.edits != 0 {
		t.Fatalf("state %s outcome %+v comments %d", got.State, got.LastOutcome, len(f.gh.comments))
	}
}

func TestAPlanWrittenForAnEarlierDescriptionAsksTheDeveloper(t *testing.T) {
	stale := task.DescriptionFingerprint("An older description.")
	t.Run("GitHub", func(t *testing.T) {
		f := newFixture()
		key := started(t, f, startReq(t))
		f.gh.issueComments = githubPlanComments(f, stale, task.PlanDocs...)
		if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
			t.Fatal(err)
		}
		got := f.store.recs[key]
		q := got.Pending()
		if got.State != task.NeedsDecision || q == nil || q.Question.Issue != planConflictIssue || !strings.Contains(q.Question.Context, "- PRD: https://github.com/x/issues/825#issuecomment-502") {
			t.Fatalf("state %s decisions %+v", got.State, got.Decisions)
		}
		if f.claude.starts != 0 || len(f.gh.comments) != 4 || f.gh.edits != 0 || len(got.Plan) != 0 {
			t.Errorf("starts %d comments %d edits %d plan %+v", f.claude.starts, len(f.gh.comments), f.gh.edits, got.Plan)
		}
	})
	t.Run("Linear", func(t *testing.T) {
		f := newFixture()
		key, tracker := linearTask(t, &f)
		tracker.comments = linearPlanComments(stale, task.Analysis)
		if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
			t.Fatal(err)
		}
		got := f.store.recs[key]
		if q := got.Pending(); got.State != task.NeedsDecision || q == nil || !strings.Contains(q.Question.Context, "- Analysis: https://linear.app/comment/lin-analysis") {
			t.Fatalf("state %s decisions %+v", got.State, got.Decisions)
		}
		if f.claude.starts != 0 || len(tracker.postIDs) != 0 {
			t.Errorf("starts %d post IDs %v", f.claude.starts, tracker.postIDs)
		}
	})
}

// answerPlanConflict blocks the task on a stale plan, then records the answer.
func answerPlanConflict(t *testing.T, f fixture, key task.Key, answer task.Option) {
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	rec := f.store.recs[key]
	if rec.State != task.NeedsDecision {
		t.Fatalf("state %s, want a decision", rec.State)
	}
	rec.Pending().Answer = optionText(answer)
	rec.State = task.Working
}

func TestKeepingAConflictingPlanAdoptsItWithoutPosting(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.gh.issueComments = githubPlanComments(f, task.DescriptionFingerprint("An older description."), task.PlanDocs...)
	answerPlanConflict(t, f, key, keepPlanOption)
	f.claude.turns = []agent.Turn{prTurn("sid-1")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Reviewing || !got.PlanPosted() || len(f.gh.comments) != 4 || f.gh.edits != 0 || !slices.Equal(f.claude.stages, []task.Stage{task.StageImplement}) {
		t.Fatalf("state %s plan %+v comments %d edits %d stages %v", got.State, got.Plan, len(f.gh.comments), f.gh.edits, f.claude.stages)
	}
}

func TestRevisingAConflictingPlanEditsItsCommentsForTheCurrentDescription(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.gh.issueComments = githubPlanComments(f, task.DescriptionFingerprint("An older description."), task.PlanDocs...)
	answerPlanConflict(t, f, key, revisePlanOption)
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if !got.PlanPosted() || len(f.gh.comments) != 4 || f.gh.edits != 4 || f.claude.starts != 2 {
		t.Fatalf("plan %+v comments %d edits %d starts %d", got.Plan, len(f.gh.comments), f.gh.edits, f.claude.starts)
	}
	fp := task.DescriptionFingerprint(f.gh.issue.Body)
	if _, gotFP, _ := task.ParsePlanMarker(f.gh.comments[503].Body); gotFP != fp {
		t.Errorf("revised design marker fingerprint %q, want %q", gotFP, fp)
	}
	if p := f.claude.prompts[0]; !strings.Contains(p, "## Documents already on the issue") || !strings.Contains(p, optionText(revisePlanOption)) {
		t.Errorf("the planning prompt must carry the documents and the answer:\n%s", p)
	}
}

func TestAnUnreadableIssueFailsTheTaskBeforePlanning(t *testing.T) {
	readErr := errors.New("HTTP 502")
	t.Run("GitHub", func(t *testing.T) {
		f := newFixture()
		key := started(t, f, startReq(t))
		f.gh.issueCommentsErr = readErr
		if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
			t.Fatal(err)
		}
		got := f.store.recs[key]
		if got.State != task.Failed || got.LastOutcome.Summary != "reading the issue to reconcile its plan failed: HTTP 502" || f.claude.starts != 0 || len(f.gh.comments) != 0 {
			t.Fatalf("state %s outcome %+v starts %d comments %d", got.State, got.LastOutcome, f.claude.starts, len(f.gh.comments))
		}
	})
	t.Run("Linear", func(t *testing.T) {
		f := newFixture()
		key, tracker := linearTask(t, &f)
		tracker.commentsErr = readErr
		if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
			t.Fatal(err)
		}
		got := f.store.recs[key]
		if got.State != task.Failed || !strings.HasSuffix(got.LastOutcome.Summary, "HTTP 502") || f.claude.starts != 0 || len(tracker.postIDs) != 0 {
			t.Fatalf("state %s outcome %+v starts %d post IDs %v", got.State, got.LastOutcome, f.claude.starts, tracker.postIDs)
		}
	})
}

func TestOnlyTheNewestMarkedCommentCountsAsAPlanningDocument(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.gh.issue.Body = "Retry failed requests.\n\n## Analysis\n\nWritten in the description."
	older := githubPlanComments(f, "", task.Analysis, task.Analysis)
	f.gh.issueComments = []IssueComment{
		{ID: 400, Body: "## PRD\n\nAn unmarked PRD."},
		{ID: 401, Body: agent.ReplyMarker + " Done."},
		older[0], older[1],
	}
	f.gh.comments[400], f.gh.comments[401] = f.gh.issueComments[0], f.gh.issueComments[1]
	f.claude.turns = []agent.Turn{keepingTurn(task.Analysis), prTurn("sid-2")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if !got.PlanPosted() || got.PlanComment(task.Analysis).CommentID != 502 || f.gh.edits != 0 || len(f.gh.comments) != 7 {
		t.Fatalf("plan %+v edits %d comments %d", got.Plan, f.gh.edits, len(f.gh.comments))
	}
	if f.gh.comments[400].Body != "## PRD\n\nAn unmarked PRD." || f.gh.comments[401].Body != agent.ReplyMarker+" Done." {
		t.Errorf("unrelated comments changed: %+v", f.gh.comments)
	}
}

func TestARecordedCommentThatLostItsMarkerIsNotEdited(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	unmarked := IssueComment{ID: 77, URL: "https://github.com/x/issues/825#issuecomment-77", Body: "## Analysis\n\nThe marker was removed."}
	f.gh.comments = map[int64]IssueComment{77: unmarked}
	f.gh.issueComments = []IssueComment{unmarked}
	f.store.recs[key].SetPlanComment(task.PlanComment{Doc: task.Analysis, CommentID: 77, URL: unmarked.URL})
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if c := got.PlanComment(task.Analysis); !got.PlanPosted() || c.CommentID == 77 || f.gh.edits != 0 || f.gh.comments[77].Body != unmarked.Body {
		t.Fatalf("plan %+v edits %d comment 77 %q", got.Plan, f.gh.edits, f.gh.comments[77].Body)
	}
}

func TestAKeepAnswerForAnEarlierDescriptionAsksAgain(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.gh.issueComments = githubPlanComments(f, task.DescriptionFingerprint("An older description."), task.PlanDocs...)
	answerPlanConflict(t, f, key, keepPlanOption)
	f.gh.issue.Body = "The description changed again."
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if q := got.Pending(); got.State != task.NeedsDecision || q == nil || q.Question.Issue != planConflictIssue || len(got.Plan) != 0 || f.claude.starts != 0 {
		t.Fatalf("state %s decisions %+v plan %+v starts %d", got.State, got.Decisions, got.Plan, f.claude.starts)
	}
}

func TestAResumedPlanningTurnReceivesDocumentsFoundOnTheIssue(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	rec := f.store.recs[key]
	rec.Delegation.Implementer.ID = "sid-1"
	rec.Decisions = []task.Decision{{Question: task.Question{Issue: "Retry 4xx?"}, Answer: "A. 5xx only", AnsweredAt: f.app.Now()}}
	f.gh.issueComments = githubPlanComments(f, "", task.Analysis)
	f.claude.turns = []agent.Turn{keepingTurn(task.Analysis), prTurn("sid-2")}
	if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(f.claude.resumed) != 1 || !strings.Contains(f.claude.resumed[0], "## Documents already on the issue") || !strings.Contains(f.claude.resumed[0], "### Analysis\n\nPosted analysis.") {
		t.Fatalf("resumed %q", f.claude.resumed)
	}
	if got := f.store.recs[key]; !got.PlanPosted() || got.PlanComment(task.Analysis).CommentID != 501 || f.gh.edits != 0 {
		t.Errorf("plan %+v edits %d", got.Plan, f.gh.edits)
	}
}
