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

type Check = task.Check

// Outcome is how every implementation turn ends. For pr_opened, PRNumber may be 0
// when the agent could not report it; foreman then looks the PR up by branch.
type Outcome struct {
	Status     Status
	Summary    string
	PRNumber   int
	PRURL      string
	Validation []Check
	Questions  []task.Question
}

type wireOutcome struct {
	Status     Status          `json:"status"`
	Summary    string          `json:"summary"`
	PRNumber   *int            `json:"prNumber"`
	PRURL      *string         `json:"prUrl"`
	Validation []Check         `json:"validation"`
	Questions  []task.Question `json:"questions"`
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
		if len(w.Validation) == 0 {
			return Outcome{}, errors.New("pr_opened outcome needs the validation that was run")
		}
		if w.PRNumber != nil && *w.PRNumber > 0 {
			out.PRNumber = *w.PRNumber
		}
		if w.PRURL != nil {
			out.PRURL = *w.PRURL
		}
		out.Validation = w.Validation
	case Blocked:
		if len(w.Questions) == 0 {
			return Outcome{}, errors.New("blocked outcome has no questions")
		}
		for i, q := range w.Questions {
			if err := q.Validate(); err != nil {
				return Outcome{}, fmt.Errorf("blocked outcome, question %d: %w", i+1, err)
			}
		}
		out.Questions = w.Questions
	case FailedRun:
	default:
		return Outcome{}, fmt.Errorf("unknown outcome status %q", w.Status)
	}
	return out, nil
}
