package agent

import (
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func knowledgeBrief() TaskBrief {
	return TaskBrief{
		Key: task.Key{Repo: "a/b", Number: 7}, IssueBody: "Body", Branch: "feat/7", BaseBranch: "main", Worktree: "/wt",
		Knowledge: "- Run make lint before pushing.\n",
	}
}

func TestPlanningAndImplementationPromptsOmitRepositoryKnowledge(t *testing.T) {
	cases := map[string]func(*TaskBrief){
		"implementation": func(*TaskBrief) {},
		"planning":       func(b *TaskBrief) { b.Planning = true },
		"checkpoint":     func(b *TaskBrief) { b.CheckpointHead, b.NextObjective = "abc123", "Add the pager" },
		"fresh feedback": func(b *TaskBrief) { b.PR = 12 },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			b := knowledgeBrief()
			edit(&b)
			p := ImplementationPrompt(b)
			if strings.Contains(p, "## Repository knowledge") || strings.Contains(p, "Run make lint before pushing.") {
				t.Fatalf("prompt leaked triage research context:\n%s", p)
			}
		})
	}
}

func TestPromptsOmitRepositoryKnowledgeWhenEmptyOrRepairingConflicts(t *testing.T) {
	cases := map[string]func(*TaskBrief){
		"empty":    func(b *TaskBrief) { b.Knowledge = "  \n" },
		"conflict": func(b *TaskBrief) { b.PR, b.Conflict = 12, &Conflict{PR: 12, Base: "main"} },
		"merge":    func(b *TaskBrief) { b.PR, b.Merge = 12, &MergeBrief{Base: "main", Files: []string{"x.go"}} },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			b := knowledgeBrief()
			edit(&b)
			if p := ImplementationPrompt(b); strings.Contains(p, "## Repository knowledge") {
				t.Fatalf("prompt has a knowledge section:\n%s", p)
			}
		})
	}
}
