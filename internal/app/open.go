package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// openPR pushes a cleanly reviewed local commit and opens its PR. A matching open PR
// for the branch, left by a run that stopped after creating it, is adopted without a
// turn. Otherwise the implementer pushes and opens it, and foreman checks the PR
// against the review. Either way the run ends there and the gate takes over.
func (r *taskRun) openPR() (step, error) {
	a := r.a
	if r.rec.FinalCheckPending() {
		return stepFinalCheck, nil
	}
	if err := r.enter(task.OpeningPR); err != nil {
		return stepDone, err
	}
	if _, _, textErr := a.reviewDescription(r.ctx, r.rec); textErr != nil {
		rec, err := a.update(r.final, r.rec.Key, existing(func(cur *task.Record) { failTask(cur, textErr.Error(), a.Now()) }))
		r.rec = rec
		return stepDone, err
	}
	if next, err := r.checkPRDBeforeOpening(); err != nil || next != stepOpenPR {
		return next, err
	}
	d := r.rec.Delegation
	found, err := a.GitHub.OpenPullRequestForBranch(r.ctx, r.rec.Repo, d.Branch)
	if err == nil && found != nil {
		pr, checkErr := a.checkOpenedPR(r.ctx, r.rec, agent.Outcome{PRNumber: found.Key.Number})
		r.rec, err = a.update(r.final, r.rec.Key, existing(func(cur *task.Record) {
			if checkErr != nil {
				failTask(cur, checkErr.Error(), a.Now())
				return
			}
			fmt.Fprintf(r.log, "foreman: PR #%d already has the reviewed commit and description; adopting it\n", pr.Key.Number)
			adoptOpenedPR(cur, pr, a.Now())
		}))
		return stepDone, err
	}
	if err != nil {
		r.rec, err = a.update(r.final, r.rec.Key, existing(func(cur *task.Record) {
			failTask(cur, fmt.Sprintf("look up an open PR for %s: %v", d.Branch, err), a.Now())
		}))
		return stepDone, err
	}
	bodyFile, err := writeBodyFile(d.ProposedBody)
	if err != nil {
		return stepDone, err
	}
	defer os.Remove(bodyFile)
	fmt.Fprintf(r.log, "foreman: the review of %s is clean; opening the PR\n", short(d.LocalHead))
	res := a.runTurn(r.ctx, r.rec, turnRequest{openBodyFile: bodyFile}, a.openedPR(r.log), r.log)
	r.rec, err = a.finishTurn(r.ctx, r.final, r.rec.Key, "opening the pull request", res, func(cur *task.Record) {
		applyTurnResult(cur, res, a.Now())
		if res.err == nil && res.turn.Outcome.Status == agent.PROpened {
			coverOpenedPR(cur)
			cur.State = task.WaitingForCI
		}
	}, r.log)
	return stepDone, err
}

// checkPRDBeforeOpening reads the PRD again for a task with claim verification, because
// it may have changed since the clean review, even across a restart. A changed PRD
// sends the change back to review, and a PRD the review could not load stops the task.
func (r *taskRun) checkPRDBeforeOpening() (step, error) {
	a := r.a
	if !r.rec.Delegation.ClaimVerification {
		return stepOpenPR, nil
	}
	prd, err := a.reviewPRD(r.ctx, r.rec)
	if err != nil {
		r.rec, err = a.update(r.final, r.rec.Key, existing(func(cur *task.Record) {
			failTask(cur, fmt.Sprintf("open the reviewed pull request: %v", err), a.Now())
			applyStop(r.ctx, cur, "opening the pull request", r.log, a.Now())
		}))
		return stepDone, err
	}
	if task.Digest(prd) == r.rec.Delegation.PRDDigest {
		return stepOpenPR, nil
	}
	fmt.Fprintf(r.log, "foreman: the PRD changed since the clean review of %s; reviewing again\n", short(r.rec.Delegation.LocalHead))
	r.rec, err = a.update(r.final, r.rec.Key, existing(func(cur *task.Record) {
		cur.Delegation.PRDDigest = task.Digest(prd)
		cur.State = cur.ReviewStage()
	}))
	return stepReview, err
}

