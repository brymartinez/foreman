package agent

import (
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func claimedReviewBrief() ReviewBrief {
	b := reviewBrief()
	b.Claimed = true
	b.PRD = "## Acceptance criteria\n\n- AC1. 5xx responses are retried.\n- AC2. 4xx responses are not."
	b.Claims = []task.Claim{{Criteria: []string{"AC1", "AC2"}, Behavior: "only 5xx is retried", Evidence: "retry.go:40, retry_test.go TestRetry", Check: "go test ./... passed"}}
	return b
}

func TestClaimedReviewPromptCarriesThePRDAndTheClaimsToTest(t *testing.T) {
	for _, self := range []bool{false, true} {
		b := claimedReviewBrief()
		b.Self = self
		b.Plan = "R1. Retry only 5xx."
		p := ReviewPrompt(b)
		for _, want := range []string{"## PRD", "> - AC1. 5xx responses are retried.", "acceptance criteria",
			"## Implementer's claims", "only 5xx is retried", "retry.go:40, retry_test.go TestRetry", "go test ./... passed",
			"criterion no claim covers", "counterexample", "not proof", "limitation", "blocking", "nonblocking",
			"belongs to implementation validation", "Do not run tests, builds"} {
			if !strings.Contains(p, want) {
				t.Errorf("self %t: review prompt lacks %q:\n%s", self, want, p)
			}
		}
		if strings.Contains(p, "only statement of what the change must do") {
			t.Errorf("self %t: the description is still the only requirements", self)
		}
		if strings.Contains(p, "Retry 5xx responses only.") {
			t.Errorf("self %t: the PR description leaked into review", self)
		}
	}
}

func TestReviewPromptWithoutClaimsOmitsClaimRequirements(t *testing.T) {
	p := ReviewPrompt(reviewBrief())
	for _, unwanted := range []string{"## PRD", "## Implementer's claims", "counterexample to each claim"} {
		if strings.Contains(p, unwanted) {
			t.Errorf("review prompt of a task without claims has %q", unwanted)
		}
	}
	if !strings.Contains(p, "Set limitations to null") {
		t.Error("the prompt does not say what to put in the required limitations field")
	}
}

func TestParseReviewReturnsLimitations(t *testing.T) {
	got, err := ParseReview([]byte(`{"verdict":"clean","summary":"ok","findings":[],"limitations":[{"behavior":"retry under load","reason":"no load harness","blocking":false}]}`))
	if err != nil || !got.Clean || len(got.Limitations) != 1 || got.Limitations[0].Reason != "no load harness" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got, err := ParseReview([]byte(`{"verdict":"clean","summary":"ok","findings":[],"limitations":null}`)); err != nil || len(got.Limitations) != 0 {
		t.Fatalf("null limitations: %+v, %v", got, err)
	}
}

func TestParseReviewRejectsACleanVerdictWithABlockingLimitation(t *testing.T) {
	_, err := ParseReview([]byte(`{"verdict":"clean","summary":"ok","findings":[],"limitations":[{"behavior":"Linear PRD load","reason":"no token","blocking":true}]}`))
	if err == nil || !strings.Contains(err.Error(), "blocking limitation") {
		t.Fatalf("err = %v", err)
	}
	got, err := ParseReview([]byte(`{"verdict":"changes_requested","summary":"s","findings":[{"category":"defect","severity":"blocking","file":null,"line":null,"summary":"add a test for the Linear PRD load","detail":""}],
"limitations":[{"behavior":"Linear PRD load","reason":"no token","blocking":true}]}`))
	if err != nil || got.Clean || !got.Limitations[0].Blocking {
		t.Fatalf("changes requested with a blocking limitation: %+v, %v", got, err)
	}
}

func TestParseReviewRejectsAnIncompleteLimitation(t *testing.T) {
	for _, lim := range []string{`{"behavior":" ","reason":"r","blocking":false}`, `{"behavior":"b","reason":"","blocking":false}`} {
		if _, err := ParseReview([]byte(`{"verdict":"clean","summary":"ok","findings":[],"limitations":[` + lim + `]}`)); err == nil {
			t.Errorf("accepted %s", lim)
		}
	}
}

func TestParseReviewRejectsABlockingLimitationWithoutABlockingFinding(t *testing.T) {
	_, err := ParseReview([]byte(`{"verdict":"changes_requested","summary":"s","findings":[{"category":"defect","severity":"should_fix","file":null,"line":null,"summary":"rename retry","detail":""}],
"limitations":[{"behavior":"Linear PRD load","reason":"no token","blocking":true}]}`))
	if err == nil || !strings.Contains(err.Error(), "blocking finding") {
		t.Fatalf("err = %v, want a blocking limitation without a blocking finding rejected", err)
	}
}

func TestUnavailableReviewCheckCannotRequestChanges(t *testing.T) {
	raw := []byte(`{"verdict":"changes_requested","summary":"I could not run the gates","findings":[{"category":"unavailable_check","severity":"blocking","file":null,"line":null,"summary":"go vet and e2e","detail":"review shell access was denied"}],"limitations":[]}`)
	got, err := ParseReview(raw)
	if err != nil || !got.Clean || len(got.Findings) != 0 || len(got.Limitations) != 1 || got.Limitations[0].Blocking {
		t.Fatalf("unavailable check blocked review: %+v, %v", got, err)
	}
	if got.Limitations[0].Behavior != "go vet and e2e" || got.Limitations[0].Reason != "review shell access was denied" {
		t.Fatalf("unavailable check lost its reason: %+v", got.Limitations[0])
	}
}

func TestUnavailableReviewCheckDoesNotHideADefect(t *testing.T) {
	raw := []byte(`{"verdict":"changes_requested","summary":"one defect and one limit","findings":[{"category":"unavailable_check","severity":"blocking","file":null,"line":null,"summary":"go vet","detail":"shell denied"},{"category":"defect","severity":"blocking","file":"retry.go","line":40,"summary":"4xx retried","detail":"guard missing"}],"limitations":[]}`)
	got, err := ParseReview(raw)
	if err != nil || got.Clean || len(got.Findings) != 1 || got.Findings[0].Summary != "4xx retried" || len(got.Limitations) != 1 || got.Limitations[0].Blocking {
		t.Fatalf("defect or limitation lost: %+v, %v", got, err)
	}
}

func TestDeniedReviewCommandsAreNotCodeDefectsWhenMislabeled(t *testing.T) {
	raw := []byte(`{"verdict":"changes_requested","summary":"checks unverified","findings":[{"category":"defect","severity":"blocking","file":null,"line":null,"summary":"I could not run go vet or e2e in this review session","detail":"Bash was denied by the review permissions"}],"limitations":[]}`)
	got, err := ParseReview(raw)
	if err != nil || !got.Clean || len(got.Findings) != 0 || len(got.Limitations) != 1 || got.Limitations[0].Blocking {
		t.Fatalf("denied review commands blocked review: %+v, %v", got, err)
	}
	actualFailure := []byte(`{"verdict":"changes_requested","summary":"vet failed","findings":[{"category":"defect","severity":"blocking","file":null,"line":null,"summary":"go vet failed in the review session","detail":"The package refers to an undefined name"}],"limitations":[]}`)
	got, err = ParseReview(actualFailure)
	if err != nil || got.Clean || len(got.Findings) != 1 {
		t.Fatalf("actual validation failure was hidden: %+v, %v", got, err)
	}
}

func TestUnavailableReviewCheckNeedsAReasonAndNoCodeLocation(t *testing.T) {
	for _, raw := range []string{
		`{"verdict":"changes_requested","findings":[{"category":"unavailable_check","severity":"blocking","file":"retry.go","line":40,"summary":"go vet","detail":"shell denied"}]}`,
		`{"verdict":"changes_requested","findings":[{"category":"unavailable_check","severity":"blocking","file":null,"line":null,"summary":"go vet","detail":""}]}`,
		`{"verdict":"changes_requested","findings":[{"category":"other","severity":"blocking","file":null,"line":null,"summary":"go vet","detail":"shell denied"}]}`,
		`{"verdict":"changes_requested","findings":[{"severity":"blocking","file":null,"line":null,"summary":"go vet","detail":"shell denied"}]}`,
	} {
		if _, err := ParseReview([]byte(raw)); err == nil {
			t.Errorf("accepted invalid finding: %s", raw)
		}
	}
}
