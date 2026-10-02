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

// DefaultFixRounds caps the fix and re-review cycles per developer decision (D11, D77).
const DefaultFixRounds = 3

// DefaultReviewRounds is how many passes the reviewer makes over a PR (D77).
const DefaultReviewRounds = 3

// RunTask is the body of the detached runner. It drives implement → verify PR →
// review → fix until the review is clean, the task needs the developer, or it fails.
// Every step's result is written to the task file before the next one starts.
func (a App) RunTask(ctx context.Context, key task.Key, pid int, log io.Writer) error {
	rec, err := a.update(ctx, key, func(cur *task.Record) (*task.Record, error) {
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
	defer a.update(final, key, existing(func(cur *task.Record) {
		if cur.Delegation.RunnerPID == pid {
			cur.Delegation.RunnerPID = 0
		}
	}))

	if rec.Delegation.ConflictPending {
		// The gate found the handed-over PR conflicting with its base (D130). A repair
		// that worked leaves a new head to review; anything else is the developer's.
		if rec, err = a.repairConflict(ctx, final, rec, log); err != nil || rec.State != task.Reviewing {
			return err
		}
	}
	var findings *task.Review
	// feedback is the comments on the PR this run has to answer, and rounds is how many
	// times it has answered. A resumed task starts here (D122).
	feedback, rec, done, err := a.unanswered(ctx, final, rec, log)
	if err != nil || done {
		return err
	}
	rounds := 0
	for reviewNext := len(feedback) == 0 && rec.ReviewPending(); ; reviewNext = false {
		// answered is set once a feedback turn has pushed a change, which goes to the
		// reviewer like any other change rather than through another implementer turn.
		answered := false
		if len(feedback) > 0 {
			rounds++
			answering := feedback
			feedback = nil
			if rec, err = a.answerFeedback(ctx, final, rec, answering, log); err != nil {
				return err
			}
			if rec.State != task.Reviewing && rec.State != task.ReadyForHuman {
				// Blocked, failed, or stopped. The comments stay unanswered, so the next
				// run asks for them again (D122).
				return nil
			}
			// A turn that pushed nothing leaves the code its review already covers, so
			// there is nothing to review and the task goes back to the developer.
			if !rec.ReviewPending() {
				if feedback, rec, err = a.beforeHandover(ctx, final, rec, rounds, log); err != nil || len(feedback) == 0 {
					return err
				}
				continue
			}
			answered = true
		}
		if !answered && reviewNext {
			// The PR is verified but its head has no review: pick up at the review. A
			// PR that is no longer open can't be reviewed, so the implementer takes over.
			pr, verifyErr := a.verifyPR(ctx, rec, agent.Outcome{PRNumber: rec.Delegation.PR})
			if verifyErr != nil {
				fmt.Fprintf(log, "foreman: can't review PR #%d (%v); implementing instead\n", rec.Delegation.PR, verifyErr)
				reviewNext = false
			} else {
				if rec, err = a.update(final, key, existing(func(cur *task.Record) {
					applyPRHead(cur, pr)
				})); err != nil {
					return err
				}
				fmt.Fprintf(log, "foreman: PR #%d head %s has no review yet; reviewing\n", pr.Key.Number, short(pr.HeadSHA))
			}
		}
		if !answered && !reviewNext {
			turn, runErr := a.implement(ctx, rec, findings, nil, nil, log)
			step := "implementation"
			if a.needsPlan(rec) {
				step = "planning"
			}
			if step == "planning" && runErr == nil && turn.Outcome.Status == agent.PROpened {
				fmt.Fprintf(log, "foreman: %s opened a PR without posting a plan\n", rec.Delegation.Implementer.Agent)
			}
			var pr *PullRequest
			if runErr == nil && turn.Outcome.Status == agent.PROpened {
				if pr, runErr = a.verifyPR(ctx, rec, turn.Outcome); runErr == nil {
					fmt.Fprintf(log, "foreman: verified PR #%d %s at %s\n", pr.Key.Number, pr.URL, short(pr.HeadSHA))
				}
			}
			if rec, err = a.update(final, key, existing(func(cur *task.Record) {
				applyTurn(cur, turn, pr, runErr, a.Now())
				if runErr != nil {
					applyStop(ctx, cur, step, log, a.Now())
				}
				addUsage(&cur.Usage, turn.Usage)
			})); err != nil {
				return err
			}
			if runErr == nil && turn.Outcome.Status == agent.Planned {
				if rec, err = a.postPlan(ctx, final, rec, turn.Outcome.Documents, log); err != nil || rec.State != task.Working {
					return err
				}
				continue
			}
			if rec.State != task.Reviewing {
				return nil
			}
		}

		// Every pass, including the first and one after a fix, reviews a PR that merges
		// cleanly into its latest base (D133).
		var ready bool
		if rec, ready, err = a.resolveConflicts(ctx, final, rec, log); err != nil || !ready {
			return err
		}
		review, reviewErr := a.review(ctx, rec, log)
		if rec, err = a.update(final, key, existing(func(cur *task.Record) {
			applyReview(cur, review, reviewErr, a.Now())
			if reviewErr != nil {
				applyStop(ctx, cur, "review", log, a.Now())
			}
			addUsage(&cur.Usage, review.Usage)
			// Every fix is reviewed; the developer is asked only once fix_rounds fixes
			// still leave findings (D78).
			if cur.State == task.Working && cur.ChangeRequestsSinceDecision() > a.fixRounds() {
				escalateReview(cur, a.fixRounds(), a.Now())
			}
		})); err != nil {
			return err
		}
		if rec.State != task.Working {
			// A clean review hands the task to the gate, so this is the run's last
			// chance to answer comments that arrived while it worked (D123).
			if reviewErr == nil && review.Clean && rec.State == task.Reviewing {
				if feedback, rec, err = a.beforeHandover(ctx, final, rec, rounds, log); err != nil || len(feedback) == 0 {
					return err
				}
				continue
			}
			return nil
		}
		findings = rec.LatestReview()
	}
}

// errTaskRemoved stops a runner whose task file was deleted while it ran.
var errTaskRemoved = errors.New("the task file was removed; stopping")

// errPRClosed stops a runner whose PR was closed while it ran. The gate records
// PR_CLOSED before it stops the runner, so the stop that follows is foreman's doing,
// not a failure of the agent's, and recording it would tell the developer their run
// failed when they closed the PR themselves (D107).
var errPRClosed = errors.New("the task's PR was closed; stopping")

// existing adapts a change to a task the runner owns. A task without a file or a
// delegation can't be advanced, so the run stops instead of recreating it, and
// neither can one foreman has already ended.
func existing(change func(cur *task.Record)) func(cur *task.Record) (*task.Record, error) {
	return func(cur *task.Record) (*task.Record, error) {
		if cur == nil || cur.Delegation == nil {
			return nil, errTaskRemoved
		}
		if cur.State == task.PRClosed {
			return nil, errPRClosed
		}
		change(cur)
		return cur, nil
	}
}

func (a App) fixRounds() int {
	if a.FixRounds > 0 {
		return a.FixRounds
	}
	return DefaultFixRounds
}

func (a App) reviewRounds() int {
	if a.ReviewRounds > 0 {
		return a.ReviewRounds
	}
	return DefaultReviewRounds
}

// implement runs one implementer turn. It resumes the session with an answered
// decision, a merge, PR feedback, or a review round when there is one, and otherwise
// (or when the session is gone) starts a fresh turn whose prompt is rebuilt from the
// task file.
// needsPlan is true until every planning document is on the issue. A task that
// already has a PR, from before planning existed, goes on without one (D90).
func (a App) needsPlan(rec *task.Record) bool {
	return !rec.PlanPosted() && rec.Delegation.PR == 0
}

func (a App) implement(ctx context.Context, rec *task.Record, findings *task.Review, feedback []PRComment, merge *agent.MergeBrief, log io.Writer) (agent.Turn, error) {
	d := rec.Delegation
	adapter, ok := a.Agents[d.Implementer.Agent]
	if !ok {
		return agent.Turn{}, fmt.Errorf("agent %s is not configured", d.Implementer.Agent)
	}
	planning := a.needsPlan(rec)
	stage := task.StageImplement
	if planning {
		stage = task.StagePlan
	}
	in := agent.StartInput{Worktree: d.Worktree, Log: log, MaxBudgetUSD: a.ImplementerBudgetUSD, Stage: stage}
	resume := ""
	var conflict *agent.Conflict
	if d.ConflictPending {
		conflict = &agent.Conflict{PR: d.PR, Base: strings.TrimPrefix(d.BaseRef, "origin/"), BaseHead: d.LastConflict().Base}
		resume = agent.ConflictMessage(*conflict)
	} else if decs := rec.Undelivered(); len(decs) > 0 {
		var open *task.Review
		if latest := rec.LatestReview(); latest != nil && !latest.Clean {
			open = latest
		}
		resume = agent.ResumeMessage(decs, open, planning)
	} else if merge != nil {
		resume = agent.MergeMessage(d.Branch, merge.Base, merge.Files)
	} else if len(feedback) > 0 {
		resume = agent.FeedbackMessage(rec.Repo, d.PR, feedbackItems(feedback))
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
	// The turn that pushed before a merge already fixed the latest findings.
	if latest := rec.LatestReview(); merge == nil && latest != nil && !latest.Clean {
		brief.OpenFindings = latest.Findings
	}
	brief.Merge = merge
	brief.PR, brief.Feedback, brief.Conflict = d.PR, feedbackItems(feedback), conflict
	// The repo layer comes from the worktree: the committed .foreman/ on the base (D48).
	implement, err := a.Stages.Resolve(d.Worktree, task.StageImplement)
	if err != nil {
		return agent.Turn{}, err
	}
	brief.StageInstructions = implement.Content()
	pr, err := a.Stages.Resolve(d.Worktree, task.StagePR)
	if err != nil {
		return agent.Turn{}, err
	}
	brief.PRInstructions = pr.Content()
	if planning {
		plan, err := a.Stages.Resolve(d.Worktree, task.StagePlan)
		if err != nil {
			return agent.Turn{}, err
		}
		brief.Planning, brief.PlanInstructions = true, plan.Content()
		fmt.Fprintf(log, "foreman: starting %s in %s to write the plan\n", adapter.Name(), d.Worktree)
	} else {
		fmt.Fprintf(log, "foreman: starting %s in %s\n", adapter.Name(), d.Worktree)
	}
	in.Prompt = agent.ImplementationPrompt(brief)
	return adapter.Start(ctx, in, func(s task.Session) {
		_, _ = a.update(ctx, rec.Key, existing(func(cur *task.Record) {
			cur.Delegation.Implementer = s
		}))
	})
}

// answerFeedback runs one turn that answers the comments it is given. The PR head
// before the turn is what tells a reply-only turn from one that changed code (D122).
func (a App) answerFeedback(ctx, final context.Context, rec *task.Record, comments []PRComment, log io.Writer) (*task.Record, error) {
	d := rec.Delegation
	headBefore := d.PRHead
	fmt.Fprintf(log, "foreman: %s\n", feedbackNote(len(comments), d.PR))
	turn, runErr := a.implement(ctx, rec, nil, comments, nil, log)
	var pr *PullRequest
	if runErr == nil && turn.Outcome.Status == agent.PROpened {
		if pr, runErr = a.verifyPR(ctx, rec, turn.Outcome); runErr == nil {
			fmt.Fprintf(log, "foreman: verified PR #%d %s at %s\n", pr.Key.Number, pr.URL, short(pr.HeadSHA))
		}
	}
	return a.update(final, rec.Key, existing(func(cur *task.Record) {
		applyFeedbackTurn(cur, turn, pr, runErr, comments, headBefore, a.Now())
		if runErr != nil {
			applyStop(ctx, cur, "answering PR feedback", log, a.Now())
		}
		addUsage(&cur.Usage, turn.Usage)
	}))
}

// applyFeedbackTurn records a turn that answered comments on the pull request. The
// comments it was given count as answered only once the turn finished, so a run that
// failed or asked a question leaves them for the next one. A turn that pushed nothing
// leaves the code its review already covers, so the task goes straight back to the
// developer with its CI result; a turn that pushed goes through the review and CI flow
// like any other change (D122).
func applyFeedbackTurn(rec *task.Record, turn agent.Turn, pr *PullRequest, runErr error, comments []PRComment, headBefore string, now time.Time) {
	ci := rec.CI
	applyTurn(rec, turn, pr, runErr, now)
	if runErr != nil || turn.Outcome.Status != agent.PROpened {
		return
	}
	rec.MarkFeedbackHandled(answered(comments), now)
	if pr.HeadSHA != headBefore {
		return
	}
	rec.CI = ci
	d := rec.Delegation
	// The same bar the gate applies: a clean review of this head and CI that finished.
	// Without both, the task stays under review and the gate hands it over (D122).
	if reviewCovers(rec, pr.HeadSHA) && ci != nil && ci.State != task.CIPending {
		rec.State = task.ReadyForHuman
		rec.LastOutcome = &task.Outcome{Status: "feedback_answered",
			Summary: fmt.Sprintf("answered %d %s on PR #%d; no change was needed", len(comments), plural(len(comments), "comment", "comments"), d.PR), At: now}
	}
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
	instructions, err := a.Stages.Resolve(d.Worktree, task.StageReview)
	if err != nil {
		return agent.ReviewOutcome{}, err
	}
	brief.StageInstructions = instructions.Content()
	// The reviewer has no network, so the PRD it checks against comes in the prompt.
	if c := rec.PlanComment(task.PRD); c != nil {
		if got, err := a.GitHub.GetIssueComment(ctx, rec.Repo, c.CommentID); err == nil {
			brief.PRD = got.Body
		} else {
			fmt.Fprintf(log, "foreman: reviewing without the PRD: %v\n", err)
		}
	}
	fmt.Fprintf(log, "foreman: %s reviewing PR #%d (round %d)\n", reviewer.Name(), d.PR, len(rec.Reviews)+1)
	return reviewer.Review(ctx, agent.StartInput{
		Worktree:     d.Worktree,
		Log:          log,
		MaxBudgetUSD: a.ReviewerBudgetUSD,
		Prompt: agent.ReviewPrompt(agent.ReviewBrief{TaskBrief: brief, PR: d.PR, Validation: rec.Validation, Previous: rec.Reviews,
			Passes: a.reviewRounds()}),
	})
}

func addUsage(total *task.Usage, turn task.Usage) {
	total.ClaudeCostUSD += turn.ClaudeCostUSD
	total.CodexInputTokens += turn.CodexInputTokens
	total.CodexCachedTokens += turn.CodexCachedTokens
	total.CodexOutputTokens += turn.CodexOutputTokens
	total.CodexReasoningTokens += turn.CodexReasoningTokens
	total.ClaudeTurns += turn.ClaudeTurns
	total.CodexTurns += turn.CodexTurns
}

// update is Store.Update plus the developer's notification: a task that moves into
// a state that needs them announces it, whichever command or runner moved it.
func (a App) update(ctx context.Context, key task.Key, fn func(cur *task.Record) (*task.Record, error)) (*task.Record, error) {
	var before task.State
	rec, err := a.Store.Update(ctx, key, func(cur *task.Record) (*task.Record, error) {
		if cur != nil {
			before = cur.State
		}
		return fn(cur)
	})
	if err == nil && rec != nil && rec.State != before {
		a.notify(key, rec)
	}
	return rec, err
}

func (a App) notify(key task.Key, rec *task.Record) {
	if a.Notifier == nil {
		return
	}
	var body string
	switch rec.State {
	case task.NeedsDecision:
		body = "Needs your decision"
	case task.Failed:
		body = "Run failed"
		if rec.LastOutcome != nil && rec.LastOutcome.Status == Cancelled {
			body = "Run cancelled"
		}
	case task.ReadyForHuman:
		body = "Ready for human review"
	case task.Reviewed:
		body = "PR feedback to answer"
	default:
		return
	}
	_ = a.Notifier.Notify(fmt.Sprintf("Foreman %s#%d", key.Repo, key.Number), body)
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
		Plan:       rec.Plan,
	}, nil
}

// verifyPR checks the agent's pr_opened claim against GitHub: the PR must be open,
// in this repo, from the task branch into the task's base. Without a number, the
// open PR for the branch is looked up. The task's own PR may target another base,
// because a maintainer can retarget it; the conflict check then follows it (D134).
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
	case pr.BaseRefName != base && (d.PR == 0 || pr.Key.Number != d.PR):
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
		rec.Delegation.PR, rec.Delegation.PRURL, rec.Delegation.PRHead = pr.Key.Number, pr.URL, pr.HeadSHA
		rec.Validation = o.Validation
	case agent.Blocked:
		rec.State = task.NeedsDecision
		for _, q := range o.Questions {
			rec.Decisions = append(rec.Decisions, task.Decision{AskedAt: now, Question: q})
		}
	case agent.FailedRun:
		rec.State = task.Failed
	}
}

