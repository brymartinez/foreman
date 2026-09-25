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

// DefaultReviewRounds is the review budget per developer decision (D11).
const DefaultReviewRounds = 3

// RunTask is the body of the detached runner. It drives implement → verify PR →
// review → fix until the review is clean, the task needs the developer, or it fails.
// Every step's result is written to the task file before the next one starts.
func (a App) RunTask(ctx context.Context, key task.Key, pid int, log io.Writer) error {
	rec, err := a.Store.Update(ctx, key, func(cur *task.Record) (*task.Record, error) {
		if cur == nil || cur.Delegation == nil || cur.State != task.Working {
			return nil, errors.New("task is not WORKING; nothing to run")
		}
		cur.Delegation.RunnerPID = pid
		return cur, nil
	})
	if err != nil {
		return err
	}
	final := context.WithoutCancel(ctx)
	defer a.Store.Update(final, key, func(cur *task.Record) (*task.Record, error) {
		if cur.Delegation.RunnerPID == pid {
			cur.Delegation.RunnerPID = 0
		}
		return cur, nil
	})

	var findings *task.Review
	for {
		turn, runErr := a.implement(ctx, rec, findings, log)
		var pr *PullRequest
		if runErr == nil && turn.Outcome.Status == agent.PROpened {
			if pr, runErr = a.verifyPR(ctx, rec, turn.Outcome); runErr == nil {
				fmt.Fprintf(log, "foreman: verified PR #%d %s\n", pr.Key.Number, pr.URL)
			}
		}
		if rec, err = a.Store.Update(final, key, func(cur *task.Record) (*task.Record, error) {
			applyTurn(cur, turn, pr, runErr, a.Now())
			return cur, nil
		}); err != nil || rec.State != task.Reviewing {
			return err
		}

		if rec.ChangeRequestsSinceDecision() >= a.reviewRounds() {
			_, err = a.Store.Update(final, key, func(cur *task.Record) (*task.Record, error) {
				escalateReview(cur, a.reviewRounds(), a.Now())
				return cur, nil
			})
			return err
		}
		review, reviewErr := a.review(ctx, rec, log)
		if rec, err = a.Store.Update(final, key, func(cur *task.Record) (*task.Record, error) {
			applyReview(cur, review, reviewErr, a.Now())
			return cur, nil
		}); err != nil || rec.State != task.Working {
			return err
		}
		findings = rec.LatestReview()
	}
}

func (a App) reviewRounds() int {
	if a.ReviewRounds > 0 {
		return a.ReviewRounds
	}
	return DefaultReviewRounds
}

// implement runs one implementer turn. It resumes the session with an answered
// decision or a review round when there is one, and otherwise (or when the session
// is gone) starts a fresh turn whose prompt is rebuilt from the task file.
func (a App) implement(ctx context.Context, rec *task.Record, findings *task.Review, log io.Writer) (agent.Turn, error) {
	d := rec.Delegation
	adapter, ok := a.Agents[d.Implementer.Agent]
	if !ok {
		return agent.Turn{}, fmt.Errorf("agent %s is not configured", d.Implementer.Agent)
	}
	in := agent.StartInput{Worktree: d.Worktree, Log: log}
	resume := ""
	if dec := rec.Undelivered(); dec != nil {
		resume = agent.ResumeMessage(*dec)
	} else if findings != nil {
		resume = agent.FindingsMessage(*findings)
	}
	if resume != "" && d.Implementer.ID != "" {
		fmt.Fprintf(log, "foreman: resuming %s session %s\n", adapter.Name(), d.Implementer.ID)
		in.Prompt = resume
		turn, err := adapter.Resume(ctx, d.Implementer, in)
		if !errors.Is(err, agent.ErrNoSession) {
			return turn, err
		}
		fmt.Fprintf(log, "foreman: session is gone (%v); starting a fresh turn rebuilt from the task file\n", err)
	}
	brief, err := a.brief(ctx, rec)
	if err != nil {
		return agent.Turn{}, err
	}
	if latest := rec.LatestReview(); latest != nil && !latest.Clean {
		brief.OpenFindings = latest.Findings
	}
	// The repo layer comes from the worktree: the committed .foreman/ on the base (D48).
	implement, err := a.Stages.Resolve(d.Worktree, task.StageImplement)
	if err != nil {
		return agent.Turn{}, err
	}
	brief.StageInstructions = implement.Content()
	in.Prompt = agent.ImplementationPrompt(brief)
	fmt.Fprintf(log, "foreman: starting %s in %s\n", adapter.Name(), d.Worktree)
	return adapter.Start(ctx, in, func(s task.Session) {
		_, _ = a.Store.Update(ctx, rec.Key, func(cur *task.Record) (*task.Record, error) {
			cur.Delegation.Implementer = s
			return cur, nil
		})
	})
}

