package task

import "testing"

func TestLocalReviewHelpersFollowTheStoredCommitAndItsReviews(t *testing.T) {
	local := func(head string, reviews ...Review) *Record {
		return &Record{State: Working, Delegation: &Delegation{ReviewBeforePR: true, LocalHead: head}, Reviews: reviews}
	}
	for name, tc := range map[string]struct {
		rec                        *Record
		localReview, pending, open bool
	}{
		"setting off is never a local review": {
			rec: &Record{State: Working, Delegation: &Delegation{LocalHead: "h1"}},
		},
		"no commit returned yet is not a local review": {
			rec: local(""),
		},
		"an unreviewed commit waits for review": {
			rec: local("h1"), localReview: true, pending: true,
		},
		"a commit after findings waits for review": {
			rec: local("h2", Review{Head: "h1"}), localReview: true, pending: true,
		},
		"a reviewed commit with findings does not wait": {
			rec: local("h1", Review{Head: "h1"}), localReview: true,
		},
		"a clean review of the commit waits for the PR": {
			rec: local("h1", Review{Head: "h1", Clean: true}), localReview: true, open: true,
		},
		"a clean review of an older commit does not open": {
			rec: local("h2", Review{Head: "h1", Clean: true}), localReview: true, pending: true,
		},
		"an undelivered answer goes to the implementer first": {
			rec: func() *Record {
				r := local("h1", Review{Head: "h1"})
				r.Decisions = []Decision{{Answer: "A"}}
				return r
			}(), localReview: true,
		},
		"a task with a PR is not a local review": {
			rec: &Record{State: Working, Delegation: &Delegation{ReviewBeforePR: true, LocalHead: "h1", PR: 7}},
		},
	} {
		if got := tc.rec.LocalReview(); got != tc.localReview {
			t.Errorf("%s: LocalReview() = %t", name, got)
		}
		if got := tc.rec.ReviewPending(); got != tc.pending {
			t.Errorf("%s: ReviewPending() = %t", name, got)
		}
		if got := tc.rec.OpenPending(); got != tc.open {
			t.Errorf("%s: OpenPending() = %t", name, got)
		}
	}
}

func TestPhaseOfAWorkingLocalReviewIsReview(t *testing.T) {
	rec := Record{State: Working, Delegation: &Delegation{ReviewBeforePR: true, LocalHead: "h1"}}
	if got := rec.PhaseOf(); got != PhaseReview {
		t.Fatalf("PhaseOf() = %q, want %q", got, PhaseReview)
	}
}
