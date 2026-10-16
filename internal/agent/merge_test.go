package agent

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/task"
)

const mergeJSON = `{"groups":[{"members":["local:L1","import:L1"],"kind":"convention","text":"Wrap errors with the operation name.","conflict":null},{"members":["import:L2"],"kind":"practice","text":"Run the linter before pushing.","conflict":"the import asks for the opposite"}]}`

var mergeRefs = []string{"local:L1", "import:L1", "import:L2"}

func TestParseMergeReadsGroups(t *testing.T) {
	got, err := ParseMerge([]byte(mergeJSON), mergeRefs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !slices.Equal(got[0].Members, []string{"local:L1", "import:L1"}) || got[0].Kind != knowledge.Convention ||
		got[0].Conflict != "" || got[1].Conflict != "the import asks for the opposite" {
		t.Fatalf("groups = %+v", got)
	}
}

func TestParseMergeRejectsAnInvalidGrouping(t *testing.T) {
	group := func(members, kind, text string) string {
		return fmt.Sprintf(`{"members":%s,"kind":%q,"text":%q,"conflict":null}`, members, kind, text)
	}
	whole := func(groups ...string) string { return `{"groups":[` + strings.Join(groups, ",") + `]}` }
	all := group(`["local:L1","import:L1","import:L2"]`, "convention", "Wrap errors.")
	for name, raw := range map[string]string{
		"not json":       `groups`,
		"missing groups": `{}`,
		"left out ref":   whole(group(`["local:L1","import:L1"]`, "convention", "Wrap errors.")),
		"ref twice":      whole(all, group(`["import:L2"]`, "practice", "Lint.")),
		"unknown ref":    whole(group(`["local:L1","import:L1","import:L2","import:L9"]`, "convention", "Wrap errors.")),
		"empty group":    whole(all, group(`[]`, "practice", "Lint.")),
		"unknown kind":   whole(group(`["local:L1","import:L1","import:L2"]`, "opinion", "Wrap errors.")),
		"unsafe text":    whole(group(`["local:L1","import:L1","import:L2"]`, "convention", "Wrap errors as in #12.")),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseMerge([]byte(raw), mergeRefs); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestMergerClaudeRunsWithoutToolsUnderTheMergeSchema(t *testing.T) {
	var got Command
	exec := func(_ context.Context, c Command) error {
		got = c
		fmt.Fprintf(c.Stdout, `{"type":"result","subtype":"success","total_cost_usd":0.04,"structured_output":%s}`, mergeJSON)
		return nil
	}
	m := Mergers{Claude: Claude{Bin: "claude", Exec: exec}}
	out, err := m.Merge(context.Background(), EvaluateInput{Agent: task.Claude, Prompt: "merge", MaxBudgetUSD: 0.5}, mergeRefs)
	if err != nil || len(out.Groups) != 2 || out.Usage.ClaudeCostUSD != 0.04 {
		t.Fatalf("got %+v, %v", out, err)
	}
	if argValue(got.Args, "--json-schema") != string(MergeSchema) || argValue(got.Args, "--tools") != "" || argValue(got.Args, "--max-budget-usd") != "0.50" {
		t.Errorf("argv = %q", got.Args)
	}
}

func TestMergerCodexDiscardsATurnThatUsedTools(t *testing.T) {
	exec := func(_ context.Context, c Command) error {
		fmt.Fprintln(c.Stdout, `{"type":"thread.started","thread_id":"th-1"}`)
		fmt.Fprintln(c.Stdout, `{"type":"item.completed","item":{"id":"1","type":"command_execution","command":"ls"}}`)
		return os.WriteFile(argValue(c.Args, "-o"), []byte(mergeJSON), 0o600)
	}
	m := Mergers{Codex: Codex{Bin: "codex", Exec: exec}}
	if _, err := m.Merge(context.Background(), EvaluateInput{Agent: task.Codex, Prompt: "merge"}, mergeRefs); err == nil || !strings.Contains(err.Error(), "tool") {
		t.Fatalf("err = %v, want a tool use error", err)
	}
}

func TestMergePromptListsEveryRefWithItsSupportAndCorrections(t *testing.T) {
	items := []knowledge.MergeItem{
		{Ref: "local:L1", Learning: knowledge.Learning{Kind: knowledge.Convention, Status: knowledge.Active, Text: "Wrap errors.", Evidence: []knowledge.Evidence{{Task: "1", Source: knowledge.Maintainer}}}, Corrections: []string{"the maintainer asked for it"}},
		{Ref: "import:L1", Learning: knowledge.Learning{Kind: knowledge.Practice, Status: knowledge.Candidate, Text: "Lint first.", Evidence: []knowledge.Evidence{{Task: "2", Source: knowledge.Observed}}}},
	}
	p := MergePrompt("acme/api", items)
	for _, want := range []string{"acme/api", "local:L1 (convention, active, 1 tasks, 1 maintainer and 0 observed evidence): Wrap errors.", "maintainer correction: the maintainer asked for it", "import:L1 (practice, candidate", "exactly one group", "Do not decide status", "contradicting guidance on the same point", "Never split contradicting learnings"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
}
