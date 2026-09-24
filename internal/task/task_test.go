package task

import "testing"

func question(rec string) Question {
	return Question{Issue: "Retry 4xx?", Recommendation: rec, Options: []Option{
		{Label: "A", Description: "5xx only"}, {Label: "B", Description: "4xx + 5xx"}, {Label: "AB", Description: "both, gated"},
	}}
}

func TestRecommended(t *testing.T) {
	for rec, want := range map[string]string{
		"A":                         "A",
		"b":                         "B",
		"A. preserves semantics":    "A",
		"A: because":                "A",
		"Option B – broader":        "B",
		"AB":                        "AB",
		"AB — gated":                "AB",
		"Apples":                    "",
		"Keep the current behavior": "",
	} {
		got, ok := question(rec).Recommended()
		if (want == "") == ok || got.Label != want {
			t.Errorf("Recommended(%q) = %q, %v; want %q", rec, got.Label, ok, want)
		}
	}
}

func TestValidate(t *testing.T) {
	if err := question("A").Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []Question{
		{Options: question("A").Options},
		{Issue: "x", Options: []Option{{Label: "A"}}},
		{Issue: "x", Options: []Option{{Label: "A"}, {Label: "a"}}},
		{Issue: "x", Options: []Option{{Label: "A"}, {Label: " "}}},
	}
	for _, q := range bad {
		if q.Validate() == nil {
			t.Errorf("accepted %+v", q)
		}
	}
}

func TestPendingAndUndelivered(t *testing.T) {
	r := &Record{Decisions: []Decision{{Answer: "A"}, {}}}
	if r.Pending() != &r.Decisions[1] || r.Undelivered() != &r.Decisions[0] {
		t.Fatal("wrong decision picked")
	}
}
