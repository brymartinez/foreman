package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

var diagnosisSources = []DiagnosisSource{
	{Name: "issue", Text: "Provider retries\n\nRetry failed\nrequests."},
	{Name: "pull request description", Text: "Retry 5xx only.\n\nCloses #825"},
}

const conflictJSON = `{"kind": "conflict", "summary": "the issue and the description disagree", "conflicts": [
	{"point": "which failures retry", "first": {"source": "issue", "quote": "Retry failed requests."}, "second": {"source": "pull request description", "quote": "Retry 5xx only."}}
]}`

func TestParseDiagnosisKeepsAConflictWhoseQuotesAppearInTheirSources(t *testing.T) {
	got, err := ParseDiagnosis([]byte(conflictJSON), diagnosisSources)
	if err != nil {
		t.Fatal(err)
	}
	want := task.ContextConflict{Point: "which failures retry",
		First:  task.Statement{Source: "issue", Quote: "Retry failed requests."},
		Second: task.Statement{Source: "pull request description", Quote: "Retry 5xx only."}}
	if got.Kind != task.DiagnosedConflict || len(got.Conflicts) != 1 || got.Conflicts[0] != want {
		t.Fatalf("diagnosis = %+v", got)
	}
}

func TestParseDiagnosisDowngradesAConflictWithAnUnverifiedQuote(t *testing.T) {
	for name, raw := range map[string]string{
		"invented quote":  strings.Replace(conflictJSON, "Retry 5xx only.", "Retry every request.", 1),
		"unknown source":  strings.Replace(conflictJSON, `"source": "issue"`, `"source": "the plan"`, 1),
		"empty quote":     strings.Replace(conflictJSON, `"quote": "Retry failed requests."`, `"quote": " "`, 1),
		"no conflicts":    `{"kind": "conflict", "summary": "s", "conflicts": []}`,
		"quoted the same": strings.Replace(conflictJSON, `"quote": "Retry 5xx only."`, `"quote": "Retry failed requests."`, 1),
	} {
		got, err := ParseDiagnosis([]byte(raw), diagnosisSources)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Kind != task.DiagnosedNone || got.Conflicts != nil || !strings.Contains(got.Summary, "not found verbatim") {
			t.Errorf("%s: diagnosis = %+v", name, got)
		}
	}
}

func TestParseDiagnosisReadsPracticeGapAndNone(t *testing.T) {
	for raw, want := range map[string]task.DiagnosisKind{
		`{"kind": "practice_gap", "summary": "missed a test", "conflicts": []}`: task.DiagnosedPracticeGap,
		`{"kind": "none", "summary": "nothing stands out", "conflicts": []}`:    task.DiagnosedNone,
	} {
		got, err := ParseDiagnosis([]byte(raw), diagnosisSources)
		if err != nil || got.Kind != want || got.Conflicts != nil || got.Summary == "" {
			t.Errorf("%s: %+v, %v", raw, got, err)
		}
	}
}

func TestParseDiagnosisRejectsAnUnknownKindOrBadJSON(t *testing.T) {
	for _, raw := range []string{`{"kind": "blame", "summary": "", "conflicts": []}`, `{"summary": ""}`, `not json`} {
		if _, err := ParseDiagnosis([]byte(raw), diagnosisSources); err == nil {
			t.Errorf("%s: no error", raw)
		}
	}
}

func TestDiagnosisPromptCarriesFindingsAndNamedSources(t *testing.T) {
	p := DiagnosisPrompt([]task.Finding{{Severity: "blocking", File: "retry.go", Summary: "4xx retries", Detail: "a 404 is retried"}}, diagnosisSources)
	for _, want := range []string{"[blocking] 4xx retries", "retry.go", "a 404 is retried", `### Source "issue"`, "Retry failed\nrequests.", `### Source "pull request description"`} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
}

func TestDiagnoseRunsWithoutToolsAndVerifiesTheQuotes(t *testing.T) {
	var got Command
	exec := func(_ context.Context, c Command) error {
		got = c
		var b bytes.Buffer
		if err := json.Compact(&b, []byte(conflictJSON)); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(c.Stdout, `{"type":"result","subtype":"success","total_cost_usd":0.03,"structured_output":%s}`, b.String())
		return nil
	}
	d := Diagnosers{Claude: Claude{Bin: "claude", Exec: exec}}
	out, err := d.Diagnose(context.Background(), EvaluateInput{Agent: task.Claude, Prompt: "why", MaxBudgetUSD: 0.5}, diagnosisSources)
	if err != nil || out.Diagnosis.Kind != task.DiagnosedConflict || out.Usage.ClaudeCostUSD != 0.03 {
		t.Fatalf("got %+v, %v", out, err)
	}
	if argValue(got.Args, "--tools") != "" || !slices.Contains(got.Args, "--tools") || argValue(got.Args, "--json-schema") != string(DiagnosisSchema) {
		t.Errorf("argv = %q", got.Args)
	}
	// Without the sources the same reply quotes nothing it can prove.
	if out, _ := d.Diagnose(context.Background(), EvaluateInput{Agent: task.Claude, Prompt: "why"}, nil); out.Diagnosis.Kind != task.DiagnosedNone {
		t.Errorf("unverified conflict kept: %+v", out.Diagnosis)
	}
}
