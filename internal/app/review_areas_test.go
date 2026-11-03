package app

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

func TestReviewAreasCoverEveryFileAndKeepTestsWithCode(t *testing.T) {
	files := []ReviewFile{{"lib/queue.go", 900}, {"lib/queue_test.go", 150}, {"api/route.ts", 900}, {"api/route.test.ts", 100}, {"ui/view.tsx", 900}, {"docs/design.md", 5}, {"old/name.go", 10}, {"new/name.go", 10}}
	areas := planReviewAreas(files, 3)
	if len(areas) != 3 {
		t.Fatalf("areas=%v", areas)
	}
	owners := map[string]int{}
	for i, a := range areas {
		for _, p := range a.Paths {
			if _, ok := owners[p]; ok {
				t.Fatalf("duplicate ownership %s", p)
			}
			owners[p] = i
		}
	}
	if len(owners) != len(files) || owners["lib/queue.go"] != owners["lib/queue_test.go"] || owners["api/route.ts"] != owners["api/route.test.ts"] {
		t.Fatalf("missing paths or split test ownership: %v", owners)
	}
	reverse := append([]ReviewFile(nil), files...)
	for i, j := 0, len(reverse)-1; i < j; i, j = i+1, j-1 {
		reverse[i], reverse[j] = reverse[j], reverse[i]
	}
	if !reflect.DeepEqual(areas, planReviewAreas(reverse, 3)) {
		t.Fatal("area assignment depends on input order")
	}
	if small := planReviewAreas([]ReviewFile{{"fix.go", 3}, {"fix_test.go", 5}}, 3); len(small) != 1 {
		t.Fatalf("small change fanned out: %v", small)
	}
}

func TestSelfReviewCollectsAllAreasBeforeImplementing(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	rec := f.store.recs[key]
	postedPlan(f, rec)
	f.wt.reviewFiles = []ReviewFile{{"data/save.go", 900}, {"api/read.go", 900}, {"ui/view.go", 900}}
	f.app.ReviewerBudgetUSD = 1.5
	f.claude.turn = prTurn("sid")
	f.claude.reviews = []agent.ReviewOutcome{findings("should_fix"), findings("blocking"), {Clean: true, Summary: "UI checked"}}
	seen := 0
	f.claude.onReview = func() { seen++ }
	// Only the first round uses the large inventory; fixes have a tiny diff.
	f.claude.onResume = func() {
		if seen != 3 {
			t.Errorf("implementer ran after %d areas", seen)
		}
		f.wt.reviewFiles = []ReviewFile{{"data/save.go", 1}}
	}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.WaitingForCI || len(got.Reviews[0].Areas) != 3 || len(got.Reviews[0].Findings) != 1 || got.Reviews[0].Findings[0].Severity != "blocking" {
		t.Fatalf("batch not combined: state %s review %+v", got.State, got.Reviews[0])
	}
	if len(f.claude.reviewed) != 4 || len(f.claude.resumed) != 1 {
		t.Fatalf("reviews %d resumes %d", len(f.claude.reviewed), len(f.claude.resumed))
	}
	for _, budget := range f.claude.reviewBudgets[:3] {
		if budget != 0.5 {
			t.Fatalf("batch did not share budget: %v", f.claude.reviewBudgets)
		}
	}
}

func TestAreaFailureCannotProduceACompletedReview(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	rec := f.store.recs[key]
	postedPlan(f, rec)
	rec.Delegation.PR = 912
	rec.Delegation.PRHead = "c0ffee1"
	f.wt.reviewFiles = []ReviewFile{{"data/save.go", 900}, {"api/read.go", 900}, {"ui/view.go", 900}}
	f.claude.reviews = []agent.ReviewOutcome{findings("blocking")}
	f.claude.reviewErrs = []error{nil, errors.New("review process failed")}
	out, err := f.app.selfReview(context.Background(), rec, io.Discard)
	if err == nil || len(out.Areas) != 0 || len(out.Findings) != 1 || len(f.claude.reviewed) != 2 {
		t.Fatalf("incomplete batch claimed coverage: %+v %v", out, err)
	}
	if len(f.codex.reviewed) != 0 || len(f.claude.resumed) != 0 {
		t.Fatal("area failure advanced the workflow")
	}
}

func TestSelfReviewFollowupOwnsOnlyFixesAndPriorFindingLocations(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	rec := f.store.recs[key]
	postedPlan(f, rec)
	rec.Delegation.PR = 912
	rec.Delegation.PRHead = strings.Repeat("b", 40)
	f.wt.reviewBase = strings.Repeat("a", 40)
	rec.Reviews = []task.Review{{Kind: task.SelfReview, Reviewer: task.Claude, Head: strings.Repeat("c", 40), BaseRef: rec.Delegation.BaseRef, BaseHead: f.wt.reviewBase, Areas: []task.ReviewArea{{Name: "initial", Paths: []string{"old.go", "fixed.go", "unchanged.go"}}}, Findings: []task.Finding{{File: "old.go", Summary: "caller can publish after expiry"}, {File: "unchanged.go", Summary: "rejected concern"}}, RejectedFindings: []task.RejectedFinding{{Finding: 2, Reason: "approved behavior"}}}}
	f.wt.reviewFiles = []ReviewFile{{"fixed.go", 2}}
	out, err := f.app.selfReview(context.Background(), rec, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if out.FromHead != strings.Repeat("c", 40) || len(out.Areas) != 1 || !reflect.DeepEqual(out.Areas[0].Paths, []string{"fixed.go", "old.go"}) {
		t.Fatalf("wrong fix scope: %+v", out)
	}
	p := f.claude.reviewed[0]
	if strings.Contains(p, "rejected concern") || strings.Contains(p, "unchanged.go") || !strings.Contains(p, "caller can publish after expiry") || !strings.Contains(p, "focused re-review") {
		t.Fatalf("wrong followup prompt: %s", p)
	}
}

func TestTinyReviewBudgetDoesNotBecomeUnlimitedThroughRounding(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	rec := f.store.recs[key]
	postedPlan(f, rec)
	rec.Delegation.PR = 912
	rec.Delegation.PRHead = "c0ffee1"
	f.wt.reviewFiles = []ReviewFile{{"data.go", 900}, {"api.go", 900}, {"ui.go", 900}}
	f.app.ReviewerBudgetUSD = 0.01
	out, err := f.app.selfReview(context.Background(), rec, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Areas) != 1 || !reflect.DeepEqual(f.claude.reviewBudgets, []float64{0.01}) {
		t.Fatalf("budget lost: areas=%v budgets=%v", out.Areas, f.claude.reviewBudgets)
	}
}
