package task

import "testing"

func TestSlug(t *testing.T) {
	for title, want := range map[string]string{
		"Provider retries": "provider-retries",
		"feat(stripe-handler): New Stripe Invoice event":    "new-stripe-invoice-event",
		"fix!: Crash on empty payload":                      "crash-on-empty-payload",
		"Upgrade to Nest 12":                                "upgrade-to-nest-12",
		"Add retries for 5xx responses in the event queue":  "add-retries-for-5xx-responses",
		"Événements: pagination & cursor bug":               "evenements-pagination-cursor-bug",
		"Internationalization localization synchronization": "internationalization-localization",
		"!!!": "issue",
		"Supercalifragilisticexpialidociousnessesqueness bug": "supercalifragilisticexpialidociousnesses",
	} {
		if got := Slug(title); got != want {
			t.Errorf("Slug(%q) = %q, want %q", title, got, want)
		}
		if len(Slug(title)) > slugMaxLen {
			t.Errorf("Slug(%q) longer than %d", title, slugMaxLen)
		}
	}
}

func TestBranchAndWorktreeUseFullBranchName(t *testing.T) {
	name := WorkName(825, "Provider retries")
	if got := BranchName("fix", name); got != "fix/825-provider-retries" {
		t.Errorf("branch = %q", got)
	}
	if got := WorktreePath("/wt", "testorg/example", BranchName("fix", name)); got != "/wt/testorg-example/fix/825-provider-retries" {
		t.Errorf("worktree = %q", got)
	}
}

func TestTrackerBranchNameValidation(t *testing.T) {
	for _, name := range []string{"bryan/web-42-fix-cache", "team/feature_42"} {
		if !ValidBranchName(name) {
			t.Errorf("valid branch %q rejected", name)
		}
	}
	for _, name := range []string{"", "-bad", "../outside", "team//issue", "team/.hidden", "team/name.lock", "team/name..other", "team/a b"} {
		if ValidBranchName(name) {
			t.Errorf("invalid branch %q accepted", name)
		}
	}
}

func TestRepositoryPath(t *testing.T) {
	if got := RepositoryPath("/repos", "testorg/example"); got != "/repos/testorg-example" {
		t.Errorf("repository = %q", got)
	}
}

func TestBranchTypeFor(t *testing.T) {
	for _, tc := range []struct {
		title  string
		labels []string
		want   BranchType
	}{
		{"Provider retries", nil, "feat"},
		{"fix(queue): Retry storm on 5xx", []string{"enhancement"}, "fix"},
		{"Docs!: Rewrite setup", nil, "docs"},
		{"wip: Something", []string{"bug"}, "fix"},
		{"Crash on empty payload", []string{"priority: high", "Bug"}, "fix"},
		{"Speed up search", []string{"type: performance"}, "perf"},
		{"Bump deps", []string{"kind/chore"}, "chore"},
		{"Add dark mode", []string{"good first issue"}, "feat"},
	} {
		if got := BranchTypeFor(tc.title, tc.labels); got != tc.want {
			t.Errorf("BranchTypeFor(%q, %v) = %s, want %s", tc.title, tc.labels, got, tc.want)
		}
	}
	if _, err := ParseBranchType("agent"); err == nil {
		t.Error("agent is not a branch type")
	}
}
