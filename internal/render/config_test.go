package render

import (
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/task"
)

func TestConfigShowsResolvedValues(t *testing.T) {
	var out strings.Builder
	Config(&out, config.Config{
		Path: "/tmp/foreman.yaml", TasksDir: "/vault/tasks", WorktreesDir: "/tmp/worktrees", RepositoriesDir: "/tmp/repositories", LogDir: "/tmp/logs",
		StagesDir: "/tmp/stages", IgnoredRepos: []task.Repo{"acme/widgets"}, Claude: config.Claude{Command: "claude"}, Codex: config.Codex{Command: "codex"},
		Cleanup: config.Cleanup{OnDone: true, DeleteMergedBranch: false},
	})
	for _, want := range []string{"Config file: /tmp/foreman.yaml", "Ignore repositories: acme/widgets", "Review rounds: 3", "Delete merged branch: false"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}
