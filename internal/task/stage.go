package task

import (
	"fmt"
	"strings"
)

// Stage is a workflow step that can carry the developer's own instructions.
type Stage string

const (
	StagePlan      Stage = "plan"
	StageImplement Stage = "implement"
	StageReview    Stage = "review"
	StagePR        Stage = "pr"
)

// Stages lists every stage in workflow order.
var Stages = []Stage{StagePlan, StageImplement, StageReview, StagePR}

func ParseStage(s string) (Stage, error) {
	for _, st := range Stages {
		if Stage(strings.ToLower(strings.TrimSpace(s))) == st {
			return st, nil
		}
	}
	return "", fmt.Errorf("unknown stage %q; use one of %v", s, Stages)
}

// StageInstructions are the resolved instructions for one stage, with where each layer
// came from. A path is empty when that layer has no file. Foreman never interprets the
// text; it is passed to the agent as written.
type StageInstructions struct {
	Stage      Stage
	GlobalPath string
	Global     string
	RepoPath   string
	Repo       string
}

// Content is the global layer followed by the repo layer, so the repo's more specific
// instructions come last. It is empty when neither file exists or both are blank.
func (s StageInstructions) Content() string {
	var parts []string
	for _, p := range []string{s.Global, s.Repo} {
		if t := strings.TrimSpace(p); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}
