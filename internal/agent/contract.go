// Package agent defines the contract between foreman and a headless agent turn.
package agent

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/brymartinez/foreman/internal/task"
)

// OutcomeSchema is passed to `claude --json-schema` and `codex exec --output-schema`.
// Codex enforces strict mode (every property required, no extra keys), so the wire
// shape is one flat object with nullable fields, narrowed by ParseOutcome.
//
//go:embed outcome.schema.json
var OutcomeSchema []byte

type Status string

const (
	PROpened  Status = "pr_opened"
	Blocked   Status = "blocked"
	FailedRun Status = "failed"
)

type Check struct {
	Command string `json:"command"`
	Passed  bool   `json:"passed"`
}

// Outcome is how every implementation turn ends. Exactly the fields for its Status are set.
type Outcome struct {
	Status     Status
	Summary    string
	PRNumber   int
	PRURL      string
	Validation []Check
	Question   *task.Question
}

type wireOutcome struct {
	Status     Status         `json:"status"`
	Summary    string         `json:"summary"`
	PRNumber   *int           `json:"prNumber"`
	PRURL      *string        `json:"prUrl"`
	Validation []Check        `json:"validation"`
	Question   *task.Question `json:"question"`
}

func ParseOutcome(raw []byte) (Outcome, error) {
	var w wireOutcome
	if err := json.Unmarshal(raw, &w); err != nil {
		return Outcome{}, fmt.Errorf("agent outcome is not valid JSON: %w", err)
	}
	if w.Summary == "" {
		return Outcome{}, errors.New("agent outcome has no summary")
	}
	out := Outcome{Status: w.Status, Summary: w.Summary}
	switch w.Status {
	case PROpened:
		if w.PRNumber == nil || *w.PRNumber <= 0 || w.PRURL == nil || len(w.Validation) == 0 {
			return Outcome{}, errors.New("pr_opened outcome needs prNumber, prUrl, and validation")
		}
		out.PRNumber, out.PRURL, out.Validation = *w.PRNumber, *w.PRURL, w.Validation
	case Blocked:
		if w.Question == nil {
			return Outcome{}, errors.New("blocked outcome has no question")
		}
		if err := w.Question.Validate(); err != nil {
			return Outcome{}, fmt.Errorf("blocked outcome: %w", err)
		}
		out.Question = w.Question
	case FailedRun:
	default:
		return Outcome{}, fmt.Errorf("unknown outcome status %q", w.Status)
	}
	return out, nil
}
