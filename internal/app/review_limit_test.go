package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
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

// atSelfLimit runs a PR task whose own review still has findings after one fix.
func atSelfLimit(t *testing.T, raw string, diagErr error) (fixture, task.Key, *fakeDiagnoser) {
	t.Helper()
	f := newFixture()
	f.app.FixRounds = 1
	f.app.ReviewerBudgetUSD = 0.75
	diag := &fakeDiagnoser{raw: raw, err: diagErr}
	f.app.Diagnoser = diag
	key := started(t, f, startReq(t))
	postedPlan(f, f.store.recs[key])
	wt := t.TempDir()
	os.WriteFile(filepath.Join(wt, "AGENTS.md"), []byte("# Rules\n\n- Never retry a 4xx\n  response.\n"), 0o644)
	f.store.recs[key].Delegation.Worktree = wt
	f.claude.turns = []agent.Turn{prTurn("sid-1"), prTurn("sid-1")}
	f.claude.reviews = []agent.ReviewOutcome{selfFindings(), selfFindings()}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.NeedsDecision || kinds(got) != "self! self!" || len(f.codex.reviewed) != 0 {
		t.Fatalf("state %s reviews %s competing reviews %d", got.State, kinds(got), len(f.codex.reviewed))
	}
	return f, key, diag
}

func optionDescriptions(q task.Question) string {
	var out []string
	for _, o := range q.Options {
		out = append(out, o.Label+". "+o.Description)
	}
	return strings.Join(out, " | ")
}

func TestSelfReviewAtTheFixLimitOffersAnotherRoundOrStopButNeverAccept(t *testing.T) {
	f, key, diag := atSelfLimit(t, diagnosedNone, nil)
	got := f.store.recs[key]
	d := got.Pending()
	q := d.Question
	if len(q.Options) != 2 || strings.Contains(strings.ToLower(optionDescriptions(q)), "accept") || q.Options[1].Description != stopAtLimitOption.Description {
		t.Fatalf("options = %s", optionDescriptions(q))
	}
	if q.Recommendation != "A" || !strings.Contains(q.Context, "no test covers the 4xx path") || !strings.Contains(q.Context, "No concrete conflict") {
		t.Errorf("question = %+v", q)
	}
	if d.Diagnosis == nil || d.Diagnosis.Kind != task.DiagnosedNone {
		t.Errorf("diagnosis = %+v", d.Diagnosis)
	}
	if len(diag.inputs) != 1 || diag.inputs[0].Agent != task.Claude || diag.inputs[0].MaxBudgetUSD != 0.75 {
		t.Fatalf("diagnosis inputs = %+v", diag.inputs)
	}
	p := diag.inputs[0].Prompt
	for _, want := range []string{"no test covers the 4xx path", "Retry failed requests.", "Retry 5xx only.", "Preserve the existing 5xx-only retry semantics.", "Never retry a 4xx"} {
		if !strings.Contains(p, want) {
			t.Errorf("diagnosis prompt lacks %q:\n%s", want, p)
		}
	}
	if got.Usage.ClaudeCostUSD < 0.02 {
		t.Errorf("diagnosis cost not counted: %+v", got.Usage)
	}
}

func TestSelfReviewLimitShowsAConflictWhoseQuotesAreInTheirSources(t *testing.T) {
	f, key, _ := atSelfLimit(t, diagnosedConflict, nil)
	d := f.store.recs[key].Pending()
	q := d.Question
	if len(q.Options) != 4 || q.Options[3].Description != stopAtLimitOption.Description || q.Recommendation != "A" {
		t.Fatalf("options = %s recommendation %s", optionDescriptions(q), q.Recommendation)
	}
	for _, want := range []string{`issue: "Retry failed requests."`, `AGENTS.md: "Never retry a 4xx response."`, "which failures retry"} {
		if !strings.Contains(q.Context, want) {
			t.Errorf("context lacks %q:\n%s", want, q.Context)
		}
	}
	if d.Diagnosis == nil || d.Diagnosis.Kind != task.DiagnosedConflict || len(d.Diagnosis.Conflicts) != 1 {
		t.Errorf("diagnosis = %+v", d.Diagnosis)
	}
}

func TestSelfReviewLimitDropsAConflictWithAnInventedQuote(t *testing.T) {
	f, key, _ := atSelfLimit(t, strings.Replace(diagnosedConflict, "Never retry a 4xx response.", "Retry nothing.", 1), nil)
	d := f.store.recs[key].Pending()
	if len(d.Question.Options) != 2 || d.Diagnosis.Kind != task.DiagnosedNone || strings.Contains(d.Question.Context, "Retry nothing.") {
		t.Fatalf("decision = %+v diagnosis %+v", d.Question, d.Diagnosis)
	}
}

func TestSelfReviewLimitReportsAPracticeGap(t *testing.T) {
	f, key, _ := atSelfLimit(t, diagnosedGap, nil)
	d := f.store.recs[key].Pending()
	if len(d.Question.Options) != 2 || d.Diagnosis.Kind != task.DiagnosedPracticeGap || !strings.Contains(d.Question.Context, "generic practice") {
		t.Fatalf("decision = %+v diagnosis %+v", d.Question, d.Diagnosis)
	}
}

