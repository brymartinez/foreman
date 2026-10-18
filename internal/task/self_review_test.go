package task

import "testing"

func TestNextReviewIsTheSelfReviewUntilItPassesTheCurrentChange(t *testing.T) {
	desc := ProposalDigest("feat: x", "Closes #7")
	other := ProposalDigest("feat: x", "Closes #7, retries 5xx")
	onPR := func(head, digest string, reviews ...Review) *Record {
		return &Record{State: Reviewing, Delegation: &Delegation{PR: 7, PRHead: head, PRDigest: digest}, Reviews: reviews}
	}
	local := func(head string, reviews ...Review) *Record {
		return &Record{State: Working, Delegation: &Delegation{ReviewBeforePR: true, LocalHead: head, ProposedTitle: "feat: x", ProposedBody: "Closes #7"}, Reviews: reviews}
	}
	for name, tc := range map[string]struct {
		rec  *Record
		want ReviewKind
	}{
		"a PR with no review needs the self-review": {
			rec: onPR("h1", desc), want: SelfReview,
		},
		"a clean self-review of the PR head and description passes": {
			rec: onPR("h1", desc, Review{Kind: SelfReview, PR: 7, Head: "h1", Proposal: desc, Clean: true}), want: CompetingReview,
		},
		"a clean self-review of an older head does not pass": {
			rec: onPR("h2", desc, Review{Kind: SelfReview, PR: 7, Head: "h1", Proposal: desc, Clean: true}), want: SelfReview,
		},
		"a clean self-review of another PR description does not pass": {
			rec: onPR("h1", desc, Review{Kind: SelfReview, PR: 7, Head: "h1", Proposal: other, Clean: true}), want: SelfReview,
		},
		"a self-review with findings does not pass": {
			rec: onPR("h1", desc, Review{Kind: SelfReview, PR: 7, Head: "h1", Proposal: desc}), want: SelfReview,
		},
		"a competing review of the head does not stand in for the self-review": {
			rec: onPR("h2", desc,
				Review{Kind: SelfReview, PR: 7, Head: "h1", Proposal: desc, Clean: true},
				Review{PR: 7, Head: "h2", Clean: true}), want: SelfReview,
		},
		"a clean local self-review of the commit and proposal passes": {
			rec: local("h1", Review{Kind: SelfReview, Head: "h1", Proposal: desc, Clean: true}), want: CompetingReview,
		},
		"a clean local self-review of another proposal does not pass": {
			rec: local("h1", Review{Kind: SelfReview, Head: "h1", Proposal: other, Clean: true}), want: SelfReview,
		},
	} {
		if got := tc.rec.NextReview(); got != tc.want {
			t.Errorf("%s: NextReview() = %q, want %q", name, got, tc.want)
		}
	}
}

func TestReviewPendingWaitsForTheCompetingReviewOfTheCurrentChange(t *testing.T) {
	desc := ProposalDigest("feat: x", "Closes #7")
	onPR := func(head string, reviews ...Review) *Record {
		return &Record{State: Reviewing, Delegation: &Delegation{PR: 7, PRHead: head, PRDigest: desc}, Reviews: reviews}
	}
	local := func(head string, reviews ...Review) *Record {
		return &Record{State: Working, Delegation: &Delegation{ReviewBeforePR: true, LocalHead: head, ProposedTitle: "feat: x", ProposedBody: "Closes #7"}, Reviews: reviews}
	}
	selfClean := Review{Kind: SelfReview, PR: 7, Head: "h1", Proposal: desc, Clean: true}
	for name, tc := range map[string]struct {
		rec           *Record
		pending, open bool
	}{
		"a clean self-review alone leaves the PR review pending": {
			rec: onPR("h1", selfClean), pending: true,
		},
		"self-review findings on the PR head go to the implementer": {
			rec: onPR("h1", Review{Kind: SelfReview, PR: 7, Head: "h1", Proposal: desc}),
		},
		"self-review findings on an older head leave the review pending": {
			rec: onPR("h2", Review{Kind: SelfReview, PR: 7, Head: "h1", Proposal: desc}), pending: true,
		},
		"a clean competing review of the PR head is not pending": {
			rec: onPR("h1", selfClean, Review{PR: 7, Head: "h1", Clean: true}),
		},
		"an old record's clean review with no kind still covers its head": {
			rec: onPR("h1", Review{PR: 7, Head: "h1", Clean: true}),
		},
		"a clean local self-review alone does not open the PR": {
			rec: local("h1", Review{Kind: SelfReview, Head: "h1", Proposal: desc, Clean: true}), pending: true,
		},
		"a clean local competing review opens the PR": {
			rec:  local("h1", Review{Kind: SelfReview, Head: "h1", Proposal: desc, Clean: true}, Review{Head: "h1", Proposal: desc, Clean: true}),
			open: true,
		},
		"local self-review findings go to the implementer": {
			rec: local("h1", Review{Kind: SelfReview, Head: "h1", Proposal: desc}),
		},
	} {
		if got := tc.rec.ReviewPending(); got != tc.pending {
			t.Errorf("%s: ReviewPending() = %t", name, got)
		}
		if got := tc.rec.OpenPending(); got != tc.open {
			t.Errorf("%s: OpenPending() = %t", name, got)
		}
	}
}

func TestLatestReviewOfEachKind(t *testing.T) {
	rec := &Record{Reviews: []Review{{Round: 1, Kind: SelfReview}, {Round: 2}, {Round: 3, Kind: SelfReview}}}
	if got := rec.LatestSelf(); got == nil || got.Round != 3 {
		t.Errorf("LatestSelf() = %+v, want round 3", got)
	}
	if got := rec.LatestCompeting(); got == nil || got.Round != 2 {
		t.Errorf("LatestCompeting() = %+v, want round 2", got)
	}
	if got := rec.LatestReview(); got == nil || got.Round != 3 {
		t.Errorf("LatestReview() = %+v, want round 3", got)
	}
	if got := (&Record{Reviews: []Review{{Kind: SelfReview}}}).LatestCompeting(); got != nil {
		t.Errorf("LatestCompeting() with only self-reviews = %+v, want nil", got)
	}
}

func TestChangeRequestsFromBothReviewersShareOneCount(t *testing.T) {
	rec := &Record{Reviews: []Review{{Kind: SelfReview}, {Kind: SelfReview, Clean: true}, {}, {Kind: SelfReview}}}
	for i := range rec.Reviews {
		rec.Reviews[i].At = rec.Reviews[i].At.AddDate(2026, 0, i)
	}
	if got := rec.ChangeRequestsSinceDecision(); got != 3 {
		t.Fatalf("ChangeRequestsSinceDecision() = %d, want 3", got)
	}
}

func TestPhaseOfAReviewingTaskWithOnlyASelfReviewIsReview(t *testing.T) {
	rec := Record{State: Reviewing, Delegation: &Delegation{PR: 7, PRHead: "h1"}, Reviews: []Review{{Kind: SelfReview, PR: 7, Head: "h1", Clean: true}}}
	if got := rec.PhaseOf(); got != PhaseReview {
		t.Fatalf("PhaseOf() = %q, want %q", got, PhaseReview)
	}
}
