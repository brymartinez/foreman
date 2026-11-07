package agent

import (
	"strings"
	"testing"
)

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

func TestDeprecatedUnavailableCheckCannotPassAsClean(t *testing.T) {
	raw := []byte(`{"verdict":"clean","summary":"diff unavailable","findings":[{"category":"unavailable_check","severity":"blocking","file":null,"line":null,"summary":"complete diff","detail":"review shell access was denied"}],"limitations":[]}`)
	if _, err := ParseReview(raw); err == nil {
		t.Fatal("accepted a clean verdict with a blocking unavailable check")
	}
}

func TestParseReviewKeepsTheReviewersExplicitDefectCategory(t *testing.T) {
	raw := []byte(`{"verdict":"changes_requested","summary":"checks unverified","findings":[{"category":"defect","severity":"blocking","file":null,"line":null,"summary":"I could not run go vet or e2e in this review session","detail":"Bash was denied by the review permissions"}],"limitations":[]}`)
	got, err := ParseReview(raw)
	if err != nil || got.Clean || len(got.Findings) != 1 || len(got.Limitations) != 0 {
		t.Fatalf("review rewrote the reported defect: %+v, %v", got, err)
	}
	actualFailure := []byte(`{"verdict":"changes_requested","summary":"vet failed","findings":[{"category":"defect","severity":"blocking","file":null,"line":null,"summary":"go vet failed in the review session","detail":"The package refers to an undefined name"}],"limitations":[]}`)
	got, err = ParseReview(actualFailure)
	if err != nil || got.Clean || len(got.Findings) != 1 {
		t.Fatalf("actual validation failure was hidden: %+v, %v", got, err)
	}
}

func TestParseReviewRejectsInvalidFindingCategories(t *testing.T) {
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

func TestWritingAdviceNeverBecomesABlockingFinding(t *testing.T) {
	raw := `{"verdict":"clean","summary":"Clear requirements","findings":[{"category":"writing_advice","severity":"blocking","file":null,"line":null,"summary":"Use shorter sentences","detail":"Split R1 into two sentences."}],"limitations":[]}`
	if out, err := ParseReview([]byte(raw)); err != nil || !out.Clean || len(out.Findings) != 0 || len(out.Advice) != 1 {
		t.Fatalf("advice %+v error %v", out, err)
	}
	if _, err := ParseReview([]byte(strings.Replace(raw, "clean", "changes_requested", 1))); err == nil {
		t.Fatal("accepted a change request with only writing advice")
	}
	mixed := `{"verdict":"changes_requested","findings":[{"category":"writing_advice","severity":"should_fix","summary":"Shorten R1"},{"category":"defect","severity":"blocking","summary":"Missing retention rule"}]}`
	out, err := ParseReview([]byte(mixed))
	if err != nil || out.Clean || len(out.Findings) != 1 || out.Findings[0].Summary != "Missing retention rule" || len(out.Advice) != 1 {
		t.Fatalf("mixed %+v error %v", out, err)
	}
	unclear := `{"verdict":"clean","findings":[{"category":"writing_advice","severity":"should_fix","summary":"Shorten R1"}],"limitations":[{"behavior":"Retention period","reason":"No period is given","blocking":true}]}`
	if _, err := ParseReview([]byte(unclear)); err == nil {
		t.Fatal("unclear requirement was accepted")
	}
}

func TestIncompleteDiffReviewCannotAdvance(t *testing.T) {
	_, err := ParseReview([]byte(`{"verdict":"changes_requested","summary":"diff unreadable","findings":[],"limitations":[{"behavior":"complete diff","reason":"Git could not read the base commit","blocking":true}]}`))
	if err == nil || !strings.Contains(err.Error(), "Git could not read the base commit") {
		t.Fatalf("expected the incomplete review reason, got %v", err)
	}
}
