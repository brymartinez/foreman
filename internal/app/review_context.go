package app

import (
	"context"
	"fmt"
	"io"
	"regexp"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

var reviewCommit = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// prepareReview keeps each reviewer's own history separate. A missing or changed
// baseline falls back to a full review without spending an extra model turn.
func (a App) prepareReview(ctx context.Context, rec *task.Record, kind task.ReviewKind, reviewer task.Agent, brief agent.ReviewBrief, log io.Writer) agent.ReviewBrief {
	brief.PreviousHead, brief.PreviousFindings, brief.BaseHead = "", nil, ""
	previous := rec.LatestCompeting()
	if kind == task.SelfReview {
		previous = rec.LatestSelf()
	}
	prior := ""
	if previous != nil && previous.BaseHead != "" && previous.Reviewer == reviewer && reviewCommit.MatchString(previous.Head) && previous.BaseRef == rec.Delegation.BaseRef && rec.ScopeCovered(previous) && (!rec.Delegation.ClaimVerification || previous.PRD == rec.Delegation.PRDDigest) {
		prior = previous.Head
	}
	head, _ := rec.CurrentChange()
	r, err := a.Worktrees.ReviewRange(ctx, rec.Delegation.Worktree, rec.Delegation.BaseRef, head, prior)
	if err != nil {
		fmt.Fprintf(log, "foreman: review baseline unavailable; using a full review: %v\n", err)
		return brief
	}
	brief.BaseHead = r.BaseHead
	if prior != "" && previous.BaseHead != "" && previous.BaseHead == r.BaseHead && r.PreviousIsAncestor {
		brief.PreviousHead, brief.PreviousFindings = prior, previous.Findings
	}
	return brief
}

func reviewContext(out agent.ReviewOutcome, brief agent.ReviewBrief) agent.ReviewOutcome {
	out.BaseRef = "origin/" + brief.BaseBranch
	out.BaseHead, out.FromHead = brief.BaseHead, brief.PreviousHead
	return out
}
