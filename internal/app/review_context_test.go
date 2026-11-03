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

func TestFocusedReviewUsesOnlyItsOwnVerifiedHistory(t *testing.T) {
	for _, kind := range []task.ReviewKind{task.SelfReview, task.CompetingReview} {
		for _, mode := range []string{"valid", "old coverage", "incomplete coverage", "base changed", "scope changed", "plan changed", "rewritten history", "other agent", "missing commit", "unknown base", "read failed", "requirements changed"} {
			t.Run(string(kind)+"/"+mode, func(t *testing.T) {
				f := newFixture()
				key := started(t, f, startReq(t))
				rec := f.store.recs[key]
				d := rec.Delegation
				d.PRHead = strings.Repeat("b", 40)
				d.BaseRef = "origin/main"
				d.ScopeDigest = "scope"
				d.PlanDigest = "plan"
				d.PRDDigest = "requirements"
				d.ClaimVerification = true
				f.wt.reviewBase = strings.Repeat("a", 40)
				reviewer := task.Codex
				if kind == task.SelfReview {
					reviewer = task.Claude
				}
				previous := task.Review{Areas: []task.ReviewArea{{Name: "complete", Paths: []string{"retry.go"}}}, Kind: kind, Reviewer: reviewer, Head: strings.Repeat("c", 40), BaseRef: d.BaseRef, BaseHead: f.wt.reviewBase, Scope: d.ScopeDigest, Plan: d.PlanDigest, PRD: d.PRDDigest, Findings: []task.Finding{{Summary: "own finding"}}}
				switch mode {
				case "old coverage":
					previous.Areas = nil
				case "incomplete coverage":
					previous.Limitations = []task.Limitation{{Behavior: "one file", Reason: "could not inspect"}}
				case "base changed":
					previous.BaseHead = strings.Repeat("d", 40)
				case "scope changed":
					previous.Scope = "old"
				case "plan changed":
					previous.Plan = "old"
				case "rewritten history":
					f.wt.contains = func(string, string, string) (bool, error) { return false, nil }
				case "other agent":
					previous.Reviewer = task.Agent("different")
				case "missing commit":
					previous.Head = "bad; git command"
				case "unknown base":
					previous.BaseHead = ""
				case "read failed":
					f.wt.reviewRangeErr = errors.New("missing local object")
				case "requirements changed":
					previous.PRD = "old"
				}
				other := task.CompetingReview
				if kind == task.CompetingReview {
					other = task.SelfReview
				}
				rec.Reviews = []task.Review{previous, {Kind: other, Findings: []task.Finding{{Summary: "other review finding"}}}}
				brief := f.app.prepareReview(context.Background(), rec, kind, reviewer, agent.ReviewBrief{Passes: 1, BaseBranch: "main"}, io.Discard)
				p := agent.ReviewPrompt(brief)
				focused := mode == "valid" || (kind == task.SelfReview && (mode == "scope changed" || mode == "plan changed" || mode == "requirements changed")) || (kind == task.CompetingReview && (mode == "old coverage" || mode == "incomplete coverage"))
				if focused {
					if brief.PreviousHead != previous.Head || !strings.Contains(p, "git diff "+previous.Head+"..HEAD") || !strings.Contains(p, "own finding") || strings.Contains(p, "other review finding") {
						t.Fatalf("focused prompt %s", p)
					}
				} else if brief.PreviousHead != "" || strings.Contains(p, "own finding") {
					t.Fatalf("unsafe focused review for %s: %s", mode, p)
				}
			})
		}
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

func TestFixLoopUsesFocusedReviewsWithoutSharingFindings(t *testing.T) {
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
		if r.FromHead != "c0ffee1" || r.BaseHead != f.wt.reviewBase {
			t.Fatalf("context %+v", r)
		}
	}
	if strings.Contains(f.claude.reviewed[1], "4xx retried") || !strings.Contains(f.codex.reviewed[1], "4xx retried") {
		t.Fatal("review findings crossed roles or were lost")
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
	_, chosen, err := f.app.review(context.Background(), rec, "title", "body", "", io.Discard)
	if !errors.Is(err, agent.ErrUnavailable) || chosen != task.Codex || len(f.claude.reviewed) != 0 {
		t.Fatalf("chosen %s error %v fallback reviews %d", chosen, err, len(f.claude.reviewed))
	}
}