// openTurn runs the turn that pushes the reviewed commit and opens its PR. Its prompt
// stands alone, so a session that is gone is replaced by a fresh one with the same
// prompt.
func (a App) openTurn(ctx context.Context, rec *task.Record, adapter AgentAdapter, in agent.StartInput, bodyFile string, log io.Writer) (agent.Turn, error) {
	d := rec.Delegation
	brief := agent.OpenPRBrief{
		Branch: d.Branch, BaseBranch: strings.TrimPrefix(d.BaseRef, "origin/"), Head: d.LocalHead,
		Title: d.ProposedTitle, IssueURL: rec.URL, BodyFile: bodyFile, Validation: rec.Validation, Decisions: rec.Undelivered(),
		Claims: d.Claims,
	}
	if f := rec.FullResult; f != nil {
		brief.Unverified = f.Unverified
	}
	in.Prompt = agent.OpenPRPrompt(brief)
	if err := a.startAttempt(ctx, rec); err != nil {
		return agent.Turn{}, err
	}
	if d.Implementer.ID != "" {
		fmt.Fprintf(log, "foreman: resuming %s session %s\n", adapter.Name(), d.Implementer.ID)
		turn, err := adapter.Resume(ctx, d.Implementer, in)
		if !errors.Is(err, agent.ErrNoSession) {
			return turn, err
		}
		fmt.Fprintf(log, "foreman: session is gone (%v); starting a fresh turn to open the PR\n", err)
	}
	return adapter.Start(ctx, in, func(s task.Session) {
		_, _ = a.update(ctx, rec.Key, existing(func(cur *task.Record) {
			cur.Delegation.Implementer = s
		}))
	})
}

// openedPR is the check of the open turn's pr_opened claim.
func (a App) openedPR(log io.Writer) prCheck {
	return func(ctx context.Context, rec *task.Record, o agent.Outcome) (*PullRequest, error) {
		pr, err := a.checkOpenedPR(ctx, rec, o)
		if err == nil {
			fmt.Fprintf(log, "foreman: verified PR #%d %s at the reviewed commit %s\n", pr.Key.Number, pr.URL, short(pr.HeadSHA))
		}
		return pr, err
	}
}

// checkOpenedPR verifies the PR like any reported PR, then checks that it holds what
// the reviewer passed: the reviewed commit, title, and description. A mismatch names
// the PR so the developer can look at it.
func (a App) checkOpenedPR(ctx context.Context, rec *task.Record, o agent.Outcome) (*PullRequest, error) {
	pr, err := a.verifyPR(ctx, rec, o)
	if err != nil {
		return nil, err
	}
	d := rec.Delegation
	switch {
	case pr.HeadSHA != d.LocalHead:
		err = fmt.Errorf("its head is %s, not the reviewed commit %s", short(pr.HeadSHA), short(d.LocalHead))
	case strings.TrimSpace(pr.Title) != strings.TrimSpace(d.ProposedTitle):
		err = fmt.Errorf("its title %q is not the reviewed title %q", pr.Title, d.ProposedTitle)
	case normalizeBody(pr.Body) != normalizeBody(d.ProposedBody):
		err = errors.New("its description is not the reviewed description")
	}
	if err != nil {
		return nil, fmt.Errorf("PR #%d does not match the clean review: %w", pr.Key.Number, err)
	}
	return pr, nil
}

func normalizeBody(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
}

// adoptOpenedPR records a PR that already holds the reviewed commit, as a pr_opened
// turn would.
func adoptOpenedPR(rec *task.Record, pr *PullRequest, now time.Time) {
	rec.State = task.WaitingForCI
	rec.CI = nil
	rec.Delegation.PR, rec.Delegation.PRURL, rec.Delegation.PRHead = pr.Key.Number, pr.URL, pr.HeadSHA
	rec.Delegation.CheckpointNext = ""
	rec.LastOutcome = &task.Outcome{Status: string(agent.PROpened), Summary: fmt.Sprintf("adopted PR #%d for the reviewed commit", pr.Key.Number), At: now}
	coverOpenedPR(rec)
}

// coverOpenedPR makes the clean local competing review the review of the new PR's
// head, so the task goes on to CI without a second review, and clears the local proposal.
func coverOpenedPR(rec *task.Record) {
	d := rec.Delegation
	if latest := rec.LatestCompeting(); latest != nil && latest.Clean && latest.PR == 0 && latest.Head == d.PRHead {
		latest.PR = d.PR
	}
	d.LocalHead, d.ProposedTitle, d.ProposedBody = "", "", ""
	d.FinalChecks = 0
}

// writeBodyFile puts the reviewed description in a file outside the worktree, so gh
// reads it unchanged and the worktree stays clean.
func writeBodyFile(body string) (string, error) {
	f, err := os.CreateTemp("", "foreman-pr-body-*.md")
	if err != nil {
		return "", fmt.Errorf("write the PR description file: %w", err)
	}
	_, err = f.WriteString(body + "\n")
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("write the PR description file: %w", err)
	}
	return f.Name(), nil
}
