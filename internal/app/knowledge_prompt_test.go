package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/knowledge"
)

const fixtureRepo = "testorg/example"

// learn stores texts as active learnings of repo, backed by a maintainer.
func learn(t *testing.T, ks knowledge.Store, repo, taskID string, texts ...string) {
	t.Helper()
	ev := knowledge.Evaluation{Task: taskID, TaskAt: t0, At: t0, Fingerprint: "fp", Assessment: "ok", MaintainerRefs: []string{"D1"}}
	for _, text := range texts {
		ev.Proposals = append(ev.Proposals, knowledge.Proposal{Kind: knowledge.Convention, Text: text, Scope: knowledge.Reusable, Source: knowledge.Maintainer, Cites: []string{"D1"}})
	}
	if err := ks.Apply(repo, ev); err != nil {
		t.Fatal(err)
	}
}

func knowledgeSection(prompt string) string {
	_, after, ok := strings.Cut(prompt, "## Repository knowledge\n\n")
	if !ok {
		return ""
	}
	_, after, _ = strings.Cut(after, "\n\n")
	section, _, _ := strings.Cut(after, "\n\n## ")
	return section
}

func TestPlanningAndImplementationTurnsOmitRepositoryKnowledge(t *testing.T) {
	f := newFixture()
	ks := knowledge.Store{Dir: t.TempDir()}
	f.app.Knowledge = ks
	learn(t, ks, fixtureRepo, "gh-1", "Run make lint before pushing.")
	learn(t, ks, "other/repo", "gh-2", "Never touch the legacy billing module.")
	key := started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2")}
	var log strings.Builder
	if err := f.run(context.Background(), key, 4242, &log); err != nil {
		t.Fatal(err)
	}
	if len(f.claude.prompts) != 2 {
		t.Fatalf("prompts = %d", len(f.claude.prompts))
	}
	for i, p := range f.claude.prompts {
		if strings.Contains(p, "make lint") || strings.Contains(p, "legacy billing") {
			t.Errorf("turn %d carries triage research context", i)
		}
	}
	for _, p := range f.codex.reviewed {
		if strings.Contains(p, "Repository knowledge") || strings.Contains(p, "make lint") {
			t.Errorf("review prompt carries knowledge:\n%s", p)
		}
	}
	if len(f.codex.reviewed) == 0 {
		t.Error("no review ran")
	}
}

func TestRepositoryKnowledgeStaysOutOfPlanning(t *testing.T) {
	f := newFixture()
	ks := knowledge.Store{Dir: t.TempDir()}
	f.app.Knowledge = ks
	var texts []string
	for _, w := range []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet"} {
		texts = append(texts, "Keep the "+w+" module "+strings.Repeat("stable and documented "+w+" ", 8)+"always.")
	}
	learn(t, ks, fixtureRepo, "gh-1", texts...)
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	if err := f.run(context.Background(), key, 4242, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.claude.prompts[0], "Repository knowledge") || strings.Contains(f.claude.prompts[0], "Keep the alpha") {
		t.Errorf("planner received private repository knowledge:\n%s", f.claude.prompts[0])
	}
}

func TestRunsWithoutTheKnowledgeSectionWhenTheStoreIsEmptyOrUnreadable(t *testing.T) {
	t.Run("empty store", func(t *testing.T) {
		f := newFixture()
		f.app.Knowledge = knowledge.Store{Dir: t.TempDir()}
		key := started(t, f, startReq(t))
		f.claude.turn = prTurn("sid-1")
		if err := f.run(context.Background(), key, 4242, &strings.Builder{}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(f.claude.prompts[0], "## Repository knowledge") {
			t.Error("empty knowledge produced a section")
		}
	})
	t.Run("unreadable store", func(t *testing.T) {
		f := newFixture()
		dir := t.TempDir()
		f.app.Knowledge = knowledge.Store{Dir: dir}
		path := filepath.Join(dir, "testorg", "example", "learnings.yaml")
		os.MkdirAll(filepath.Dir(path), 0o700)
		os.WriteFile(path, []byte("learnings: [unterminated"), 0o600)
		key := started(t, f, startReq(t))
		f.claude.turn = prTurn("sid-1")
		var log strings.Builder
		if err := f.run(context.Background(), key, 4242, &log); err != nil {
			t.Fatal(err)
		}
		if len(f.claude.prompts) != 1 || strings.Contains(f.claude.prompts[0], "## Repository knowledge") {
			t.Error("unreadable knowledge must leave the section out and still run the turn")
		}
	})
}

func TestSelectorPromptDoesNotCarryRepositoryKnowledge(t *testing.T) {
	f, s := routed(t, agent.Selection{Rule: 1}, known)
	ks := knowledge.Store{Dir: t.TempDir()}
	f.app.Knowledge = ks
	learn(t, ks, fixtureRepo, "gh-1", "Run make lint before pushing.")
	if _, err := f.app.Route(context.Background(), routeReq); err != nil {
		t.Fatal(err)
	}
	if p := s.calls[0].Prompt; strings.Contains(p, "make lint") {
		t.Errorf("selector prompt carries knowledge:\n%s", p)
	}
}