// Cancelled is the outcome status of a run stopped by foreman cancel or a signal.
const Cancelled = "cancelled"

// applyStop marks a step that failed because the runner was told to stop. Its error
// is only the agent being killed, so the outcome says the run was cancelled instead.
func applyStop(ctx context.Context, rec *task.Record, step string, log io.Writer, now time.Time) {
	if ctx.Err() == nil {
		return
	}
	fmt.Fprintf(log, "foreman: stopped during %s: %v\n", step, context.Cause(ctx))
	rec.State = task.Failed
	rec.LastOutcome = &task.Outcome{Status: Cancelled, Summary: fmt.Sprintf("run cancelled during %s (%v)", step, context.Cause(ctx)), At: now}
}

// postPlan puts each planning document on the issue as its own comment, editing the
// comment from an earlier plan when there is one. Each comment is recorded as soon as
// it is posted. A failed post fails the task; restarting it plans again (D90).
func (a App) postPlan(ctx, final context.Context, rec *task.Record, docs map[task.PlanDoc]string, log io.Writer) (*task.Record, error) {
	for _, doc := range task.PlanDocs {
		var id int64
		if c := rec.PlanComment(doc); c != nil {
			id = c.CommentID
		}
		body := planCommentBody(doc, docs[doc])
		posted, err := a.GitHub.PostIssueComment(ctx, rec.Key, id, body)
		if errors.Is(err, ErrGone) && id > 0 {
			posted, err = a.GitHub.PostIssueComment(ctx, rec.Key, 0, body)
		}
		var updateErr error
		if rec, updateErr = a.update(final, rec.Key, existing(func(cur *task.Record) {
			if err != nil {
				cur.State = task.Failed
				cur.LastOutcome = &task.Outcome{Status: "failed", Summary: fmt.Sprintf("posting the %s on the issue failed: %v", doc.Title(), err), At: a.Now()}
				applyStop(ctx, cur, "planning", log, a.Now())
				return
			}
			cur.SetPlanComment(task.PlanComment{Doc: doc, CommentID: posted.ID, URL: posted.URL})
		})); updateErr != nil || err != nil {
			return rec, updateErr
		}
		fmt.Fprintf(log, "foreman: posted the %s: %s\n", doc.Title(), posted.URL)
	}
	return rec, nil
}

