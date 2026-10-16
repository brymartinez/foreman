package task

import "testing"

func TestPlanMarkerRoundTripsWithAFingerprint(t *testing.T) {
	fp := DescriptionFingerprint("Retry 5xx.\n")
	if len(fp) != 12 || fp != DescriptionFingerprint("  Retry 5xx.") {
		t.Fatalf("fingerprint %q must be 12 hex characters of the trimmed description", fp)
	}
	for _, doc := range PlanDocs {
		got, gotFP, ok := ParsePlanMarker(PlanMarker(doc, fp) + "\n## " + doc.Title() + "\n\ntext")
		if !ok || got != doc || gotFP != fp {
			t.Errorf("%s: got %q %q %v", doc, got, gotFP, ok)
		}
	}
}

func TestParsePlanMarkerAcceptsALegacyMarkerWithoutAFingerprint(t *testing.T) {
	doc, fp, ok := ParsePlanMarker("<!-- foreman:prd -->\n## PRD\n\nR1.")
	if !ok || doc != PRD || fp != "" {
		t.Fatalf("got %q %q %v", doc, fp, ok)
	}
}

func TestParsePlanMarkerRejectsCommentsThatAreNotPlanningDocuments(t *testing.T) {
	for _, body := range []string{
		"<!-- foreman:reply --> Done.",
		"## Analysis\n\nNo marker.",
		"See the plan:\n<!-- foreman:plan -->",
		"<!-- foreman:plan desc=zz -->\n## Implementation plan",
		"<!-- foreman:plan extra -->",
		"<!-- foreman:plans -->",
		"",
	} {
		if doc, _, ok := ParsePlanMarker(body); ok {
			t.Errorf("ParsePlanMarker(%q) = %q, want no document", body, doc)
		}
	}
}