func TestSelfReviewLimitStillAsksWhenTheDiagnosisFails(t *testing.T) {
	f, key, _ := atSelfLimit(t, "", errors.New("claude exited 1"))
	got := f.store.recs[key]
	d := got.Pending()
	if len(d.Question.Options) != 2 || d.Diagnosis.Kind != task.DiagnosedNone || !strings.Contains(d.Question.Context, "Diagnosis unavailable: claude exited 1") {
		t.Fatalf("decision = %+v diagnosis %+v", d.Question, d.Diagnosis)
	}
}

func TestAnsweredContextDecisionReachesTheImplementerAndAFreshSelfReviewFollows(t *testing.T) {
	f, key, diag := atSelfLimit(t, diagnosedConflict, nil)
	first := f.store.recs[key].Pending().Question.Options[0]
	answer(f, key, optionText(first))
	f.claude.turns = []agent.Turn{prTurn("sid-1")}
	f.claude.reviews = []agent.ReviewOutcome{{Clean: true}}
	if err := f.run(context.Background(), key, 4243, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if kinds(got) != "self! self! self competing" || got.State != task.WaitingForCI {
		t.Fatalf("state %s reviews %s", got.State, kinds(got))
	}
	last := f.claude.prompts[len(f.claude.prompts)-1]
	if !strings.Contains(last, "Answer: A.") || !strings.Contains(last, "## Findings to correct") {
		t.Errorf("answer correction turn = %q", last)
	}
	if len(diag.inputs) != 1 {
		t.Errorf("the answered decision was diagnosed again: %d calls", len(diag.inputs))
	}
}

func TestStopAtTheSelfReviewLimitFailsAndLeavesThePR(t *testing.T) {
	f, key, _ := atSelfLimit(t, diagnosedNone, nil)
	answer(f, key, optionText(f.store.recs[key].Pending().Question.Options[1]))
	turns := len(f.claude.resumed) + f.claude.starts
	pr := f.gh.prs[912]
	if err := f.run(context.Background(), key, 4243, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "no test covers the 4xx path") || got.Delegation.PR != 912 {
		t.Fatalf("state %s outcome %+v", got.State, got.LastOutcome)
	}
	if f.gh.prs[912] != pr || len(f.claude.resumed)+f.claude.starts != turns || len(got.Undelivered()) != 0 {
		t.Fatalf("the stop changed the PR, ran a turn, or left the answer undelivered")
	}
}

func TestLocalSelfReviewLimitOffersNoAcceptAndStopsWithoutAPR(t *testing.T) {
	f := newFixture()
	f.app.FixRounds = 1
	f.app.Diagnoser = &fakeDiagnoser{raw: diagnosedNone}
	key := localTask(t, f)
	f.wt.checkpointHeads = []string{"commit1", "commit2"}
	f.claude.turns = []agent.Turn{reviewReadyTurn("sid-1", proposedBody), reviewReadyTurn("sid-1", proposedBody)}
	f.claude.reviews = []agent.ReviewOutcome{selfFindings(), selfFindings()}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	q := got.Pending().Question
	if got.State != task.NeedsDecision || len(q.Options) != 2 || strings.Contains(strings.ToLower(optionDescriptions(q)), "accept") || strings.Contains(q.Context, "PR #") {
		t.Fatalf("state %s question %+v", got.State, q)
	}
	answer(f, key, optionText(q.Options[1]))
	if err := f.run(context.Background(), key, 4243, io.Discard); err != nil {
		t.Fatal(err)
	}
	got = f.store.recs[key]
	if got.State != task.Failed || !strings.Contains(got.LastOutcome.Summary, "no PR opened") || !strings.Contains(got.LastOutcome.Summary, "no test covers the 4xx path") || got.Delegation.PR != 0 {
		t.Fatalf("state %s outcome %+v", got.State, got.LastOutcome)
	}
}

func TestLimitDiagnosisQuotesAnsweredDecisionsAndStageInstructions(t *testing.T) {
	f := newFixture()
	f.app.FixRounds = 1
	diag := &fakeDiagnoser{raw: `{"kind": "conflict", "summary": "the decision and the description disagree", "conflicts": [
		{"point": "whether 429 retries", "first": {"source": "decision 1", "quote": "Retry 429 responses too"}, "second": {"source": "pull request description", "quote": "Retry 5xx only."}}
	]}`}
	f.app.Diagnoser = diag
	key := started(t, f, startReq(t))
	f.store.recs[key].Decisions = []task.Decision{
		{Question: task.Question{Issue: "Should 429 retry?"}, Answer: "A. Retry 429 responses too", AnsweredAt: t0, DeliveredAt: t0},
	}
	f.claude.turns = []agent.Turn{prTurn("sid-1"), prTurn("sid-1")}
	f.claude.reviews = []agent.ReviewOutcome{selfFindings(), selfFindings()}
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	d := f.store.recs[key].Pending()
	if d == nil || d.Diagnosis == nil || d.Diagnosis.Kind != task.DiagnosedConflict || !strings.Contains(d.Question.Context, `decision 1: "Retry 429 responses too"`) {
		t.Fatalf("decision = %+v", d)
	}
	p := diag.inputs[0].Prompt
	for _, want := range []string{`### Source "decision 1"`, "Should 429 retry?", `### Source "implement stage instructions"`, "implement instructions"} {
		if !strings.Contains(p, want) {
			t.Errorf("diagnosis prompt lacks %q:\n%s", want, p)
		}
	}
}
