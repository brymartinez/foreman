// Package agent defines the contract between foreman and a headless agent turn.
package agent

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
	Planned    Status = "planned"
	Checkpoint Status = "checkpoint"
	PROpened   Status = "pr_opened"
	Blocked    Status = "blocked"
	FailedRun  Status = "failed"
)

type Check = task.Check

// Outcome is how every implementation turn ends. For pr_opened, PRNumber may be 0
// when the agent could not report it; foreman then looks the PR up by branch.
type Outcome struct {
	Status        Status
	Summary       string
	NextObjective string
	PRNumber      int
	PRURL         string
	Validation    []Check
	Questions     []task.Question
	// Documents are the planning documents a planned turn wrote, by task.PlanDocs.
	Documents map[task.PlanDoc]string
}

// CheckpointValidationError keeps the failed commands available for a repair turn.
type CheckpointValidationError struct {
	Failed []string
}

func (e *CheckpointValidationError) Error() string {
	return "checkpoint validation must pass: " + strings.Join(e.Failed, ", ")
}

type wireDocuments struct {
	Analysis string `json:"analysis"`
	PRD      string `json:"prd"`
	Design   string `json:"design"`
	Plan     string `json:"plan"`
}

type wireOutcome struct {
	Status        Status          `json:"status"`
	Summary       string          `json:"summary"`
	NextObjective *string         `json:"nextObjective"`
	PRNumber      *int            `json:"prNumber"`
	PRURL         *string         `json:"prUrl"`
	Validation    []Check         `json:"validation"`
	Questions     []task.Question `json:"questions"`
	Documents     *wireDocuments  `json:"documents"`
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
	case Planned:
		if w.Documents == nil {
			return Outcome{}, errors.New("planned outcome has no documents")
		}
		out.Documents = map[task.PlanDoc]string{
			task.Analysis: w.Documents.Analysis, task.PRD: w.Documents.PRD,
			task.Design: w.Documents.Design, task.ImplementationPlan: w.Documents.Plan,
		}
		for _, d := range task.PlanDocs {
			if strings.TrimSpace(out.Documents[d]) == "" {
				return Outcome{}, fmt.Errorf("planned outcome has an empty %s", d.Title())
			}
		}
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
	case Checkpoint:
		if w.PRNumber != nil || w.PRURL != nil || len(w.Questions) > 0 || w.Documents != nil {
			return Outcome{}, errors.New("checkpoint outcome includes unrelated fields")
		}
		if w.NextObjective == nil || strings.TrimSpace(*w.NextObjective) == "" {
			return Outcome{}, errors.New("checkpoint outcome needs a next objective")
		}
		if len([]rune(*w.NextObjective)) > 500 {
			return Outcome{}, errors.New("checkpoint next objective exceeds 500 characters")
		}
		if len(w.Validation) == 0 {
			return Outcome{}, errors.New("checkpoint outcome needs the validation that was run")
		}
		out.NextObjective = strings.TrimSpace(*w.NextObjective)
		out.Validation = w.Validation
		var failed []string
		for _, check := range w.Validation {
			if !check.Passed {
				failed = append(failed, check.Command)
			}
		}
		if len(failed) > 0 {
			return out, &CheckpointValidationError{Failed: failed}
		}
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
