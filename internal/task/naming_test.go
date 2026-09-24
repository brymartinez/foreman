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

func TestBranchAndWorktreeShareWorkName(t *testing.T) {
	name := WorkName(825, "Provider retries")
	if got := BranchName(name); got != "agent/825-provider-retries" {
		t.Errorf("branch = %q", got)
	}
	if got := WorktreePath("/wt", "ridezoomo/armada-backend", name); got != "/wt/ridezoomo-armada-backend/825-provider-retries" {
		t.Errorf("worktree = %q", got)
	}
}
