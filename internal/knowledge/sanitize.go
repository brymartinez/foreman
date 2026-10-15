package knowledge

import (
	"errors"
	"regexp"
	"strings"
)

// MaxText bounds one learning. Longer text is rarely generic guidance.
const MaxText = 300

// pattern matches when any hit is at least min bytes long and not one it skips.
type pattern struct {
	re     *regexp.Regexp
	min    int
	reason string
	skip   func(string) bool
}

func (p pattern) hits(text string) bool {
	for _, m := range p.re.FindAllString(text, -1) {
		if len(m) >= p.min && (p.skip == nil || !p.skip(m)) {
			return true
		}
	}
	return false
}

// secretPatterns never leave the agent's output, not even into the local evidence
// trail.
var secretPatterns = []pattern{
	{regexp.MustCompile(`(?i)https?://\S+|\bwww\.\S+`), 0, "url", nil},
	{regexp.MustCompile(`-----BEGIN [A-Z ]*KEY-----`), 0, "secret", nil},
	{regexp.MustCompile(`\b(gh[pousr]_|github_pat_|sk-|xox[abprs]-|AKIA|glpat-)[A-Za-z0-9_-]{8,}`), 0, "secret", nil},
	{regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.-]+`), 0, "email", nil},
	// Absolute paths: home, root-level, and drive paths. A slash inside a word, as in
	// internal/app or and/or, is a repository path or plain text and stays.
	{regexp.MustCompile(`\B~[/\\]\S*|\B/[\w.-]\S*|\b[A-Za-z]:\\\S*`), 0, "local path", nil},
	// Long runs mixing letters and digits: tokens, keys, base64 blobs.
	{regexp.MustCompile(`[A-Za-z0-9+/_-]*(?:[0-9][A-Za-z+/_-]|[A-Za-z+/_-][0-9])[A-Za-z0-9+/_-]*={0,2}`), 20, "token", nil},
}

// referencePatterns tie text to one task. They are fine in the local evidence trail
// but not in reusable guidance.
var referencePatterns = []pattern{
	{regexp.MustCompile(`#\d+`), 0, "issue reference", nil},
	// Linear and Jira keys such as KUB-129. Encodings and standards keep their names.
	{regexp.MustCompile(`\b[A-Z][A-Z0-9]{0,9}-\d+\b`), 0, "issue reference", standardName},
	{regexp.MustCompile(`(?i)\b(pr|pull request|issue|ticket)\s*#?\d+`), 0, "issue reference", nil},
	{regexp.MustCompile(`\b[0-9a-f]*(?:[0-9][a-f]|[a-f][0-9])[0-9a-f]*\b`), 7, "commit reference", nil},
}

// standardNames are prefixes of names such as UTF-8 or SHA-256 that look like issue
// keys but name an encoding, algorithm, or standard.
var standardNames = map[string]bool{"UTF": true, "SHA": true, "ISO": true, "RFC": true, "HTTP": true, "TLS": true, "AES": true, "MD": true, "ES": true, "ECMA": true, "IEEE": true}

func standardName(m string) bool {
	prefix, _, _ := strings.Cut(m, "-")
	return standardNames[prefix]
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
