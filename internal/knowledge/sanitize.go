package knowledge

import (
	"errors"
	"regexp"
	"strings"
)

// MaxText bounds one learning. Longer text is rarely generic guidance.
const MaxText = 300

// pattern matches when any hit is at least min bytes long.
type pattern struct {
	re     *regexp.Regexp
	min    int
	reason string
}

func (p pattern) hits(text string) bool {
	for _, m := range p.re.FindAllString(text, -1) {
		if len(m) >= p.min {
			return true
		}
	}
	return false
}

// secretPatterns never leave the agent's output, not even into the local evidence
// trail.
var secretPatterns = []pattern{
	{regexp.MustCompile(`(?i)https?://\S+|\bwww\.\S+`), 0, "url"},
	{regexp.MustCompile(`-----BEGIN [A-Z ]*KEY-----`), 0, "secret"},
	{regexp.MustCompile(`\b(gh[pousr]_|github_pat_|sk-|xox[abprs]-|AKIA|glpat-)[A-Za-z0-9_-]{8,}`), 0, "secret"},
	{regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.-]+`), 0, "email"},
	// Absolute paths: home, root-level, and drive paths. A slash inside a word, as in
	// internal/app or and/or, is a repository path or plain text and stays.
	{regexp.MustCompile(`\B~[/\\]\S*|\B/[\w.-]\S*|\b[A-Za-z]:\\\S*`), 0, "local path"},
	// Long runs mixing letters and digits: tokens, keys, base64 blobs.
	{regexp.MustCompile(`[A-Za-z0-9+/_-]*(?:[0-9][A-Za-z+/_-]|[A-Za-z+/_-][0-9])[A-Za-z0-9+/_-]*={0,2}`), 20, "token"},
}

// referencePatterns tie text to one task. They are fine in the local evidence trail
// but not in reusable guidance.
var referencePatterns = []pattern{
	{regexp.MustCompile(`#\d+`), 0, "issue reference"},
	{regexp.MustCompile(`(?i)\b(pr|pull request|issue|ticket)\s*#?\d+`), 0, "issue reference"},
	{regexp.MustCompile(`\b[0-9a-f]*(?:[0-9][a-f]|[a-f][0-9])[0-9a-f]*\b`), 7, "commit reference"},
}

// Sanitize returns text fit for knowledge.md: one line, short, with no issue, pull
// request, or commit references, URLs, local paths, or secret-shaped strings. The
// error names the reason and never repeats the text.
func Sanitize(text string) (string, error) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return "", errors.New("empty")
	}
	if len(text) > MaxText {
		return "", errors.New("too long")
	}
	for _, p := range append(secretPatterns[:len(secretPatterns):len(secretPatterns)], referencePatterns...) {
		if p.hits(text) {
			return "", errors.New(p.reason)
		}
	}
	return text, nil
}

// Redact keeps local references but removes secrets, URLs, and paths, for notes in
// the local evidence trail. It also bounds the length.
func Redact(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	for _, p := range secretPatterns {
		text = p.re.ReplaceAllStringFunc(text, func(m string) string {
			if len(m) >= p.min {
				return "[redacted]"
			}
			return m
		})
	}
	if len(text) > MaxText {
		text = strings.ToValidUTF8(text[:MaxText], "") + "…"
	}
	return text
}
