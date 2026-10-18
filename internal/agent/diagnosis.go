package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

// DiagnosisSchema constrains the diagnosis turn's reply.
//
//go:embed diagnosis.schema.json
var DiagnosisSchema []byte

// DiagnosisSource is one named piece of the context the implementer was given.
type DiagnosisSource struct {
	Name string
	Text string
}

// DiagnosisResult is one diagnosis turn's outcome.
type DiagnosisResult struct {
	Diagnosis task.Diagnosis
	Usage     task.Usage
}

// ParseDiagnosis is the only way diagnosis output enters the program. A reported
// conflict stands only when every quote appears verbatim in the source it names;
// otherwise the reply becomes a none, so the model cannot invent a conflict.
// Whitespace runs compare equal, since sources are wrapped Markdown.
func ParseDiagnosis(raw []byte, sources []DiagnosisSource) (task.Diagnosis, error) {
	var w struct {
		Kind      *task.DiagnosisKind    `json:"kind"`
		Summary   string                 `json:"summary"`
		Conflicts []task.ContextConflict `json:"conflicts"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return task.Diagnosis{}, fmt.Errorf("diagnosis is not valid JSON: %w", err)
	}
	if w.Kind == nil {
		return task.Diagnosis{}, errors.New("diagnosis needs a kind")
	}
	out := task.Diagnosis{Kind: *w.Kind, Summary: strings.TrimSpace(w.Summary)}
	switch out.Kind {
	case task.DiagnosedPracticeGap, task.DiagnosedNone:
		return out, nil
	case task.DiagnosedConflict:
	default:
		return task.Diagnosis{}, fmt.Errorf("diagnosis has unknown kind %q", out.Kind)
	}
	texts := map[string]string{}
	for _, s := range sources {
		texts[s.Name] = collapse(s.Text)
	}
	quoted := func(s task.Statement) bool {
		text, ok := texts[s.Source]
		q := collapse(s.Quote)
		return ok && q != "" && strings.Contains(text, q)
	}
	verified := len(w.Conflicts) > 0
	for _, c := range w.Conflicts {
		if !quoted(c.First) || !quoted(c.Second) || collapse(c.First.Quote) == collapse(c.Second.Quote) {
			verified = false
		}
	}
	if !verified {
		return task.Diagnosis{Kind: task.DiagnosedNone, Summary: "The reported conflict quoted text not found verbatim in its source, so it was set aside."}, nil
	}
	for _, c := range w.Conflicts {
		out.Conflicts = append(out.Conflicts, task.ContextConflict{Point: strings.TrimSpace(c.Point),
			First:  task.Statement{Source: c.First.Source, Quote: strings.TrimSpace(c.First.Quote)},
			Second: task.Statement{Source: c.Second.Source, Quote: strings.TrimSpace(c.Second.Quote)}})
	}
	return out, nil
}

func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// Diagnosers runs the diagnosis turn with either agent, with no tools, like Evaluators.
type Diagnosers struct {
	Claude Claude
	Codex  Codex
}

// Diagnose runs one tool-less turn on in.Prompt and checks its quotes against sources.
func (d Diagnosers) Diagnose(ctx context.Context, in EvaluateInput, sources []DiagnosisSource) (DiagnosisResult, error) {
	raw, usage, err := toollessTurn(ctx, d.Claude, d.Codex, in, DiagnosisSchema)
	if err != nil {
		return DiagnosisResult{Usage: usage}, err
	}
	diag, err := ParseDiagnosis(raw, sources)
	return DiagnosisResult{Diagnosis: diag, Usage: usage}, err
}

// DiagnosisPrompt asks why the implementer's own review keeps finding problems after
// the fix limit. It carries the open findings and the context the implementer was
// given, each source under the name a quote must cite.
func DiagnosisPrompt(findings []task.Finding, sources []DiagnosisSource) string {
	var s strings.Builder
	s.WriteString(`The implementing agent reviewed its own change in a fresh session and still found problems after the allowed fix rounds. Decide why, from the findings and the context the implementer was given below.

Answer with the JSON the schema asks for:

- kind: practice_gap when the findings show a generic practice the implementer missed, such as a missing test or unhandled error, that any task in this repository should follow. conflict when two specific statements in the sources below contradict each other on a point the findings depend on, so the implementer cannot satisfy both. none when neither applies or the evidence is not clear.
- summary: one or two sentences on why.
- conflicts: for kind conflict, each contradiction: the point in dispute, and the two statements as first and second. Each statement names its source exactly as written after "Source" below, and quotes a short passage copied verbatim from it. Foreman drops a conflict whose quotes are not in their sources. Use an empty list for the other kinds.

Report a conflict only between concrete statements, not between a statement and a finding.

## Open findings of the implementer's review

`)
	writeFindings(&s, findings)
	s.WriteString("\n## Context given to the implementer\n")
	for _, src := range sources {
		fmt.Fprintf(&s, "\n### Source %q\n\n%s\n", src.Name, strings.TrimSpace(src.Text))
	}
	return s.String()
}
