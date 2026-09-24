package task

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	BranchPrefix = "agent/"
	slugMaxWords = 5
	slugMaxLen   = 40
)

var (
	// Matches a leading conventional-commit prefix such as "feat(stripe-handler): ".
	conventionalPrefix = regexp.MustCompile(`^[a-zA-Z]+(\([^)]*\))?!?:\s*`)
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

// WorkName is the shared identifier for a task's branch and worktree directory: "825-provider-retries".
// It is computed once at start and stored, so later title edits don't move the branch.
func WorkName(issue int, title string) string {
	return fmt.Sprintf("%d-%s", issue, Slug(title))
}

func BranchName(workName string) string {
	return BranchPrefix + workName
}

// WorktreePath is <root>/<owner>-<repo>/<workName>, so equal repo names under different owners never collide.
func WorktreePath(root string, repo Repo, workName string) string {
	return filepath.Join(root, strings.ReplaceAll(string(repo), "/", "-"), workName)
}
