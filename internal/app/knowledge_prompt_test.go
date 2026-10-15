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

const fixtureRepo = "ridezoomo/armada-backend"

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

func TestPlanningAndImplementationTurnsReceiveOnlyTheTasksRepositoryKnowledge(t *testing.T) {
	f := newFixture()
	ks := knowledge.Store{Dir: t.TempDir()}
	f.app.Knowledge = ks
	learn(t, ks, fixtureRepo, "gh-1", "Run make lint before pushing.")
	learn(t, ks, "other/repo", "gh-2", "Never touch the legacy billing module.")
	key := started(t, f, startReq(t))
	f.claude.turns = []agent.Turn{plannedTurn("sid-1"), prTurn("sid-2")}
	var log strings.Builder
	if err := f.app.RunTask(context.Background(), key, 4242, &log); err != nil {
		t.Fatal(err)
	}
	if len(f.claude.prompts) != 2 {
		t.Fatalf("prompts = %d", len(f.claude.prompts))
	}
	for i, p := range f.claude.prompts {
		if got := knowledgeSection(p); got != "- Run make lint before pushing." {
			t.Errorf("turn %d knowledge = %q", i, got)
		}
		if strings.Contains(p, "legacy billing") {
			t.Errorf("turn %d carries another repository's knowledge", i)
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

func TestRepositoryKnowledgeInPromptsStaysWithinEightItemsAndTwoThousandCharacters(t *testing.T) {
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
	if err := f.app.RunTask(context.Background(), key, 4242, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	got := knowledgeSection(f.claude.prompts[0])
	items := strings.Count(got, "- Keep the ")
	if items == 0 || items > 8 {
		t.Fatalf("items = %d:\n%s", items, got)
	}
	// The whole section counts: heading, notice, bullets, and newlines.
	_, section, _ := strings.Cut(f.claude.prompts[0], "## Repository knowledge")
	section, _, _ = strings.Cut(section, "\n## ")
	if n := len("## Repository knowledge" + section); n > 2000 {
		t.Errorf("knowledge section is %d characters", n)
	}
}

func TestRunsWithoutTheKnowledgeSectionWhenTheStoreIsEmptyOrUnreadable(t *testing.T) {
	t.Run("empty store", func(t *testing.T) {
		f := newFixture()
		f.app.Knowledge = knowledge.Store{Dir: t.TempDir()}
		key := started(t, f, startReq(t))
		f.claude.turn = prTurn("sid-1")
		if err := f.app.RunTask(context.Background(), key, 4242, &strings.Builder{}); err != nil {
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
		path := filepath.Join(dir, "ridezoomo", "armada-backend", "learnings.yaml")
		os.MkdirAll(filepath.Dir(path), 0o700)
		os.WriteFile(path, []byte("learnings: [unterminated"), 0o600)
		key := started(t, f, startReq(t))
		f.claude.turn = prTurn("sid-1")
		var log strings.Builder
		if err := f.app.RunTask(context.Background(), key, 4242, &log); err != nil {
			t.Fatal(err)
		}
		if len(f.claude.prompts) != 1 || strings.Contains(f.claude.prompts[0], "## Repository knowledge") {
			t.Error("unreadable knowledge must leave the section out and still run the turn")
		}
		if !strings.Contains(log.String(), "repository knowledge") {
			t.Errorf("log does not mention the knowledge read error:\n%s", log.String())
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
