package task

import "testing"

func TestClaimVerifiedTasksNeedAReviewOfTheCurrentClaimsAndPRD(t *testing.T) {
	desc := ProposalDigest("feat: x", "Closes #7")
	claims := []Claim{{Criteria: []string{"AC1"}, Behavior: "retries 5xx", Evidence: "client.go", Check: "go test ./..."}}
	edited := []Claim{{Criteria: []string{"AC1"}, Behavior: "retries 5xx and 429", Evidence: "client.go", Check: "go test ./..."}}
	cd, prd := ClaimsDigest(claims), Digest("the PRD")
	onPR := func(flag bool, set []Claim, prdDigest string, reviews ...Review) *Record {
		return &Record{State: SelfReviewing, Delegation: &Delegation{PR: 7, PRHead: "h1", PRDigest: desc,
			ClaimVerification: flag, Claims: set, PRDDigest: prdDigest}, Reviews: reviews}
	}
	local := func(set []Claim, prdDigest string, reviews ...Review) *Record {
		return &Record{State: Implementing, Delegation: &Delegation{ReviewBeforePR: true, LocalHead: "h1", ProposedTitle: "feat: x", ProposedBody: "Closes #7",
			ClaimVerification: true, Claims: set, PRDDigest: prdDigest}, Reviews: reviews}
	}
	self := Review{Kind: SelfReview, PR: 7, Head: "h1", Proposal: desc, Claims: cd, PRD: prd, Clean: true}
	competing := Review{PR: 7, Head: "h1", Proposal: desc, Claims: cd, PRD: prd, Clean: true}
	localSelf := Review{Kind: SelfReview, Head: "h1", Proposal: desc, Claims: cd, PRD: prd, Clean: true}
	localCompeting := Review{Head: "h1", Proposal: desc, Claims: cd, PRD: prd, Clean: true}
	for name, tc := range map[string]struct {
		rec           *Record
		next          ReviewKind
		pending, open bool
	}{
		"a clean self-review of the current claims and PRD passes": {
			rec: onPR(true, claims, prd, self), next: CompetingReview, pending: true,
		},
		"a clean self-review of an older claim set does not pass": {
			rec: onPR(true, edited, prd, self), next: SelfReview, pending: true,
		},
		"a clean self-review of an older PRD does not pass": {
			rec: onPR(true, claims, Digest("the edited PRD"), self), next: SelfReview, pending: true,
		},
		"a clean competing review of the current claims and PRD covers the PR": {
			rec: onPR(true, claims, prd, self, competing), next: CompetingReview,
		},
		"a clean competing review of an older claim set leaves the review pending": {
			rec: onPR(true, edited, prd, self, competing), next: SelfReview, pending: true,
		},
		"a clean competing review of an older PRD leaves the review pending": {
			rec: onPR(true, claims, Digest("the edited PRD"), self, competing), next: SelfReview, pending: true,
		},
		"a task without claim verification ignores the claim and PRD digests": {
			rec: onPR(false, nil, "", Review{Kind: SelfReview, PR: 7, Head: "h1", Proposal: desc, Clean: true}, Review{PR: 7, Head: "h1", Proposal: desc, Clean: true}), next: CompetingReview,
		},
		"a clean local review of the current claims and PRD opens the PR": {
			rec: local(claims, prd, localSelf, localCompeting), next: CompetingReview, open: true,
		},
		"a clean local review of an older claim set does not open the PR": {
			rec: local(edited, prd, localSelf, localCompeting), next: SelfReview, pending: true,
		},
		"a clean local review of an older PRD does not open the PR": {
			rec: local(claims, Digest("the edited PRD"), localSelf, localCompeting), next: SelfReview, pending: true,
		},
	} {
		if got := tc.rec.NextReview(); got != tc.next {
			t.Errorf("%s: NextReview() = %q, want %q", name, got, tc.next)
		}
		if got := tc.rec.ReviewPending(); got != tc.pending {
			t.Errorf("%s: ReviewPending() = %t", name, got)
		}
		if got := tc.rec.OpenPending(); got != tc.open {
			t.Errorf("%s: OpenPending() = %t", name, got)
		}
	}
}

func TestClaimsDigestIgnoresSurroundingSpaceButNotContent(t *testing.T) {
	a := []Claim{{Criteria: []string{"AC1", "AC2"}, Behavior: "retries 5xx", Evidence: "client.go:40", Check: "go test ./..."}}
	b := []Claim{{Criteria: []string{" AC1", "AC2 "}, Behavior: "retries 5xx\n", Evidence: " client.go:40", Check: "go test ./..."}}
	if ClaimsDigest(a) != ClaimsDigest(b) {
		t.Error("the same claims digest differently")
	}
	c := []Claim{{Criteria: []string{"AC1", "AC2"}, Behavior: "retries 5xx", Evidence: "client.go:40", Check: "not run: no network"}}
	if ClaimsDigest(a) == ClaimsDigest(c) {
		t.Error("claims with different checks digest the same")
	}
	if ClaimsDigest(nil) != "" {
		t.Errorf("ClaimsDigest(nil) = %q, want empty", ClaimsDigest(nil))
	}
}

func TestClaimValidateNeedsEveryPart(t *testing.T) {
	good := Claim{Criteria: []string{"AC1"}, Behavior: "retries 5xx", Evidence: "client.go", Check: "go test ./..."}
	if err := good.Validate(); err != nil {
		t.Fatalf("a complete claim: %v", err)
	}
	for name, c := range map[string]Claim{
		"no criteria":    {Behavior: "b", Evidence: "e", Check: "c"},
		"blank criteria": {Criteria: []string{" "}, Behavior: "b", Evidence: "e", Check: "c"},
		"no behavior":    {Criteria: []string{"AC1"}, Evidence: "e", Check: "c"},
		"no evidence":    {Criteria: []string{"AC1"}, Behavior: "b", Check: "c"},
		"no check":       {Criteria: []string{"AC1"}, Behavior: "b", Evidence: "e", Check: " "},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("a claim with %s passed validation", name)
		}
	}
}
