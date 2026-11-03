package app

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

type RepositoryProposal struct {
	Repo   task.Repo
	Reason string
}

var repositoryLink = regexp.MustCompile(`https://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)`)

// ProposeRepository reads issue and triage evidence without preparing a checkout.
// Only a single explicit destination can be inferred; other cases need a choice.
func (a App) ProposeRepository(ctx context.Context, key task.Key) (RepositoryProposal, error) {
	if key.Repo != "" {
		return RepositoryProposal{Repo: key.Repo, Reason: "existing task repository"}, nil
	}
	issue, err := a.issue(ctx, key)
	if err != nil {
		return RepositoryProposal{}, err
	}
	comments, err := a.issueComments(ctx, key)
	if err != nil {
		return RepositoryProposal{}, fmt.Errorf("read triage for repository selection: %w", err)
	}
	text := issue.Body
	for _, comment := range comments {
		if strings.HasPrefix(comment.Body, "## Triage decisions") || strings.HasPrefix(comment.Body, "## Triage correction") {
			text += "\n" + comment.Body
		}
	}
	repos := map[string]task.Repo{}
	for _, match := range repositoryLink.FindAllStringSubmatch(text, -1) {
		repo, err := task.ParseRepo(strings.TrimSuffix(strings.TrimRight(match[1], "."), ".git"))
		if err == nil {
			repos[strings.ToLower(string(repo))] = repo
		}
	}
	if len(repos) != 1 {
		return RepositoryProposal{Reason: "choose a repository; issue and triage do not identify one unambiguous GitHub repository"}, nil
	}
	var repo task.Repo
	for _, candidate := range repos {
		repo = candidate
	}
	if !a.repoVisible(repo, false) {
		return RepositoryProposal{Reason: fmt.Sprintf("%s is outside the configured repository filters; choose an allowed repository", repo)}, nil
	}
	return RepositoryProposal{Repo: repo, Reason: "GitHub repository linked in the issue or triage; confirm or edit"}, nil
}
