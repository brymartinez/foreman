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
	// ReviewReady is a committed, validated change that is not pushed, with the PR
	// the implementer proposes for it. Only a task reviewed before its PR returns it.
	ReviewReady    Status = "review_ready"
	Blocked        Status = "blocked"
	FailedRun      Status = "failed"
	BranchProposed Status = "branch_proposed"
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
	Documents  map[task.PlanDoc]string
	BranchName string
	Uncertain  bool
	// PRTitle and PRBody are the pull request a review_ready turn proposes.
	PRTitle string
	PRBody  string
	// Claims tie a pr_opened or review_ready change to the PRD acceptance criteria.
	// The runner decides whether the task needs them.
	Claims []task.Claim
	// FullSuite is the full-suite report of a pr_opened or review_ready turn, or nil.
	FullSuite *FullSuite
}

type FullSuiteStatus string

const (
	// FullSuiteRun is a full run on Revision, with evidence for every path changed since.
	FullSuiteRun FullSuiteStatus = "run"
	// FullSuiteUnavailable is a repository with no identifiable full suite.
	FullSuiteUnavailable FullSuiteStatus = "unavailable"
)

// FullSuite is the implementer's report of the repository's full supported suite.
type FullSuite struct {
	Status      FullSuiteStatus    `json:"status"`
	Revision    string             `json:"revision"`
	DocOnly     []task.DocOnlyPath `json:"docOnly"`
	Explanation string             `json:"explanation"`
}

// CheckpointValidationError keeps the failed commands available for a repair turn.
type CheckpointValidationError struct {
	Failed []string
}

func (e *CheckpointValidationError) Error() string {
	return "checkpoint validation must pass: " + strings.Join(e.Failed, ", ")
}

type wireDocuments struct {
	PRD  string `json:"prd"`
	Plan string `json:"plan"`
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
	BranchName    *string         `json:"branchName"`
	Uncertain     *bool           `json:"uncertain"`
	PRTitle       *string         `json:"prTitle"`
	PRBody        *string         `json:"prBody"`
	Claims        []task.Claim    `json:"claims"`
	FullSuite     *wireFullSuite  `json:"fullSuite"`
}

type wireFullSuite struct {
	Status      FullSuiteStatus    `json:"status"`
	Revision    *string            `json:"revision"`
	DocOnly     []task.DocOnlyPath `json:"docOnly"`
	Explanation *string            `json:"explanation"`
}

func ParseOutcome(raw []byte) (Outcome, error) {
	var w wireOutcome
	if err := json.Unmarshal(raw, &w); err != nil {
		return Outcome{}, fmt.Errorf("agent outcome is not valid JSON: %w", err)
	}
	if w.Summary == "" {
		return Outcome{}, errors.New("agent outcome has no summary")
	}
	for i := range w.Validation {
		switch c := &w.Validation[i]; c.Scope {
		case "":
			c.Scope = task.FocusedCheck
		case task.FocusedCheck, task.FullCheck:
		default:
			return Outcome{}, fmt.Errorf("check %q has unknown scope %q (want focused or full)", c.Command, c.Scope)
		}
	}
	out := Outcome{Status: w.Status, Summary: w.Summary}
	var err error
	switch w.Status {
	case BranchProposed:
		if w.BranchName == nil || strings.TrimSpace(*w.BranchName) == "" || w.Uncertain == nil {
			return Outcome{}, errors.New("branch proposal needs a name and uncertainty")
		}
		out.BranchName, out.Uncertain = strings.TrimSpace(*w.BranchName), *w.Uncertain
	case Planned:
		if w.Documents == nil {
			return Outcome{}, errors.New("planned outcome has no documents")
		}
		out.Documents = map[task.PlanDoc]string{
			task.PRD: w.Documents.PRD, task.ImplementationPlan: w.Documents.Plan,
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
		if out.Claims, err = parseClaims(w.Claims); err != nil {
			return Outcome{}, err
		}
		if out.FullSuite, err = parseFullSuite(w.FullSuite); err != nil {
			return Outcome{}, err
		}
	case ReviewReady:
		if w.PRNumber != nil || w.PRURL != nil || len(w.Questions) > 0 || w.Documents != nil {
			return Outcome{}, errors.New("review_ready outcome includes unrelated fields")
		}
		if w.PRTitle == nil || strings.TrimSpace(*w.PRTitle) == "" || w.PRBody == nil || strings.TrimSpace(*w.PRBody) == "" {
			return Outcome{}, errors.New("review_ready outcome needs a proposed PR title and description")
		}
		if len(w.Validation) == 0 {
			return Outcome{}, errors.New("review_ready outcome needs the validation that was run")
		}
		for _, check := range w.Validation {
			if !check.Passed {
				return Outcome{}, fmt.Errorf("review_ready validation must pass: %s failed", check.Command)
			}
		}
		out.PRTitle, out.PRBody = strings.TrimSpace(*w.PRTitle), strings.TrimSpace(*w.PRBody)
		out.Validation = w.Validation
		if out.Claims, err = parseClaims(w.Claims); err != nil {
			return Outcome{}, err
		}
		if out.FullSuite, err = parseFullSuite(w.FullSuite); err != nil {
			return Outcome{}, err
		}
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

// parseClaims checks each claim's shape. Whether a turn must return claims depends on
// the task, so an empty set is left for the runner to judge.
func parseClaims(claims []task.Claim) ([]task.Claim, error) {
	for i, c := range claims {
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("claim %d: %w", i+1, err)
		}
	}
	return claims, nil
}

// parseFullSuite checks a full-suite report's shape. Whether each path's evidence is
// enough is the implementer's judgment, and whether the report covers the head is the
// runner's, so an empty evidence string is kept for the runner to report as uncovered.
func parseFullSuite(w *wireFullSuite) (*FullSuite, error) {
	if w == nil {
		return nil, nil
	}
	out := &FullSuite{Status: w.Status, DocOnly: w.DocOnly}
	switch w.Status {
	case FullSuiteRun:
		if w.Revision == nil || strings.TrimSpace(*w.Revision) == "" {
			return nil, errors.New("full suite report of a run needs the revision it ran on")
		}
		out.Revision = strings.TrimSpace(*w.Revision)
		for i, p := range w.DocOnly {
			if strings.TrimSpace(p.Path) == "" {
				return nil, fmt.Errorf("full suite report, documentation-only path %d has no path", i+1)
			}
		}
	case FullSuiteUnavailable:
		if w.Explanation == nil || strings.TrimSpace(*w.Explanation) == "" {
			return nil, errors.New("full suite report of an unavailable suite needs an explanation of what could not be verified")
		}
		out.Explanation, out.DocOnly = strings.TrimSpace(*w.Explanation), nil
	default:
		return nil, fmt.Errorf("full suite report has unknown status %q (want run or unavailable)", w.Status)
	}
	return out, nil
}
