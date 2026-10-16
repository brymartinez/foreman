package task

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	slugMaxWords = 5
	slugMaxLen   = 40
)

var (
	// Matches a leading conventional-commit prefix such as "feat(stripe-handler): ".
	conventionalPrefix = regexp.MustCompile(`^([a-zA-Z]+)(\([^)]*\))?!?:\s*`)
	nonAlnum           = regexp.MustCompile(`[^a-z0-9]+`)
	foldAccents        = strings.NewReplacer(
		"à", "a", "á", "a", "â", "a", "ä", "a", "ã", "a", "å", "a",
		"è", "e", "é", "e", "ê", "e", "ë", "e",
		"ì", "i", "í", "i", "î", "i", "ï", "i",
		"ò", "o", "ó", "o", "ô", "o", "ö", "o", "õ", "o",
		"ù", "u", "ú", "u", "û", "u", "ü", "u",
		"ç", "c", "ñ", "n", "ß", "ss",
	)
)

// Slug turns an issue title into at most 5 lowercase ASCII words joined by
// hyphens, capped at 40 characters on a word boundary.
func Slug(title string) string {
	words := strings.Fields(nonAlnum.ReplaceAllString(foldAccents.Replace(strings.ToLower(conventionalPrefix.ReplaceAllString(title, ""))), " "))
	var out []string
	length := 0
	for _, w := range words {
		if len(out) == slugMaxWords || length+len(w)+len(out) > slugMaxLen {
			break
		}
		out = append(out, w)
		length += len(w)
	}
	if len(out) == 0 {
		if len(words) > 0 {
			return words[0][:slugMaxLen]
		}
		return "issue"
	}
	return strings.Join(out, "-")
}

// WorkName is the issue-derived part of a default branch: "825-provider-retries".
func WorkName(issue int, title string) string {
	return fmt.Sprintf("%d-%s", issue, Slug(title))
}

// BranchType is the conventional-commit type a branch is prefixed with: fix/825-….
type BranchType string

// BranchTypes are the prefixes foreman uses. Feat is the default.
var BranchTypes = []BranchType{"feat", "fix", "chore", "docs", "refactor", "perf", "test", "build", "ci"}

func ParseBranchType(s string) (BranchType, error) {
	t := BranchType(strings.ToLower(strings.TrimSpace(s)))
	for _, known := range BranchTypes {
		if t == known {
			return t, nil
		}
	}
	return "", fmt.Errorf("unknown branch type %q; use one of %v", s, BranchTypes)
}

// labelTypes maps common GitHub labels to a branch type.
var labelTypes = map[string]BranchType{
	"bug": "fix", "fix": "fix", "bugfix": "fix", "regression": "fix",
	"enhancement": "feat", "feature": "feat", "feat": "feat",
	"documentation": "docs", "docs": "docs",
	"chore": "chore", "maintenance": "chore", "dependencies": "chore",
	"refactor": "refactor", "performance": "perf", "perf": "perf",
	"test": "test", "tests": "test", "testing": "test", "ci": "ci", "build": "build",
}

// BranchTypeFor picks the type for an issue: a conventional prefix in the title
// ("fix: …") wins, then the first label that names a type (with or without a
// "type:" or "kind/" prefix), then feat.
func BranchTypeFor(title string, labels []string) BranchType {
	if m := conventionalPrefix.FindStringSubmatch(title); m != nil {
		if t, err := ParseBranchType(m[1]); err == nil {
			return t
		}
	}
	for _, l := range labels {
		name := strings.ToLower(strings.TrimSpace(l))
		if _, rest, ok := strings.Cut(name, ":"); ok {
			name = strings.TrimSpace(rest)
		} else if _, rest, ok := strings.Cut(name, "/"); ok {
			name = rest
		}
		if t, ok := labelTypes[name]; ok {
			return t
		}
	}
	return "feat"
}

func BranchName(t BranchType, workName string) string {
	return string(t) + "/" + workName
}

// ValidBranchName accepts tracker-provided names that are both Git-compatible
// and safe to use below a worktree directory.
func ValidBranchName(name string) bool {
	if name == "" || strings.HasPrefix(name, "-") || strings.HasSuffix(name, ".") || strings.Contains(name, "..") || strings.Contains(name, "@{") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
		for _, r := range part {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
				return false
			}
		}
	}
	return true
}

// WorktreePath is <root>/<owner>-<repo>/<branch>, with branch slashes as directories.
func WorktreePath(root string, repo Repo, branch string) string {
	return filepath.Join(root, strings.ReplaceAll(string(repo), "/", "-"), branch)
}

// RepositoryPath is the managed source checkout used by commands that run outside
// a user checkout. It is separate from task worktrees.
func RepositoryPath(root string, repo Repo) string {
	return filepath.Join(root, strings.ReplaceAll(string(repo), "/", "-"))
}
