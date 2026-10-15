package knowledge

import (
	"strings"
	"testing"
)

func TestSanitizeAcceptsGenericGuidanceAndCollapsesSpace(t *testing.T) {
	got, err := Sanitize("  Run   go vet before\n go test.  ")
	if err != nil || got != "Run go vet before go test." {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestSanitizeRejectsReferencesPathsAndSecrets(t *testing.T) {
	for name, text := range map[string]string{
		"empty":               "   ",
		"issue number":        "Follow the pattern from #123.",
		"cross-repo issue":    "See acme/widgets#45 for context.",
		"PR word reference":   "As done in PR 88, wrap errors.",
		"url":                 "Read https://example.com/docs first.",
		"www url":             "Read www.example.com first.",
		"home path":           "Config lives in /Users/alice/.config/x.",
		"linux home":          "Config lives in /home/bob/app.",
		"tilde path":          "Edit ~/.local/share/foreman/config.yaml first.",
		"system path":         "Read /etc/passwd for the user list.",
		"root-level path":     "Write scratch files to /tmp/build first.",
		"windows path":        "Builds go to D:\\agents\\out first.",
		"commit sha":          "Revert commit 3f9a2c7d1e to fix it.",
		"github token":        "Use ghp_abcdefghijklmnopqrstuvwxyz0123456789 to push.",
		"openai key":          "Set sk-proj-abc123def456ghi789jkl012 in env.",
		"aws key":             "The key AKIAIOSFODNN7EXAMPLE works.",
		"base64 blob":         "Token dGhpcyBpcyBhIHNlY3JldCB0b2tlbjEyMzQ1Ng== is needed.",
		"email":               "Ask maintainer@example.com before merging.",
		"too long":            strings.Repeat("word ", 70),
		"slack token":         "Post with xoxb-1234-5678-abcdefgh.",
		"private key":         "Paste -----BEGIN PRIVATE KEY----- here.",
		"pull request number": "pull request #7 shows the way",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := Sanitize(text); err == nil {
				t.Fatalf("accepted %q as %q", text, got)
			}
		})
	}
}

func TestSanitizeKeepsOrdinaryWordsAndNumbers(t *testing.T) {
	for _, text := range []string{
		"Keep functions under 80 lines.",
		"Tests use table-driven subtests with t.Run.",
		"The deadbeef helper is deprecated.",
		"internal/app depends only on ports.go interfaces.",
		"Update docs/control-layer/architecture.md with each design change.",
		"Use and/or sparingly in error messages.",
	} {
		if _, err := Sanitize(text); err != nil {
			t.Errorf("rejected %q: %v", text, err)
		}
	}
}

func TestRedactRemovesSecretsButKeepsLocalReferences(t *testing.T) {
	got := Redact("Maintainer asked in #12 to drop ghp_abcdefghijklmnopqrstuvwxyz0123456789 and see https://x.io/a")
	if strings.Contains(got, "ghp_") || strings.Contains(got, "https://") {
		t.Errorf("redact kept a secret or url: %q", got)
	}
	if !strings.Contains(got, "#12") {
		t.Errorf("redact dropped a local reference: %q", got)
	}
}