// maxCommentChars is GitHub's limit on an issue comment's length.
const maxCommentChars = 65536

// planCommentBody is one document's comment: a marker foreman can recognize, the
// document's heading, and the agent's text, cut to fit GitHub's limit.
func planCommentBody(doc task.PlanDoc, text string) string {
	head := fmt.Sprintf("<!-- foreman:%s -->\n## %s\n\n", doc, doc.Title())
	const cut = "\n\n_Truncated at GitHub's comment length limit._"
	text = strings.TrimSpace(text)
	if r := []rune(text); len([]rune(head))+len(r) > maxCommentChars {
		text = string(r[:maxCommentChars-len([]rune(head))-len([]rune(cut))]) + cut
	}
	return head + text
}

// applyPRHead records the PR head a resumed run is about to review.
func applyPRHead(rec *task.Record, pr *PullRequest) {
	rec.State = task.Reviewing
	rec.CI = nil
	rec.Delegation.PRHead = pr.HeadSHA
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
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
		Head:     rec.Delegation.PRHead,
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
	fmt.Fprintf(&ctx, "%s re-reviewed PR #%d after %s of fixes without a clean result. Latest findings, not yet addressed:", rec.Delegation.Reviewer, rec.Delegation.PR, roundsText(rounds))
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
		Issue:   fmt.Sprintf("The reviewer still requests changes after %s of fixes. How should %s proceed?", roundsText(rounds), rec.Delegation.Implementer.Agent),
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
