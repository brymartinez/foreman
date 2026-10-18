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
		p := ReviewPrompt(b)
		for _, want := range []string{"## PRD", "> - AC1. 5xx responses are retried.", "acceptance criteria",
			"## Implementer's claims", "only 5xx is retried", "retry.go:40, retry_test.go TestRetry", "go test ./... passed",
			"criterion no claim covers", "counterexample", "not proof", "limitation", "blocking", "> Retry 5xx responses only."} {
			if !strings.Contains(p, want) {
				t.Errorf("self %t: review prompt lacks %q:\n%s", self, want, p)
			}
		}
		if strings.Contains(p, "only statement of what the change must do") {
			t.Errorf("self %t: the description is still the only requirements", self)
		}
	}
}

func TestReviewPromptWithoutClaimsKeepsTheDescriptionAsTheOnlyRequirements(t *testing.T) {
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
	got, err := ParseReview([]byte(`{"verdict":"changes_requested","summary":"s","findings":[{"severity":"blocking","file":null,"line":null,"summary":"add a test for the Linear PRD load","detail":""}],
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
