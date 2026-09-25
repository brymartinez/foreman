package task

import (
	"testing"
	"time"
)

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
	r := &Record{Decisions: []Decision{{Answer: "A", DeliveredAt: time.Now()}, {Answer: "B"}, {}, {}}}
	if r.Pending() != &r.Decisions[2] || r.PendingCount() != 2 {
		t.Fatalf("pending %p of %d", r.Pending(), r.PendingCount())
	}
	if u := r.Undelivered(); len(u) != 1 || u[0].Answer != "B" {
		t.Fatalf("undelivered = %+v", u)
	}
}

func TestChangeRequestsSinceDecision(t *testing.T) {
	t0 := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	r := &Record{Reviews: []Review{{At: t0}, {At: t0.Add(time.Hour)}, {At: t0.Add(90 * time.Minute), Clean: true}, {At: t0.Add(3 * time.Hour)}}}
	if r.ChangeRequestsSinceDecision() != 3 || r.LatestReview().At != t0.Add(3*time.Hour) {
		t.Fatal("without decisions every change request counts, and clean rounds don't")
	}
	r.Decisions = []Decision{{Answer: "A", AnsweredAt: t0.Add(2 * time.Hour)}}
	if got := r.ChangeRequestsSinceDecision(); got != 1 {
		t.Fatalf("rounds since answer = %d, want 1", got)
	}
}
