package app

import (
	"context"
	"fmt"

	"github.com/brymartinez/foreman/internal/task"
)

type BranchProposal struct {
	Name      string
	Reason    string
	Uncertain bool
}

// ProposeBranch uses tracker and task metadata without creating a worktree or task.
// A stored branch always wins, including on a failed task's next start.
func (a App) ProposeBranch(ctx context.Context, req StartRequest) (BranchProposal, error) {
	if req.Key.Source == task.Linear && !a.repoVisible(req.Repo, false) {
		return BranchProposal{}, fmt.Errorf("repository %s is outside the configured repository filters", req.Repo)
	}
	info, err := a.sourceRepo(ctx, &req)
	if err != nil {
		return BranchProposal{}, err
	}
	key := req.Key
	if key == (task.Key{}) {
		key = task.Key{Repo: info.Repo, Number: req.Issue}
	}
	if key.Repo != info.Repo {
		return BranchProposal{}, fmt.Errorf("issue belongs to %s, not %s", key.Repo, info.Repo)
	}
	rec, err := a.Store.Get(ctx, key)
	if err != nil {
		return BranchProposal{}, err
	}
	if rec != nil && rec.Delegation != nil && rec.Delegation.Branch != "" {
		return BranchProposal{Name: rec.Delegation.Branch, Reason: "existing task branch"}, nil
	}
	prior, continuePR, err := a.priorPR(ctx, rec)
	if err != nil {
		return BranchProposal{}, err
	}
	if continuePR {
		return BranchProposal{Name: prior.HeadRefName, Reason: fmt.Sprintf("branch of open PR #%d from the reset run", prior.Key.Number)}, nil
	}
	issue, err := a.issue(ctx, key)
	if err != nil {
		return BranchProposal{}, err
	}
	p := proposeBranch(req.BranchType, key, issue)
	if !p.Uncertain {
		if taken := priorBranch(prior, p.Name); taken != nil {
			p.Uncertain, p.Reason = true, taken.Error()
		}
	}
	return p, nil
}

func proposeBranch(branchType task.BranchType, key task.Key, issue Issue) BranchProposal {
	if key.Source == task.Linear && issue.BranchName != "" {
		if !task.ValidBranchName(issue.BranchName) {
			return BranchProposal{Name: issue.BranchName, Reason: "the tracker supplied an invalid branch name", Uncertain: true}
		}
		return BranchProposal{Name: issue.BranchName, Reason: "Linear branch suggestion"}
	}
	if branchType == "" {
		branchType = task.BranchTypeFor(issue.Title, issue.Labels)
	}
	work := task.WorkName(key.Number, issue.Title)
	if key.Source == task.Linear {
		work = "linear-" + task.Slug(issue.Identifier) + "-" + task.Slug(issue.Title)
	}
	name := task.BranchName(branchType, work)
	if !task.ValidBranchName(name) {
		return BranchProposal{Name: name, Reason: "invalid default branch name", Uncertain: true}
	}
	return BranchProposal{Name: name, Reason: "default branch name"}
}