// review runs the competing agent in a fresh, read-only session.
func (a App) review(ctx context.Context, rec *task.Record, log io.Writer) (agent.ReviewOutcome, error) {
	d := rec.Delegation
	reviewer, ok := a.Agents[d.Reviewer]
	if !ok {
		return agent.ReviewOutcome{}, fmt.Errorf("reviewer %s is not configured", d.Reviewer)
	}
	brief, err := a.brief(ctx, rec)
	if err != nil {
		return agent.ReviewOutcome{}, err
	}
	fmt.Fprintf(log, "foreman: %s reviewing PR #%d (round %d)\n", reviewer.Name(), d.PR, len(rec.Reviews)+1)
	return reviewer.Review(ctx, agent.StartInput{
		Worktree: d.Worktree,
		Log:      log,
		Prompt:   agent.ReviewPrompt(agent.ReviewBrief{TaskBrief: brief, PR: d.PR, Validation: rec.Validation, Previous: rec.Reviews}),
	})
}

// brief gathers the durable task context both prompts are built from.
func (a App) brief(ctx context.Context, rec *task.Record) (agent.TaskBrief, error) {
	d := rec.Delegation
	issue, err := a.GitHub.GetIssue(ctx, rec.Key)
	if err != nil {
		return agent.TaskBrief{}, err
	}
	var contextText []byte
	if d.ContextFile != "" {
		if contextText, err = os.ReadFile(d.ContextFile); err != nil {
			return agent.TaskBrief{}, fmt.Errorf("context file: %w", err)
		}
	}
	return agent.TaskBrief{
		Key:        rec.Key,
		Title:      issue.Title,
		URL:        issue.URL,
		IssueBody:  issue.Body,
		Context:    string(contextText),
		Branch:     d.Branch,
		BaseBranch: strings.TrimPrefix(d.BaseRef, "origin/"),
		Worktree:   d.Worktree,
		Decisions:  rec.Decisions,
	}, nil
}

// verifyPR checks the agent's pr_opened claim against GitHub: the PR must be open,
// in this repo, from the task branch into the task's base. Without a number, the
// open PR for the branch is looked up.
func (a App) verifyPR(ctx context.Context, rec *task.Record, o agent.Outcome) (*PullRequest, error) {
	d := rec.Delegation
	var pr PullRequest
	if o.PRNumber > 0 {
		got, err := a.GitHub.GetPullRequest(ctx, task.Key{Repo: rec.Repo, Number: o.PRNumber})
		if err != nil {
			return nil, fmt.Errorf("agent reported PR #%d, but it could not be loaded: %w", o.PRNumber, err)
		}
		pr = got
	} else {
		got, err := a.GitHub.OpenPullRequestForBranch(ctx, rec.Repo, d.Branch)
		if err != nil {
			return nil, err
		}
		if got == nil {
			return nil, fmt.Errorf("agent reported a PR, but no open PR has head %s", d.Branch)
		}
		pr = *got
	}
	base := strings.TrimPrefix(d.BaseRef, "origin/")
	switch {
	case pr.State != "open":
		return nil, fmt.Errorf("PR #%d is %s, not open", pr.Key.Number, pr.State)
	case pr.IsCrossRepository:
		return nil, fmt.Errorf("PR #%d comes from a fork, not %s", pr.Key.Number, rec.Repo)
	case pr.HeadRefName != d.Branch:
		return nil, fmt.Errorf("PR #%d is from %s, not the task branch %s", pr.Key.Number, pr.HeadRefName, d.Branch)
	case pr.BaseRefName != base:
		return nil, fmt.Errorf("PR #%d targets %s, not %s", pr.Key.Number, pr.BaseRefName, base)
	}
	return &pr, nil
}

