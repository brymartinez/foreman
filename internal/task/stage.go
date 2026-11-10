package task

import (
	"fmt"
	"strings"
)

// Stage is a workflow step that can carry the developer's own instructions.
type Stage string

const (
	StagePlan       Stage = "plan"
	StageImplement  Stage = "implement"
	StageSelfReview Stage = "self-review"
	StageReview     Stage = "review"
	StagePR         Stage = "pr"
)

// Stages lists every stage in workflow order.
var Stages = []Stage{StagePlan, StageImplement, StageSelfReview, StageReview, StagePR}

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
	Stage          Stage
	GlobalPath     string
	Global         string
	RepoPath       string
	Repo           string
	PRTemplatePath string
	PRTemplate     string
}

// Content is the global layer followed by the repo layer, so the repo's more specific
// instructions come last. A repository PR template follows as the authoritative format.
func (s StageInstructions) Content() string {
	var parts []string
	for _, p := range []string{s.Global, s.Repo} {
		if t := strings.TrimSpace(p); t != "" {
			parts = append(parts, t)
		}
	}
	if template := strings.TrimSpace(s.PRTemplate); template != "" {
		parts = append(parts, "## Repository pull request template\n\nThis template and the repository rules override conflicting PR stage guidance above, including instructions to omit test results. The default body headings apply only when the repository has no template. Other PR stage guidance is fallback only where the repository leaves a choice. Preserve its section headings and fill its requested evidence, including verification commands and outcomes. Report missing verification honestly. Before returning prTitle and prBody, compare the proposal with this template and the repository's PR rules and correct any omissions or conflicting fallback formatting.\n\n"+template)
	}
	return strings.Join(parts, "\n\n")
}
