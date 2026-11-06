package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

func TestReviewStopsWhenTheBaseCannotBeResolved(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	postedPlan(f, f.store.recs[key])
	f.claude.turn = prTurn("sid")
	f.wt.reviewBaseErr = errors.New("missing local object")
	rec := run(t, f, key)
	if rec.State != task.Failed || !strings.Contains(rec.LastOutcome.Summary, "missing local object") || len(rec.Reviews) != 0 || len(f.claude.reviewed)+len(f.codex.reviewed) != 0 {
		t.Fatalf("state %s, outcome %+v, reviews %+v", rec.State, rec.LastOutcome, rec.Reviews)
	}
}

func TestWritingAdvicePostsPlanWithoutRevisionAndKeepsLocalNotes(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{plannedTurn("plan"), prTurn("implementation")}
	f.claude.scopeReviews = []agent.ReviewOutcome{{Clean: true, Advice: []task.Advice{{Summary: "Shorter sentences", Detail: "Split the first sentence."}}}}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	rec := f.store.recs[key]
	if !rec.PlanPosted() || len(f.claude.resumed) != 0 || len(f.claude.scopeReviewed) != 1 || len(rec.PlanningAdvice) != 1 || rec.Delegation.PlanDraft != nil {
		t.Fatalf("state %s advice %+v", rec.State, rec.PlanningAdvice)
	}
	for _, comment := range f.gh.comments {
		if strings.Contains(comment.Body, "Shorter sentences") {
			t.Fatal("writing advice was posted to the issue")
		}
	}
}

func TestAdoptingPlanKeepsWritingAdviceWithoutARevision(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	postedPlan(f, f.store.recs[key])
	f.store.recs[key].Delegation.PlanDigest = ""
	f.store.recs[key].Delegation.PlanScopeDigest = ""
	f.claude.scopeReviews = []agent.ReviewOutcome{{Clean: true, Advice: []task.Advice{{Summary: "Shorten R1"}}}}
	f.claude.turns = []agent.Turn{prTurn("implementation")}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if rec := f.store.recs[key]; len(rec.PlanningAdvice) != 1 || len(f.claude.resumed) != 0 || !rec.PlanPosted() {
		t.Fatalf("state %s advice %+v", rec.State, rec.PlanningAdvice)
	}
}

func TestFixLoopReviewsTheCompleteDiffWithoutSharingFindings(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	postedPlan(f, f.store.recs[key])
	f.wt.reviewBase = strings.Repeat("a", 40)
	f.claude.turns = []agent.Turn{prTurn("first"), prTurn("fix")}
	f.codex.reviews = []agent.ReviewOutcome{findings("blocking")}
	f.claude.during = func() {
		if f.claude.starts+len(f.claude.resumed) > 1 {
			pushHead(f, "c0ffee2")
		}
	}
	f.claude.onResume = f.claude.during
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	rec := f.store.recs[key]
	if kinds(rec) != "self competing! self competing" {
		t.Fatalf("reviews %s", kinds(rec))
	}
	for _, r := range rec.Reviews[2:] {
		if r.FromHead != "" || r.BaseHead != f.wt.reviewBase || r.Head != "c0ffee2" {
			t.Fatalf("context %+v", r)
		}
	}
	for _, reviews := range [][]string{f.claude.reviewed, f.codex.reviewed} {
		if len(reviews) != 2 || !strings.Contains(reviews[1], "git diff --no-ext-diff --no-textconv --ignore-submodules=none "+f.wt.reviewBase+"...c0ffee2 --") || strings.Contains(reviews[1], "4xx retried") {
			t.Fatalf("follow-up review omitted the complete diff or included prior findings: %q", reviews)
		}
	}
}

func TestUnavailableAdversarialReviewerDoesNotFallBack(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	rec := f.store.recs[key]
	postedPlan(f, rec)
	rec.Delegation.PR = 912
	rec.Delegation.PRHead = "c0ffee2"
	f.codex.reviewErr = agent.ErrUnavailable
	_, err := f.app.codeReview(context.Background(), rec, task.CompetingReview, task.Codex, io.Discard)
	if !errors.Is(err, agent.ErrUnavailable) || len(f.claude.reviewed) != 0 {
		t.Fatalf("error %v fallback reviews %d", err, len(f.claude.reviewed))
	}
}
