package agent

import (
	"cmp"
	"context"
	_ "embed"
	"fmt"
	"os"

	"github.com/brymartinez/foreman/internal/task"
)

//go:embed branch.schema.json
var BranchSchema []byte

type BranchInput struct {
	Agent   task.Agent
	Profile *task.Profile
	Prompt  string
}

// BranchProposers asks the selected implementer for one disposable, tool-free
// proposal. Repository instructions arrive in the prompt, not through CLI tools.
type BranchProposers struct {
	Claude Claude
	Codex  Codex
}

func (p BranchProposers) Propose(ctx context.Context, in BranchInput) (Outcome, error) {
	dir, err := os.MkdirTemp("", "foreman-branch-*")
	if err != nil {
		return Outcome{}, err
	}
	defer os.RemoveAll(dir)
	start := StartInput{Worktree: dir, Prompt: in.Prompt}
	var raw []byte
	switch in.Agent {
	case task.Claude:
		c := p.Claude
		c.Model = cmp.Or(c.PlanModel, c.Model)
		if in.Profile != nil {
			c.Model = cmp.Or(in.Profile.PlanModel, in.Profile.Model, c.Model)
		}
		raw, _, err = c.exec(ctx, start, []string{
			"-p", in.Prompt, "--no-session-persistence",
			"--output-format", "stream-json", "--verbose",
			"--json-schema", string(BranchSchema),
			"--permission-mode", "dontAsk", "--tools", "",
		})
	case task.Codex:
		c := p.Codex
		c.Model = cmp.Or(c.PlanModel, c.Model)
		c.ReasoningEffort = cmp.Or(c.PlanReasoningEffort, c.ReasoningEffort)
		if in.Profile != nil {
			c.Model = cmp.Or(in.Profile.PlanModel, in.Profile.Model, c.Model)
			c.ReasoningEffort = cmp.Or(in.Profile.PlanReasoningEffort, in.Profile.ReasoningEffort, c.ReasoningEffort)
		}
		_, raw, _, err = c.exec(ctx, start, BranchSchema, func(schema, last string) []string {
			return []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check", "-C", dir,
				"-s", "read-only", "--output-schema", schema, "-o", last}
		}, "", func(task.Session) {})
	default:
		return Outcome{}, fmt.Errorf("unknown branch proposer agent %q", in.Agent)
	}
	if err != nil {
		return Outcome{}, err
	}
	out, err := ParseOutcome(raw)
	if err != nil {
		return Outcome{}, err
	}
	if out.Status != BranchProposed {
		return Outcome{}, fmt.Errorf("branch proposer returned %s", out.Status)
	}
	return out, nil
}