// applyTurn maps a finished implementer turn onto the task. A completed turn has
// received every answered decision. pr is the verified PR for pr_opened.
func applyTurn(rec *task.Record, turn agent.Turn, pr *PullRequest, runErr error, now time.Time) {
	if turn.Session.ID != "" {
		rec.Delegation.Implementer = turn.Session
	}
	if runErr != nil {
		rec.State = task.Failed
		rec.LastOutcome = &task.Outcome{Status: "failed", Summary: runErr.Error(), At: now}
		return
	}
	// The agent acted on the branch, so the gate's last CI result no longer applies.
	rec.CI = nil
	for i := range rec.Decisions {
		if d := &rec.Decisions[i]; d.Answer != "" && d.DeliveredAt.IsZero() {
			d.DeliveredAt = now
		}
	}
	o := turn.Outcome
	rec.LastOutcome = &task.Outcome{Status: string(o.Status), Summary: o.Summary, At: now}
	switch o.Status {
	case agent.PROpened:
		rec.State = task.Reviewing
		rec.Delegation.PR, rec.Delegation.PRURL = pr.Key.Number, pr.URL
		rec.Validation = o.Validation
	case agent.Blocked:
		rec.State = task.NeedsDecision
		rec.Decisions = append(rec.Decisions, task.Decision{AskedAt: now, Question: *o.Question})
	case agent.FailedRun:
		rec.State = task.Failed
	}
}

// applyReview records a review round. Findings send the task back to WORKING for the
// implementer; a clean review leaves it REVIEWING for the readiness gate (gate.go).
func applyReview(rec *task.Record, r agent.ReviewOutcome, reviewErr error, now time.Time) {
	if reviewErr != nil {
		rec.State = task.Failed
		rec.LastOutcome = &task.Outcome{Status: "failed", Summary: fmt.Sprintf("%s review failed: %v", rec.Delegation.Reviewer, reviewErr), At: now}
		return
	}
	rec.Reviews = append(rec.Reviews, task.Review{
		Round:    len(rec.Reviews) + 1,
		Reviewer: rec.Delegation.Reviewer,
		PR:       rec.Delegation.PR,
		Clean:    r.Clean,
		Summary:  r.Summary,
		Findings: r.Findings,
		At:       now,
	})
	if r.Clean {
		rec.LastOutcome = &task.Outcome{Status: "review_clean", Summary: r.Summary, At: now}
		return
	}
	rec.State = task.Working
	rec.LastOutcome = &task.Outcome{Status: "changes_requested", Summary: fmt.Sprintf("%d findings: %s", len(r.Findings), r.Summary), At: now}
}

// escalateReview asks the developer how to proceed when the review budget is spent.
// The answer goes to the implementer like any decision, and resets the budget.
func escalateReview(rec *task.Record, rounds int, now time.Time) {
	latest := rec.LatestReview()
	var ctx strings.Builder
	fmt.Fprintf(&ctx, "%s reviewed PR #%d in %s without a clean result. Latest findings:", rec.Delegation.Reviewer, rec.Delegation.PR, roundsText(rounds))
	recommend := "B"
	for _, f := range latest.Findings {
		fmt.Fprintf(&ctx, "\n- [%s] %s", f.Severity, f.Summary)
		if f.Severity == "blocking" {
			recommend = "A"
		}
	}
	rec.State = task.NeedsDecision
	rec.LastOutcome = &task.Outcome{Status: "blocked", Summary: "review not clean after " + roundsText(rounds), At: now}
	rec.Decisions = append(rec.Decisions, task.Decision{AskedAt: now, Question: task.Question{
		Issue:   fmt.Sprintf("The reviewer still requests changes after %s. How should %s proceed?", roundsText(rounds), rec.Delegation.Implementer.Agent),
		Context: ctx.String(),
		Options: []task.Option{
			{Label: "A", Description: "Address the remaining findings in another round"},
			{Label: "B", Description: "Leave the code as is; treat the remaining findings as accepted"},
		},
		Recommendation: recommend,
		ImpactIfWrong:  "A spends more agent time on disputed findings; B sends the remaining findings to your review unfixed.",
	}})
}

func roundsText(n int) string {
	if n == 1 {
		return "1 round"
	}
	return fmt.Sprintf("%d rounds", n)
}
