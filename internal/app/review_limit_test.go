package app

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// fakeDiagnoser answers with raw and checks it through the real parser, so the quote
// check runs against the sources the runner gathered.
type fakeDiagnoser struct {
	raw     string
	err     error
	inputs  []agent.EvaluateInput
	sources [][]agent.DiagnosisSource
}

func (d *fakeDiagnoser) Diagnose(_ context.Context, in agent.EvaluateInput, sources []agent.DiagnosisSource) (agent.DiagnosisResult, error) {
	d.inputs = append(d.inputs, in)
	d.sources = append(d.sources, sources)
	if d.err != nil {
		return agent.DiagnosisResult{Usage: task.Usage{ClaudeCostUSD: 0.01}}, d.err
	}
	diag, err := agent.ParseDiagnosis([]byte(d.raw), sources)
	return agent.DiagnosisResult{Diagnosis: diag, Usage: task.Usage{ClaudeCostUSD: 0.02}}, err
}

const (
	diagnosedNone = `{"kind": "none", "summary": "nothing stands out", "conflicts": []}`
	diagnosedGap  = `{"kind": "practice_gap", "summary": "the implementer skips error-path tests", "conflicts": []}`
	// diagnosedConflict quotes the fixture's issue body and AGENTS.md.
	diagnosedConflict = `{"kind": "conflict", "summary": "the issue and the instructions disagree", "conflicts": [
		{"point": "which failures retry", "first": {"source": "issue", "quote": "Retry failed requests."}, "second": {"source": "AGENTS.md", "quote": "Never retry a 4xx response."}}
	]}`
)

func TestSelfReviewLimitIsAssessedByTheImplementer(t *testing.T) {
	for _, reject := range []bool{false, true} {
		f := newFixture()
		f.app.FixRounds = 1
		key := started(t, f, startReq(t))
		postedPlan(f, f.store.recs[key])
		diag := &fakeDiagnoser{raw: diagnosedGap}
		f.app.Diagnoser = diag
		last := prTurn("sid")
		if reject {
			last.Outcome.ReviewAssessment = &task.ReviewAssessment{Round: 2, Rejected: []task.RejectedFinding{{Finding: 1, Reason: "The approved requirement excludes that case."}}}
		} else {
			last.Outcome = agent.Outcome{Status: agent.Blocked, Summary: "The remaining finding needs your decision", Questions: []task.Question{{Issue: "Should this case be supported?", Context: "The remaining finding changes the approved scope.", Options: []task.Option{{Label: "A", Description: "Keep the scope"}, {Label: "B", Description: "Extend it"}}, Recommendation: "A", ImpactIfWrong: "Extra behavior"}}}
		}
		f.claude.turns = []agent.Turn{prTurn("sid"), selfFix(prTurn("sid"), 1), last}
		f.claude.reviews = []agent.ReviewOutcome{selfFindings(), selfFindings()}
		f.claude.onResume = func() {
			if len(f.claude.resumed) == 1 {
				setHead(f, "c0ffee2")
			}
		}
		if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
			t.Fatal(err)
		}
		got := f.store.recs[key]
		if len(diag.inputs) != 0 || len(f.claude.resumed) != 2 || !strings.Contains(f.claude.resumed[1], "self-review fix limit is reached") {
			t.Fatalf("limit did not return to retained implementer: diagnosis %d resumes %v", len(diag.inputs), f.claude.resumed)
		}
		if reject {
			if got.State != task.WaitingForCI || kinds(got) != "self! self! competing" {
				t.Fatalf("state %s reviews %s", got.State, kinds(got))
			}
		} else {
			if got.State != task.NeedsDecision || got.Pending() == nil || got.Pending().Question.Issue != "Should this case be supported?" {
				t.Fatalf("implementer question missing: %+v", got.LastOutcome)
			}
		}
	}
}

func TestSelfReviewLimitRejectsAnotherCorrectionWithoutDecision(t *testing.T) {
	f := newFixture()
	f.app.FixRounds = 1
	key := started(t, f, startReq(t))
	postedPlan(f, f.store.recs[key])
	f.claude.turns = []agent.Turn{prTurn("sid"), selfFix(prTurn("sid"), 1), selfFix(prTurn("sid"), 2)}
	f.claude.reviews = []agent.ReviewOutcome{selfFindings(), selfFindings()}
	f.claude.onResume = func() {
		if len(f.claude.resumed) == 1 {
			pushHead(f, "c0ffee2")
		} else {
			pushHead(f, "c0ffee3")
		}
	}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "self-review fix limit reached") {
		t.Fatalf("state %s outcome %+v", got.State, got.LastOutcome)
	}
}
