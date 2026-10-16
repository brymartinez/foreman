package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/task"
)

// MergeSchema constrains the merge agent's reply: a grouping of the learnings, not
// merged records, so Go builds every learning's evidence and status itself.
//
//go:embed merge.schema.json
var MergeSchema []byte

// MergeResult is one merge turn's grouping.
type MergeResult struct {
	Groups []knowledge.Group
	Usage  task.Usage
}

// ParseMerge is the only way merge output enters the program. Every ref must appear
// in exactly one group, and each group's text must be fit for knowledge.md.
func ParseMerge(raw []byte, refs []string) ([]knowledge.Group, error) {
	var w struct {
		Groups *[]struct {
			Members  []string       `json:"members"`
			Kind     knowledge.Kind `json:"kind"`
			Text     string         `json:"text"`
			Conflict *string        `json:"conflict"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("merge is not valid JSON: %w", err)
	}
	if w.Groups == nil {
		return nil, errors.New("merge needs groups")
	}
	known := map[string]bool{}
	for _, r := range refs {
		known[r] = true
	}
	placed := map[string]bool{}
	var out []knowledge.Group
	for i, g := range *w.Groups {
		if len(g.Members) == 0 {
			return nil, fmt.Errorf("group %d has no members", i+1)
		}
		for _, ref := range g.Members {
			switch {
			case !known[ref]:
				return nil, fmt.Errorf("group %d names unknown learning %q", i+1, ref)
			case placed[ref]:
				return nil, fmt.Errorf("learning %q is in two groups", ref)
			}
			placed[ref] = true
		}
		if g.Kind != knowledge.Convention && g.Kind != knowledge.Fact && g.Kind != knowledge.Practice {
			return nil, fmt.Errorf("group %d has unknown kind %q", i+1, g.Kind)
		}
		text, err := knowledge.Sanitize(g.Text)
		if err != nil {
			return nil, fmt.Errorf("group %d text: %v", i+1, err)
		}
		group := knowledge.Group{Members: g.Members, Kind: g.Kind, Text: text}
		if g.Conflict != nil {
			group.Conflict = knowledge.Redact(*g.Conflict)
		}
		out = append(out, group)
	}
	for _, r := range refs {
		if !placed[r] {
			return nil, fmt.Errorf("the merge left out learning %q", r)
		}
	}
	return out, nil
}

// Mergers runs the merge turn with either agent, with no tools, like Evaluators.
type Mergers struct {
	Claude Claude
	Codex  Codex
}

// Merge groups refs, the refs of the learnings in in.Prompt.
func (m Mergers) Merge(ctx context.Context, in EvaluateInput, refs []string) (MergeResult, error) {
	raw, usage, err := toollessTurn(ctx, m.Claude, m.Codex, in, MergeSchema)
	if err != nil {
		return MergeResult{Usage: usage}, err
	}
	groups, err := ParseMerge(raw, refs)
	return MergeResult{Groups: groups, Usage: usage}, err
}

// MergePrompt asks for one grouping of both sides' learnings. It carries the
// learnings' text, status, and support counts, and no task text.
func MergePrompt(repo string, items []knowledge.MergeItem) string {
	var s strings.Builder
	fmt.Fprintf(&s, `You merge two machines' learnings about the repository %s into one set. Foreman learned each of them from earlier tasks. Refs that start with local: are this machine's; refs that start with import: come from the other machine.

Answer with the JSON the schema asks for: groups, where each group is one learning of the result.

- members: the refs the group combines. Put every ref below in exactly one group. Put learnings in one group when they give the same guidance, even in different words, and also when they give contradicting guidance on the same point, such as two different indentation widths. Keep guidance on different points in separate groups.
- kind: convention (how this repository wants code written), fact (something true about this repository), or practice (how to work on it).
- text: the group's guidance in one short generic sentence: no issue, PR, or commit numbers, no URLs, no file paths outside the repository, no names, no secrets. For a group of one, repeat its text.
- conflict: null when the members agree. When they contradict each other on the same point, one sentence on how. Never split contradicting learnings into separate groups to avoid a conflict. Foreman resolves a flagged conflict from maintainer corrections, not from your text.

Do not decide status. Foreman sets each learning's status from its evidence.

## Learnings
`, repo)
	for _, it := range items {
		l := it.Learning
		maintainer, observed := 0, 0
		for _, e := range l.Evidence {
			if e.Source == knowledge.Maintainer {
				maintainer++
			} else {
				observed++
			}
		}
		fmt.Fprintf(&s, "\n- %s (%s, %s, %d tasks, %d maintainer and %d observed evidence): %s\n", it.Ref, l.Kind, l.Status, l.Tasks(), maintainer, observed, l.Text)
		for _, c := range it.Corrections {
			fmt.Fprintf(&s, "  - maintainer correction: %s\n", c)
		}
	}
	return s.String()
}
