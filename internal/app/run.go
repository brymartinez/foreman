package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// DefaultFixRounds caps the fix and re-review cycles per developer decision.
const DefaultFixRounds = 3

// DefaultReviewRounds is how many passes the reviewer makes over a PR.
const DefaultReviewRounds = 1

// RunTask is the body of the detached runner. It drives implement → verify PR →
// review → fix until the review is clean, the task needs the developer, or it fails.
// Every step's result is written to the task file before the next one starts.
func (a App) RunTask(ctx context.Context, key task.Key, pid int, token string, log io.Writer) error {
	rec, err := a.update(ctx, key, func(cur *task.Record) (*task.Record, error) {
		if cur == nil || cur.Delegation == nil || !cur.State.Runnable() {
			return nil, errors.New("task is not in a stage a runner drives; nothing to run")
		}
		// Only the runner the task's launch spawned may work it. Any other would share
		// the worktree and the task file with that one.
		if !ownsLaunch(cur, token) {
			return nil, errors.New("this runner does not hold the task's launch; another launch owns the task")
		}
		cur.Delegation.Launch = nil
		cur.Delegation.RunnerPID = pid
		return cur, nil
	})
	if err != nil {
		return err
	}
	final := context.WithoutCancel(ctx)
	a = a.meterAgents(key)
	defer a.update(final, key, existing(func(cur *task.Record) {
		if cur.Delegation.RunnerPID == pid {
			cur.Delegation.RunnerPID = 0
		}
	}))
	if rec, err = a.logRoute(ctx, rec, log); err != nil {
		return err
	}
	if _, err := a.readScope(ctx, rec.Key); err != nil {
		_, saveErr := a.update(final, rec.Key, existing(func(cur *task.Record) { failTask(cur, err.Error(), a.Now()) }))
		return saveErr
	}

	if rec.Delegation.ConflictPending {
		// The gate found the handed-over PR conflicting with its base. A repair
		// that worked leaves a new head to review; anything else is the developer's.
		if rec, err = a.repairConflict(ctx, final, rec, log); err != nil || !rec.State.Reviewing() {
			return err
		}
	}
	var feedback []PRComment
	if a.ReplyToPRFeedback {
		// A resumed task answers the comments on its PR first.
		var done bool
		feedback, rec, done, err = a.unanswered(ctx, final, rec, log)
		if err != nil || done {
			return err
		}
	} else if len(rec.UnhandledFeedback()) > 0 && rec.Delegation.PR != 0 && reviewCovers(rec, rec.Delegation.PRHead) {
		var current bool
		if rec, current, err = a.currentPR(ctx, final, rec); err != nil {
			return err
		}
		if current && reviewCovers(rec, rec.Delegation.PRHead) && !rec.FinalCheckPending() {
			// A task left by the old feedback flow already passed review. Hand it
			// back without resuming the implementer on the same comments.
			_, err = a.update(final, rec.Key, existing(func(cur *task.Record) {
				cur.State = task.WaitingForCI
				if handsOverNow(cur, cur.CI) {
					cur.State = task.ReadyForHuman
				}
				cur.Feedback = nil
			}))
			return err
		}
	}
	if stoppedAtLimit(rec) {
		_, err = a.update(final, rec.Key, existing(func(cur *task.Record) {
			failTask(cur, stopSummary(cur), a.Now())
			markDelivered(cur, a.Now())
		}))
		return err
	}
	r := &taskRun{a: a, ctx: ctx, final: final, log: log, rec: rec, feedback: feedback,
		forcePlan: rec.Delegation.PR != 0 && !rec.PlanPosted()}
	if err := r.loop(firstStep(rec, len(feedback))); err != nil {
		return err
	}
	a.learnAfterRun(r.rec, log)
	return nil
}

// logRoute writes the route line once per routing decision, ahead of the run's
// agent:model:step lines, so the log says which profile ran and why.
func (a App) logRoute(ctx context.Context, rec *task.Record, log io.Writer) (*task.Record, error) {
	d := rec.Delegation
	if d.Profile == nil || d.RouteLogged {
		return rec, nil
	}
	fmt.Fprintf(log, "route:%s:%s:%s\n", d.Profile.Name, d.Profile.Agent, strings.Join(strings.Fields(d.RouteReason), " "))
	return a.update(ctx, rec.Key, existing(func(cur *task.Record) { cur.Delegation.RouteLogged = true }))
}

// step is one phase of a run. Each step's method returns the step that follows it.
type step int

const (
	stepImplement    step = iota // an implementer turn that plans, implements, or fixes findings
	stepAnswer                   // an implementer turn that answers waiting PR comments
	stepPickUpReview             // verify a PR head the last run left unreviewed
	stepReview                   // resolve merge conflicts, then review the PR head or local commit
	stepOpenPR                   // push a cleanly reviewed local commit and open its PR
	stepHandover                 // read comments that arrived during the run
	stepFinalCheck               // supply the full-suite result a cleanly reviewed change needs
	stepDone
)

// firstStep is where a run starts once any conflict repair is done: waiting comments
// first, then a reviewed local commit that has no PR, then a verified PR head or a
// local commit that has no review, and otherwise an implementer turn.
func firstStep(rec *task.Record, waiting int) step {
	switch {
	case waiting > 0:
		return stepAnswer
	case rec.OpenPending():
		return stepOpenPR
	case rec.ReviewPending() && rec.LocalReview():
		return stepReview
	case rec.ReviewPending():
		return stepPickUpReview
	case rec.FinalCheckPending():
		return stepFinalCheck
	}
	return stepImplement
}

// taskRun is the state one RunTask carries between steps. ctx stops the agents;
// final writes results to the task file even after ctx is cancelled.
type taskRun struct {
	a          App
	ctx, final context.Context
	log        io.Writer
	rec        *task.Record
	findings   *task.Review // the latest review's findings, for the next fix turn
	feedback   []PRComment  // the comments the next stepAnswer answers
	rounds     int          // how many times this run has answered comments
	plan       issuePlan    // the plan on the issue, read before a planning turn
	scope      confirmedScope
	forcePlan  bool // an open PR entered this run without planning documents
	// selfPassed is set when this run's self-review just passed the change, so the
	// competing review of the same change follows without a second conflict check.
	selfPassed bool
}

func (r *taskRun) loop(next step) error {
	for next != stepDone {
		var stopped bool
		var err error
		next, stopped, err = r.scopeBoundary(next)
		if err != nil || stopped {
			return err
		}
		switch next {
		case stepImplement:
			next, err = r.implement()
		case stepAnswer:
			next, err = r.answer()
		case stepPickUpReview:
			next, err = r.pickUpReview()
		case stepReview:
			next, err = r.review()
		case stepOpenPR:
			next, err = r.openPR()
		case stepHandover:
			next, err = r.handover()
		case stepFinalCheck:
			next, err = r.finalCheck()
		default:
			return fmt.Errorf("unknown run step %d", next)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// scopeBoundary re-reads triage before every stage. A changed scope invalidates
// prior plan and code reviews; a removed label or unreadable source stops the run.
func (r *taskRun) scopeBoundary(next step) (step, bool, error) {
	scope, err := r.a.readScope(r.ctx, r.rec.Key)
	if err != nil {
		rec, saveErr := r.a.update(r.final, r.rec.Key, existing(func(cur *task.Record) { failTask(cur, err.Error(), r.a.Now()) }))
		r.rec = rec
		return stepDone, true, saveErr
	}
	r.scope = scope
	if r.rec.Delegation.ScopeDigest != scope.digest {
		clearSession := r.rec.Delegation.ScopeDigest != "" || r.rec.PlanPosted()
		rec, err := r.a.update(r.final, r.rec.Key, existing(func(cur *task.Record) {
			cur.Delegation.ScopeDigest = scope.digest
			cur.Delegation.PlanScopeDigest = ""
			cur.Delegation.PlanDigest = ""
			if clearSession {
				cur.Delegation.Implementer.ID = ""
			}
		}))
		if err != nil {
			return stepDone, true, err
		}
		r.rec = rec
	}
	if r.rec.PlanPosted() && r.rec.Delegation.PlanDigest != "" {
		plan, err := r.a.readIssuePlan(r.ctx, r.rec)
		if err != nil {
			rec, saveErr := r.a.update(r.final, r.rec.Key, existing(func(cur *task.Record) {
				failTask(cur, fmt.Sprintf("reading checked planning documents failed: %v", err), r.a.Now())
			}))
			r.rec = rec
			return stepDone, true, saveErr
		}
		if plan.complete() && r.rec.Delegation.PlanScopeDigest == scope.digest && legacyPlanDigest(plan) == r.rec.Delegation.PlanDigest {
			// The old four-document check still covers the same triage and comments.
			// Save the two-document digest without starting a new planning turn.
			rec, err := r.a.update(r.final, r.rec.Key, existing(func(cur *task.Record) {
				cur.Delegation.PlanDigest = planDigest(plan.texts())
				cur.Plan = slices.DeleteFunc(cur.Plan, func(c task.PlanComment) bool {
					return !slices.Contains(task.PlanDocs, c.Doc)
				})
			}))
			if err != nil {
				return stepDone, true, err
			}
			r.rec = rec
		} else if !plan.complete() || planDigest(plan.texts()) != r.rec.Delegation.PlanDigest {
			rec, err := r.a.update(r.final, r.rec.Key, existing(func(cur *task.Record) {
				cur.Delegation.PlanScopeDigest = ""
				cur.Delegation.PlanDigest = ""
				cur.Delegation.Implementer.ID = ""
			}))
			if err != nil {
				return stepDone, true, err
			}
			r.rec = rec
		}
	}
	if next != stepImplement && r.planNeeded() {
		return stepImplement, false, nil
	}
	return next, false, nil
}

func (r *taskRun) planNeeded() bool {
	return r.a.needsPlan(r.rec) || (r.forcePlan && !r.rec.PlanPosted())
}

// implement runs an implementer turn with the open findings, if any. A posted plan
// goes straight on to an implementation turn; a PR goes to review.
func (r *taskRun) implement() (step, error) {
	a := r.a
	if r.forcePlan && !r.rec.PlanPosted() && r.rec.Delegation.PlanDraft == nil && r.rec.Delegation.Implementer.ID != "" {
		rec, err := a.update(r.final, r.rec.Key, existing(func(cur *task.Record) {
			cur.Delegation.Implementer.ID = ""
		}))
		if err != nil {
			return stepDone, err
		}
		r.rec = rec
	}
	if r.planNeeded() {
		if err := r.enter(task.Planning); err != nil {
			return stepDone, err
		}
		if restored, next, err := r.restorePlanDraft(); restored || err != nil {
			return next, err
		}
		if ok, err := r.reconcilePlan(); err != nil || !ok {
			return stepDone, err
		}
	}
	during, stage := "implementation", task.Implementing
	req := turnRequest{findings: r.findings, forcePlan: r.forcePlan && !r.rec.PlanPosted()}
	if r.planNeeded() {
		during, stage = "planning", task.Planning
	}
	if err := r.enter(stage); err != nil {
		return stepDone, err
	}
	res := a.runTurn(r.ctx, r.rec, req, a.verifiedPR(r.log), r.log)
	if req.forcePlan && res.err == nil && res.turn.Outcome.Status != agent.Planned && res.turn.Outcome.Status != agent.Blocked && res.turn.Outcome.Status != agent.FailedRun {
		res.err = fmt.Errorf("planning turn returned %s instead of planned, blocked, or failed", res.turn.Outcome.Status)
	}
	rec, err := a.finishTurn(r.ctx, r.final, r.rec.Key, during, res, func(cur *task.Record) {
		applyTurnResult(cur, res, a.Now())
		if res.err == nil && res.turn.Outcome.Status == agent.Planned {
			cur.Delegation.PlanDraft = &task.PlanDraft{Scope: r.scope.digest, Fingerprint: r.plan.fingerprint, Documents: mergePlanTexts(r.plan.texts(), res.turn.Outcome.Documents), Sources: r.plan.texts()}
		}
		if res.err == nil && res.turn.Outcome.Status == agent.Checkpoint {
			cur.Delegation.CheckpointHead = res.checkpointHead
			cur.Delegation.CheckpointNext = res.turn.Outcome.NextObjective
			cur.Delegation.Checkpoints++
			cur.Delegation.Implementer.ID = ""
			cur.Validation = res.turn.Outcome.Validation
		}
	}, r.log)
	if err != nil {
		return stepDone, err
	}
	r.rec = rec
	if res.err == nil && res.turn.Outcome.Status == agent.Planned {
		if r.rec, err = a.postPlan(r.ctx, r.final, rec, res.turn.Outcome.Documents, r.plan, r.log); err != nil || r.rec.State != task.Planning {
			return stepDone, err
		}
		if r.rec.Delegation.PR != 0 {
			return stepReview, nil
		}
		return stepImplement, nil
	}
	if res.err == nil && res.turn.Outcome.Status == agent.Checkpoint {
		return stepImplement, nil
	}
	if res.err == nil && res.turn.Outcome.Status == agent.ReviewReady && rec.State == task.Implementing {
		return stepReview, nil
	}
	if !rec.State.Reviewing() {
		return stepDone, nil
	}
	return stepReview, nil
}

// answer runs an implementer turn on the waiting comments. A turn that pushed a
// change goes to the reviewer like any other change.
func (r *taskRun) answer() (step, error) {
	r.rounds++
	comments := r.feedback
	r.feedback = nil
	if err := r.enter(task.Implementing); err != nil {
		return stepDone, err
	}
	rec, err := r.a.answerFeedback(r.ctx, r.final, r.rec, comments, r.log)
	if err != nil {
		return stepDone, err
	}
	r.rec = rec
	switch {
	case !rec.State.Reviewing() && rec.State != task.WaitingForCI && rec.State != task.ReadyForHuman:
		// Blocked, failed, or stopped. The comments stay unanswered, so the next run
		// asks for them again.
		return stepDone, nil
	case rec.FinalCheckPending():
		return stepFinalCheck, nil
	case !rec.ReviewPending():
		// A turn that pushed nothing leaves the code its review already covers, so
		// there is nothing to review and the task goes back to the developer.
		return stepHandover, nil
	}
	return stepReview, nil
}

// pickUpReview takes up a verified PR whose head has no review. A PR that is no longer
// open can't be reviewed, so the implementer takes over.
func (r *taskRun) pickUpReview() (step, error) {
	a := r.a
	pr, err := a.verifyPR(r.ctx, r.rec, agent.Outcome{PRNumber: r.rec.Delegation.PR})
	if err != nil {
		fmt.Fprintf(r.log, "foreman: can't review PR #%d (%v); implementing instead\n", r.rec.Delegation.PR, err)
		return stepImplement, nil
	}
	rec, err := a.update(r.final, r.rec.Key, existing(func(cur *task.Record) {
		applyPRHead(cur, pr)
	}))
	if err != nil {
		return stepDone, err
	}
	r.rec = rec
	fmt.Fprintf(r.log, "foreman: PR #%d head %s has no review yet; reviewing\n", pr.Key.Number, short(pr.HeadSHA))
	return stepReview, nil
}

// review reviews the current change: the implementer's self-review until a clean one
// covers the head and description, then the competing review. Findings from either go
// back to the implementer; a clean self-review goes on to the competing review; a
// clean competing review hands over; anything else ends the run.
func (r *taskRun) review() (step, error) {
	a := r.a
	// Every change, including the first and one after a fix, is reviewed as a PR that
	// merges cleanly into its latest base. The check runs once per change, before its
	// self-review.
	rec := r.rec
	if !r.selfPassed {
		var ready bool
		var err error
		if rec, ready, err = a.resolveConflicts(r.ctx, r.final, r.rec, r.log); err != nil || !ready {
			return stepDone, err
		}
		r.rec = rec
	}
	r.selfPassed = false
	var err error
	title, body, reviewErr := a.reviewDescription(r.ctx, rec)
	var prd string
	if reviewErr == nil && rec.Delegation.ClaimVerification {
		prd, reviewErr = a.reviewPRD(r.ctx, rec)
		if reviewErr == nil && len(rec.Delegation.Claims) == 0 {
			reviewErr = errors.New("the change has no claim set to verify")
		}
	}
	// Keep PR text for audit and publication checks. Requirements changes still
	// invalidate code reviews even when the commit did not change.
	if reviewErr == nil {
		d, digest := rec.Delegation, task.ProposalDigest(title, body)
		setPR := !rec.LocalReview() && d.PRDigest != digest
		setPRD := d.ClaimVerification && d.PRDDigest != task.Digest(prd)
		if setPR || setPRD {
			if rec, err = a.update(r.final, rec.Key, existing(func(cur *task.Record) {
				if setPR {
					cur.Delegation.PRDigest = digest
				}
				if setPRD {
					cur.Delegation.PRDDigest = task.Digest(prd)
				}
			})); err != nil {
				return stepDone, err
			}
			r.rec = rec
		}
	}
	kind := rec.NextReview()
	reviewer := rec.Delegation.Reviewer
	if kind == task.SelfReview {
		reviewer = rec.Delegation.Implementer.Agent
	}
	if err := r.enter(rec.ReviewStage()); err != nil {
		return stepDone, err
	}
	rec = r.rec
	var review agent.ReviewOutcome
	switch {
	case reviewErr != nil:
	case kind == task.SelfReview:
		review, reviewErr = a.selfReview(r.ctx, rec, title, body, prd, r.log)
	default:
		review, reviewer, reviewErr = a.review(r.ctx, rec, title, body, prd, r.log)
	}
	if reviewErr == nil {
		if rec.Delegation.ClaimVerification {
			_, reviewErr = a.reviewPRD(r.ctx, rec)
		}
	}
	if reviewErr == nil {
		_, _, reviewErr = a.reviewRequirements(r.ctx, rec)
	}
	// Every fix is reviewed; the developer is asked only once fix_rounds fixes, requested
	// by either reviewer, still leave findings. A self-review at the limit is diagnosed
	// first, and the review, the diagnosis, and the question are written together, so a
	// restart reviews again rather than asking without a diagnosis.
	atLimit := reviewErr == nil && !review.Clean && rec.ChangeRequestsSinceDecision()+1 > a.fixRounds()
	var diagnosis *task.Diagnosis
	var diagnosisUsage task.Usage
	if atLimit && kind == task.SelfReview {
		var d task.Diagnosis
		d, diagnosisUsage = a.diagnoseLimit(r.ctx, rec, title, body, review.Findings, r.log)
		diagnosis = &d
	}
	if rec, err = a.update(r.final, rec.Key, existing(func(cur *task.Record) {
		if kind == task.CompetingReview {
			cur.Delegation.Reviewer = reviewer
		}
		applyReview(cur, kind, reviewer, review, reviewErr, a.Now())
		if reviewErr != nil {
			applyStop(r.ctx, cur, "review", r.log, a.Now())
		}
		if !a.metered {
			addUsage(&cur.Usage, review.Usage)
			addUsage(&cur.Usage, diagnosisUsage)
		}
		// Findings put the task back to IMPLEMENTING; only they can reach the limit.
		if atLimit && cur.State == task.Implementing && cur.ChangeRequestsSinceDecision() > a.fixRounds() {
			if kind == task.SelfReview {
				escalateSelfReview(cur, a.fixRounds(), *diagnosis, a.Now())
			} else {
				escalateReview(cur, a.fixRounds(), a.Now())
			}
		}
		holdForFinalCheck(cur)
	})); err != nil {
		return stepDone, err
	}
	r.rec = rec
	switch {
	case kind == task.SelfReview && rec.SelfReviewPassed() && rec.State == task.SelfReviewing:
		r.selfPassed = true
		return stepReview, nil
	case rec.State == task.Implementing && rec.FinalCheckPending():
		return stepFinalCheck, nil
	case rec.State == task.OpeningPR && rec.OpenPending():
		return stepOpenPR, nil
	case rec.State == task.Implementing:
		r.findings = rec.LatestReview()
		return stepImplement, nil
	case reviewErr == nil && review.Clean && rec.State == task.WaitingForCI && a.ReplyToPRFeedback:
		return stepHandover, nil
	}
	return stepDone, nil
}

// handover is the run's last chance to answer comments that arrived while it
// worked, before the gate hands the task to the developer.
func (r *taskRun) handover() (step, error) {
	feedback, rec, err := r.a.beforeHandover(r.ctx, r.final, r.rec, r.rounds, r.log)
	if err != nil || len(feedback) == 0 {
		return stepDone, err
	}
	r.rec, r.feedback = rec, feedback
	return stepAnswer, nil
}

// enter records the stage the run is about to take its next turn in, before the turn
// starts, so the task file and the dashboard show it while it runs and after a restart.
func (r *taskRun) enter(stage task.State) error {
	if r.rec.State == stage {
		return nil
	}
	rec, err := r.a.update(r.final, r.rec.Key, existing(func(cur *task.Record) { cur.State = stage }))
	if err != nil {
		return err
	}
	r.rec = rec
	return nil
}

// errTaskRemoved stops a runner whose task file was deleted while it ran.
var errTaskRemoved = errors.New("the task file was removed; stopping")

// errPRClosed stops a runner whose PR was closed while it ran. The gate records
// PR_CLOSED before it stops the runner, so the stop that follows is foreman's doing,
// not a failure of the agent's, and recording it would tell the developer their run
// failed when they closed the PR themselves.
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

// needsPlan is true until every planning document is on the issue. A task that
// already has a PR is handled by taskRun.forcePlan at the start of its next run.
func (a App) needsPlan(rec *task.Record) bool {
	return (!rec.PlanPosted() && rec.Delegation.PR == 0) ||
		(rec.PlanPosted() && rec.Delegation.ScopeDigest != "" && rec.Delegation.PlanScopeDigest != rec.Delegation.ScopeDigest)
}

// implement runs one implementer turn. It resumes the session with a conflict repair,
// an answered decision, or the work in req when there is one, and otherwise (or when
// the session is gone) starts a fresh turn whose prompt is rebuilt from the task file.
func (a App) implement(ctx context.Context, rec *task.Record, req turnRequest, log io.Writer) (agent.Turn, error) {
	d := rec.Delegation
	findings, feedback, merge := req.findings, req.feedback, req.merge
	adapter, ok := a.Agents[d.Implementer.Agent]
	if !ok {
		return agent.Turn{}, fmt.Errorf("agent %s is not configured", d.Implementer.Agent)
	}
	planning := a.needsPlan(rec) || req.forcePlan
	local := d.BeforePR() && !planning
	stage, step := task.StageImplement, agent.StepImplement
	switch {
	case planning:
		stage, step = task.StagePlan, agent.StepPlan
	case merge != nil || d.ConflictPending:
		step = agent.StepMerge
	case len(feedback) > 0:
		step = agent.StepFeedback
	}
	in := agent.StartInput{Worktree: d.Worktree, Log: log, MaxBudgetUSD: a.ImplementerBudgetUSD, Stage: stage, Step: step, Profile: d.Profile}
	if req.finalCheck != nil {
		in.Purpose = "final_check"
	} else if !planning && findings != nil {
		in.Purpose = "correction"
	}
	if req.openBodyFile != "" {
		in.Purpose = "open_pr"
		return a.openTurn(ctx, rec, adapter, in, req.openBodyFile, log)
	}
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
		resume = agent.ResumeMessage(decs, open, planning, local)
	} else if req.finalCheck != nil {
		resume = agent.FinalCheckMessage(*req.finalCheck)
	} else if merge != nil && local {
		resume = agent.LocalMergeMessage(d.Branch, merge.Base, merge.Files)
	} else if merge != nil {
		resume = agent.MergeMessage(d.Branch, merge.Base, merge.Files)
	} else if len(feedback) > 0 {
		resume = agent.FeedbackMessage(rec.Repo, d.PR, feedbackItems(feedback))
	} else if findings != nil && local {
		resume = agent.LocalFindingsMessage(*findings)
	} else if findings != nil {
		resume = agent.FindingsMessage(*findings)
	}
	if resume != "" && d.ClaimVerification && !planning {
		resume += agent.ClaimsMessage
	}
	if resume != "" && !planning {
		resume = fullSuiteResume(rec, req, resume)
	}
	if resume != "" && (findings == nil || len(rec.Undelivered()) > 0) && d.Implementer.ID != "" {
		if err := a.startAttempt(ctx, rec); err != nil {
			return agent.Turn{}, err
		}
		fmt.Fprintf(log, "foreman: resuming %s session %s\n", adapter.Name(), d.Implementer.ID)
		in.Prompt = resume
		turn, err := adapter.Resume(ctx, d.Implementer, in)
		if !errors.Is(err, agent.ErrNoSession) {
			return turn, err
		}
		fmt.Fprintf(log, "foreman: session is gone (%v); starting a fresh turn rebuilt from the task file\n", err)
	}
	brief, err := a.brief(ctx, rec, log)
	if err != nil {
		return agent.Turn{}, err
	}
	// The turn that pushed before a merge already fixed the latest findings.
	if latest := rec.LatestReview(); merge == nil && latest != nil && !latest.Clean {
		brief.OpenFindings = latest.Findings
	}
	brief.Correction = !planning && merge == nil && !d.ConflictPending && len(feedback) == 0 && req.finalCheck == nil && len(brief.OpenFindings) > 0
	if brief.Correction {
		brief.CorrectionAnswers = rec.Undelivered()
	}
	brief.Merge = merge
	brief.LocalReview = d.BeforePR()
	brief.Claims = d.ClaimVerification
	brief.FullSuite, brief.FinalCheck = d.FullSuiteGate, req.finalCheck
	brief.FocusedOnly = d.FullSuiteGate && !needsFullResult(rec, req)
	brief.PR, brief.Feedback, brief.Conflict = d.PR, feedbackItems(feedback), conflict
	if d.PR == 0 && d.CheckpointNext != "" {
		brief.CheckpointHead, brief.NextObjective = d.CheckpointHead, d.CheckpointNext
	}
	if !planning && !brief.Correction && rec.PlanPosted() {
		_, brief.PlanText, err = a.reviewRequirements(ctx, rec)
		if err != nil {
			return agent.Turn{}, err
		}
	}
	// The repo layer comes from the worktree: the committed .foreman/ on the base.
	if planning {
		plan, err := a.Stages.Resolve(d.Worktree, task.StagePlan)
		if err != nil {
			return agent.Turn{}, err
		}
		brief.Planning, brief.PlanInstructions = true, plan.Content()
		fmt.Fprintf(log, "foreman: starting %s in %s to write the plan\n", adapter.Name(), d.Worktree)
	} else {
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
		fmt.Fprintf(log, "foreman: starting %s in %s\n", adapter.Name(), d.Worktree)
	}
	in.Prompt = agent.ImplementationPrompt(brief)
	if err := a.startAttempt(ctx, rec); err != nil {
		return agent.Turn{}, err
	}
	return adapter.Start(ctx, in, func(s task.Session) {
		_, _ = a.update(ctx, rec.Key, existing(func(cur *task.Record) {
			cur.Delegation.Implementer = s
		}))
	})
}

// startAttempt stamps the attempt's first agent turn just before the agent starts, so
// routing, conflict checks, and turn preparation before it do not count as work. The
// agent does not start unless the stamp is saved: a PR whose first turn went
// unrecorded would show no timing at all.
func (a App) startAttempt(ctx context.Context, rec *task.Record) error {
	if at := rec.CurrentAttempt(); at == nil || !at.StartedAt.IsZero() {
		return nil
	}
	now := a.Now()
	if _, err := a.update(ctx, rec.Key, existing(func(cur *task.Record) {
		if at := cur.CurrentAttempt(); at != nil && at.StartedAt.IsZero() {
			at.StartedAt = now
		}
	})); err != nil {
		return fmt.Errorf("record the attempt's first turn: %w", err)
	}
	rec.CurrentAttempt().StartedAt = now
	return nil
}

// answerFeedback runs one turn that answers the comments it is given. The PR head
// before the turn is what tells a reply-only turn from one that changed code.
func (a App) answerFeedback(ctx, final context.Context, rec *task.Record, comments []PRComment, log io.Writer) (*task.Record, error) {
	d := rec.Delegation
	headBefore := d.PRHead
	fmt.Fprintf(log, "foreman: %s\n", feedbackNote(len(comments), d.PR))
	res := a.runTurn(ctx, rec, turnRequest{feedback: comments}, a.verifiedPR(log), log)
	return a.finishTurn(ctx, final, rec.Key, "answering PR feedback", res, func(cur *task.Record) {
		// The result is stored first, so the handover below judges coverage by it.
		recordFull(cur, res)
		applyFeedbackTurn(cur, res.turn, res.pr, res.err, comments, headBefore, a.Now())
	}, log)
}

// applyFeedbackTurn records a turn that answered comments on the pull request. The
// comments it was given count as answered only once the turn finished, so a run that
// failed or asked a question leaves them for the next one. A turn that pushed nothing
// leaves the code its review already covers, so the task goes straight back to the
// developer with its CI result; a turn that pushed goes through the review and CI flow
// like any other change.
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
	if strings.TrimSpace(pr.Body) == "" {
		failTask(rec, "the pull request description is empty; restore it before handoff", now)
		return
	}
	d := rec.Delegation
	// The turn's PR is the newest read of the description. Storing it makes an edit
	// made while the agent answered need its reviews before the run hands over.
	d.PRDigest = task.ProposalDigest(pr.Title, pr.Body)
	rec.State = rec.ReviewStage()
	// The same bar the gate applies: a clean review of this head and CI that finished.
	// A clean review alone waits for CI, and the gate hands the task over.
	if !reviewCovers(rec, pr.HeadSHA) {
		return
	}
	rec.State = task.WaitingForCI
	if handsOverNow(rec, ci) {
		rec.State = task.ReadyForHuman
		rec.LastOutcome = &task.Outcome{Status: "feedback_answered",
			Summary: fmt.Sprintf("answered %d %s on PR #%d; no change was needed", len(comments), plural(len(comments), "comment", "comments"), d.PR), At: now}
	}
}

// reviewDescription checks that the PR has text and reads it for audit.
// Code reviews receive checked requirements and code, rather than this text.
func (a App) reviewDescription(ctx context.Context, rec *task.Record) (title, body string, err error) {
	d, target := rec.Delegation, reviewTarget(rec)
	if rec.LocalReview() {
		title, body = d.ProposedTitle, d.ProposedBody
	} else {
		pr, err := a.GitHub.GetPullRequest(ctx, task.Key{Repo: rec.Repo, Number: d.PR})
		if err != nil {
			return "", "", fmt.Errorf("review %s: %w", target, err)
		}
		title, body = pr.Title, pr.Body
	}
	if strings.TrimSpace(body) == "" {
		return "", "", fmt.Errorf("review %s: the pull request description is empty", target)
	}
	return title, body, nil
}

// reviewPRD is the current text of the PRD comment on the issue, which a review of a
// task with claim verification judges the change against. A PRD that is gone, lost its
// marker, or is empty stops the review: errPRDMissing names the cause. Planning posts
// the PRD before any change exists, so a missing one is never waited for.
func (a App) reviewPRD(ctx context.Context, rec *task.Record) (string, error) {
	c := rec.PlanComment(task.PRD)
	if c == nil || (c.CommentID == 0 && c.ExternalCommentID == "") {
		return "", fmt.Errorf("%w: the task has no PRD comment on %s", errPRDMissing, issueLabel(rec.Key, rec.Identifier))
	}
	id := c.ExternalCommentID
	if rec.IssueSource() != task.Linear {
		id = strconv.FormatInt(c.CommentID, 10)
	}
	got, err := a.issueComment(ctx, rec.Key, id)
	switch {
	case errors.Is(err, ErrGone):
		return "", fmt.Errorf("%w: the PRD comment %s was deleted", errPRDMissing, c.URL)
	case err != nil:
		return "", fmt.Errorf("load the PRD comment %s: %w", c.URL, err)
	}
	if doc, _, ok := task.ParsePlanMarker(got.Body); !ok || doc != task.PRD {
		return "", fmt.Errorf("%w: the comment %s is no longer marked as the PRD", errPRDMissing, c.URL)
	}
	text := strings.ReplaceAll(planText(task.PRD, got.Body), "\r\n", "\n")
	if text == "" {
		return "", fmt.Errorf("%w: the PRD comment %s is empty", errPRDMissing, c.URL)
	}
	return text, nil
}

// errPRDMissing is a PRD the issue no longer holds, as opposed to one that could not
// be read this time.
var errPRDMissing = errors.New("no PRD to review against")

// reviewTarget names the change under review for the log.
func reviewTarget(rec *task.Record) string {
	if rec.LocalReview() {
		return "the local branch " + rec.Delegation.Branch
	}
	return fmt.Sprintf("PR #%d", rec.Delegation.PR)
}

// selfReview runs the implementing agent's review in a fresh, read-only session. It
// reads the description, confirmed scope, checked plan, and code, without the
// implementer's session, PR comments, or earlier findings. It never falls back to another
// agent, so an error stops the task instead of skipping to the competing review.
func (a App) selfReview(ctx context.Context, rec *task.Record, title, body, prd string, log io.Writer) (agent.ReviewOutcome, error) {
	d := rec.Delegation
	_, plan, err := a.reviewRequirements(ctx, rec)
	if err != nil {
		return agent.ReviewOutcome{}, err
	}
	adapter, ok := a.Agents[d.Implementer.Agent]
	if !ok {
		return agent.ReviewOutcome{}, fmt.Errorf("agent %s is not configured", d.Implementer.Agent)
	}
	instructions, err := a.Stages.Resolve(d.Worktree, task.StageReview)
	if err != nil {
		return agent.ReviewOutcome{}, err
	}
	brief := agent.ReviewBrief{Key: rec.Key, Branch: d.Branch, BaseBranch: strings.TrimPrefix(d.BaseRef, "origin/"),
		StageInstructions: instructions.Content(), Passes: a.reviewRounds(), Self: true,
		Claimed: d.ClaimVerification, PRD: prd, Claims: d.Claims, Plan: plan}
	if err := a.startAttempt(ctx, rec); err != nil {
		return agent.ReviewOutcome{}, err
	}
	brief = a.prepareReview(ctx, rec, task.SelfReview, d.Implementer.Agent, brief, log)
	in := agent.StartInput{Worktree: d.Worktree, Log: log, MaxBudgetUSD: a.ReviewerBudgetUSD, Prompt: agent.ReviewPrompt(brief), Step: agent.StepReview, Purpose: "self_review"}
	fmt.Fprintf(log, "foreman: %s self-reviewing %s (round %d)\n", adapter.Name(), reviewTarget(rec), len(rec.Reviews)+1)
	out, reviewErr := adapter.Review(ctx, in)
	return reviewContext(out, brief), reviewErr
}

// review runs the competing agent's fresh, read-only session. When the competing
// agent is unavailable, the implementer's agent reviews in a separate session.
func (a App) review(ctx context.Context, rec *task.Record, title, body, prd string, log io.Writer) (agent.ReviewOutcome, task.Agent, error) {
	d := rec.Delegation
	_, _, requirementsErr := a.reviewRequirements(ctx, rec)
	if requirementsErr != nil {
		return agent.ReviewOutcome{}, d.Reviewer, requirementsErr
	}
	chosen := d.Reviewer
	if chosen != d.Implementer.Agent && a.Quota != nil {
		if quotas, err := a.Quota.Quota(ctx); err == nil {
			status := quotas[chosen].Status
			own := quotas[d.Implementer.Agent].Status
			if (status == QuotaNotSetUp || status == QuotaExhausted) && own != QuotaNotSetUp && own != QuotaExhausted {
				fmt.Fprintf(log, "foreman: %s unavailable according to quota-axi; using %s for review\n", chosen, d.Implementer.Agent)
				chosen = d.Implementer.Agent
			}
		}
	}
	reviewer, ok := a.Agents[chosen]
	if !ok {
		return agent.ReviewOutcome{}, chosen, fmt.Errorf("reviewer %s is not configured", chosen)
	}
	// Scope and plan are verified before this fresh diff-based review. Claim
	// verification explicitly adds the PRD and claims.
	target := reviewTarget(rec)
	instructions, err := a.Stages.Resolve(d.Worktree, task.StageReview)
	if err != nil {
		return agent.ReviewOutcome{}, chosen, err
	}
	brief := agent.ReviewBrief{Key: rec.Key, Branch: d.Branch, BaseBranch: strings.TrimPrefix(d.BaseRef, "origin/"),
		StageInstructions: instructions.Content(), Passes: a.reviewRounds(),
		Claimed: d.ClaimVerification, PRD: prd, Claims: d.Claims}
	if err := a.startAttempt(ctx, rec); err != nil {
		return agent.ReviewOutcome{}, chosen, err
	}
	in := agent.StartInput{
		Worktree:     d.Worktree,
		Log:          log,
		MaxBudgetUSD: a.ReviewerBudgetUSD,
		Prompt:       agent.ReviewPrompt(brief),
		Purpose:      "competing_review",
		Step:         agent.StepReview,
	}
	fmt.Fprintf(log, "foreman: %s reviewing %s (round %d)\n", reviewer.Name(), target, len(rec.Reviews)+1)
	brief = a.prepareReview(ctx, rec, task.CompetingReview, chosen, brief, log)
	in.Prompt = agent.ReviewPrompt(brief)
	out, err := reviewer.Review(ctx, in)
	if errors.Is(err, agent.ErrUnavailable) && chosen != d.Implementer.Agent {
		chosen = d.Implementer.Agent
		reviewer, ok = a.Agents[chosen]
		if !ok {
			return agent.ReviewOutcome{}, chosen, fmt.Errorf("reviewer %s is not configured", chosen)
		}
		fmt.Fprintf(log, "foreman: %s unavailable; using %s for review\n", d.Reviewer, chosen)
		fmt.Fprintf(log, "foreman: %s reviewing %s (round %d)\n", reviewer.Name(), target, len(rec.Reviews)+1)
		brief = a.prepareReview(ctx, rec, task.CompetingReview, chosen, brief, log)
		in.Prompt = agent.ReviewPrompt(brief)
		out, err = reviewer.Review(ctx, in)
	}
	return reviewContext(out, brief), chosen, err
}

func (a App) reviewRequirements(ctx context.Context, rec *task.Record) (confirmedScope, string, error) {
	scope, err := a.readScope(ctx, rec.Key)
	if err != nil {
		return confirmedScope{}, "", err
	}
	if scope.digest != rec.Delegation.ScopeDigest {
		return confirmedScope{}, "", errors.New("confirmed triage changed before review; restart to recheck planning and implementation")
	}
	if !rec.PlanPosted() {
		return scope, "", nil
	}
	plan, err := a.readIssuePlan(ctx, rec)
	if err != nil {
		return confirmedScope{}, "", fmt.Errorf("reading planning documents for review: %w", err)
	}
	if !plan.complete() || planDigest(plan.texts()) != rec.Delegation.PlanDigest || rec.Delegation.PlanScopeDigest != scope.digest {
		return confirmedScope{}, "", errors.New("planning documents changed or have not been checked against current triage; restart to reconcile them")
	}
	return scope, "## Requirements\n\n" + plan.found[task.PRD].text + "\n\n## Work plan\n\n" + plan.found[task.ImplementationPlan].text, nil
}

func addUsage(total *task.Usage, turn task.Usage) {
	total.ClaudeInputTokens += turn.ClaudeInputTokens
	total.ClaudeCachedTokens += turn.ClaudeCachedTokens
	total.ClaudeOutputTokens += turn.ClaudeOutputTokens
	total.ClaudeCostUSD += turn.ClaudeCostUSD
	total.CodexInputTokens += turn.CodexInputTokens
	total.CodexCachedTokens += turn.CodexCachedTokens
	total.CodexOutputTokens += turn.CodexOutputTokens
	total.CodexReasoningTokens += turn.CodexReasoningTokens
	total.ClaudeTurns += turn.ClaudeTurns
	total.CodexTurns += turn.CodexTurns
	for name, n := range turn.ToolCalls {
		if total.ToolCalls == nil {
			total.ToolCalls = map[string]int{}
		}
		total.ToolCalls[name] += n
	}
}

// update is Store.Update plus the developer's notification: a task that moves into
// a state that needs them announces it, whichever command or runner moved it. It also
// marks a phase change on the task's attempt in the same write.
func (a App) update(ctx context.Context, key task.Key, fn func(cur *task.Record) (*task.Record, error)) (*task.Record, error) {
	var before task.State
	rec, err := a.Store.Update(ctx, key, func(cur *task.Record) (*task.Record, error) {
		if cur != nil {
			before = cur.State
		}
		next, err := fn(cur)
		if err == nil && next != nil {
			next.Track(a.Now())
		}
		return next, err
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
	switch rec.State {
	case task.NeedsDecision, task.Failed, task.ReadyForHuman, task.Reviewed:
	default:
		return
	}
	alert := Alert{State: rec.State, Key: key, URL: rec.URL}
	alert.Cancelled = rec.State == task.Failed && rec.LastOutcome != nil && rec.LastOutcome.Status == Cancelled
	if rec.Delegation != nil && rec.Delegation.PRURL != "" {
		alert.URL = rec.Delegation.PRURL
	}
	_ = a.Notifier.Notify(alert)
}

// brief gathers the durable task context the implementation prompt is built from.
func (a App) brief(ctx context.Context, rec *task.Record, log io.Writer) (agent.TaskBrief, error) {
	d := rec.Delegation
	issue, err := a.issue(ctx, rec.Key)
	if err != nil {
		return agent.TaskBrief{}, err
	}
	b := agent.TaskBrief{
		Key:        rec.Key,
		Identifier: issue.Identifier,
		Title:      issue.Title,
		URL:        issue.URL,
		Branch:     d.Branch,
		BaseBranch: strings.TrimPrefix(d.BaseRef, "origin/"),
		Worktree:   d.Worktree,
		Decisions:  rec.Undelivered(),
	}
	if !a.needsPlan(rec) {
		return b, nil
	}
	scope, err := a.readScope(ctx, rec.Key)
	if err != nil {
		return agent.TaskBrief{}, err
	}
	b.IssueBody, b.ConfirmedScope = issue.Body, scope.text
	return b, nil
}

// Prompts carry at most this much repository knowledge, section heading and notice
// included, so learnings never crowd out the issue and the developer's instructions.
const (
	promptLearnings = 8
	promptKnowledge = 2000
)

// repoKnowledge is the repository's learnings that best match the issue, as Markdown
// bullets. Knowledge is advisory, so a read error only drops it from the prompt.
func (a App) repoKnowledge(repo task.Repo, issue Issue, log io.Writer) string {
	if a.Knowledge == nil {
		return ""
	}
	query := strings.Join(append([]string{issue.Title, issue.Body}, issue.Labels...), "\n")
	// knowledgeSection adds the intro and one trailing newline to the bullets.
	ls, err := a.Knowledge.Select(string(repo), query, promptLearnings, promptKnowledge-len(agent.KnowledgeIntro)-1)
	if err != nil {
		fmt.Fprintf(log, "foreman: starting without repository knowledge: %v\n", err)
		return ""
	}
	var s strings.Builder
	for _, l := range ls {
		s.WriteString("- " + l.Text + "\n")
	}
	return s.String()
}

// verifyPR checks the agent's pr_opened claim against GitHub: the PR must be open,
// in this repo, from the task branch into the task's base. Without a number, the
// open PR for the branch is looked up. The task's own PR may target another base,
// because a maintainer can retarget it; the conflict check then follows it.
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
		failTask(rec, runErr.Error(), now)
		return
	}
	// The agent acted on the branch, so the gate's last CI result no longer applies.
	rec.CI = nil
	markDelivered(rec, now)
	o := turn.Outcome
	rec.LastOutcome = &task.Outcome{Status: string(o.Status), Summary: o.Summary, At: now}
	switch o.Status {
	case agent.PROpened:
		rec.Delegation.PR, rec.Delegation.PRURL, rec.Delegation.PRHead = pr.Key.Number, pr.URL, pr.HeadSHA
		rec.Delegation.CheckpointNext = ""
		rec.Validation = o.Validation
		rec.State = rec.ReviewStage()
	case agent.Checkpoint, agent.ReviewReady:
		// The step that follows enters its own stage before its turn.
		rec.State = task.Implementing
	case agent.Blocked:
		rec.State = task.NeedsDecision
		for _, q := range o.Questions {
			rec.Decisions = append(rec.Decisions, task.Decision{AskedAt: now, Question: q})
		}
	case agent.FailedRun:
		rec.State = task.Failed
	}
	// A fix returns the full claim set, so it replaces the stored one whole.
	if finishesChange(o.Status) && rec.Delegation.ClaimVerification && len(o.Claims) > 0 {
		rec.Delegation.Claims = o.Claims
	}
}

// markDelivered records that every answered decision reached the implementer.
func markDelivered(rec *task.Record, now time.Time) {
	for i := range rec.Decisions {
		if d := &rec.Decisions[i]; d.Answer != "" && d.DeliveredAt.IsZero() {
			d.DeliveredAt = now
		}
	}
}

// applyTurnResult is applyTurn for a checked turn. A review_ready turn stores its
// verified commit with the proposed PR in the same write, so the reviewer always
// reads the description the commit came with.
func applyTurnResult(rec *task.Record, res turnResult, now time.Time) {
	applyTurn(rec, res.turn, res.pr, res.err, now)
	recordFull(rec, res)
	if res.err != nil || res.turn.Outcome.Status != agent.ReviewReady {
		return
	}
	o, d := res.turn.Outcome, rec.Delegation
	d.LocalHead, d.ProposedTitle, d.ProposedBody = res.localHead, o.PRTitle, o.PRBody
	d.CheckpointNext = ""
	rec.Validation = o.Validation
}

// Cancelled is the outcome status of a run stopped by a cancel or a signal.
const Cancelled = "cancelled"

// applyStop marks a step that failed because the runner was told to stop. Its error
// is only the agent being killed, so the outcome says the run was cancelled instead.
func applyStop(ctx context.Context, rec *task.Record, during string, log io.Writer, now time.Time) {
	if ctx.Err() == nil {
		return
	}
	fmt.Fprintf(log, "foreman: stopped during %s: %v\n", during, context.Cause(ctx))
	rec.State = task.Failed
	rec.LastOutcome = &task.Outcome{Status: Cancelled, Summary: fmt.Sprintf("run cancelled during %s (%v)", during, context.Cause(ctx)), At: now}
}

// applyPRHead records the PR head a resumed run is about to review.
func applyPRHead(rec *task.Record, pr *PullRequest) {
	rec.CI = nil
	rec.Delegation.PRHead = pr.HeadSHA
	rec.State = rec.ReviewStage()
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// applyReview records a review round. Findings send the task back to IMPLEMENTING. A
// clean self-review leaves the next review to enter its stage. A clean competing review
// of a local commit goes on to OPENING_PR, and of a PR waits for the readiness gate
// (gate.go) in WAITING_FOR_CI.
// A self-review's error fails the task too: it never falls through to the competing
// review. The round records the head and description it read, and for a task with
// claim verification the claim set and PRD, which is what later decides whether it
// still covers the change.
func applyReview(rec *task.Record, kind task.ReviewKind, reviewer task.Agent, r agent.ReviewOutcome, reviewErr error, now time.Time) {
	name := "review"
	if kind == task.SelfReview {
		name = "self-review"
	}
	if reviewErr != nil {
		failTask(rec, fmt.Sprintf("%s %s failed: %v", reviewer, name, reviewErr), now)
		return
	}
	head, proposal := rec.CurrentChange()
	review := task.Review{
		BaseRef: r.BaseRef, BaseHead: r.BaseHead, FromHead: r.FromHead,
		Round:    len(rec.Reviews) + 1,
		Kind:     kind,
		Reviewer: reviewer,
		PR:       rec.Delegation.PR,
		Head:     head,
		Proposal: proposal,
		Clean:    r.Clean,
		Summary:  r.Summary,
		Findings: r.Findings,
		At:       now,
		Scope:    rec.Delegation.ScopeDigest,
		Plan:     rec.Delegation.PlanDigest,
	}
	if d := rec.Delegation; d.ClaimVerification {
		review.Claims, review.PRD, review.Limitations = task.ClaimsDigest(d.Claims), d.PRDDigest, r.Limitations
	}
	rec.Reviews = append(rec.Reviews, review)
	if r.Clean {
		status := "review_clean"
		if kind == task.SelfReview {
			status = "self_review_clean"
		}
		rec.LastOutcome = &task.Outcome{Status: status, Summary: r.Summary, At: now}
		switch {
		case kind == task.SelfReview:
		case rec.LocalReview():
			rec.State = task.OpeningPR
		default:
			rec.State = task.WaitingForCI
		}
		return
	}
	rec.State = task.Implementing
	rec.Delegation.Implementer.ID = ""
	rec.LastOutcome = &task.Outcome{Status: "changes_requested", Summary: fmt.Sprintf("%d findings: %s", len(r.Findings), r.Summary), At: now}
}

// escalateReview asks the developer how to proceed when the review budget is spent.
// The answer goes to the implementer like any decision, and resets the budget.
// A task reviewed before its PR has no PR to accept findings on, so its B stops the
// task instead, and the PR opens only after a clean review.
func escalateReview(rec *task.Record, rounds int, now time.Time) {
	latest := rec.LatestReview()
	local := rec.LocalReview()
	subject := fmt.Sprintf("PR #%d", rec.Delegation.PR)
	if local {
		subject = "the local change"
	}
	var ctx strings.Builder
	fmt.Fprintf(&ctx, "%s re-reviewed %s after %s of fixes without a clean result. Latest findings, not yet addressed:", rec.Delegation.Reviewer, subject, roundsText(rounds))
	recommend := "B"
	for _, f := range latest.Findings {
		fmt.Fprintf(&ctx, "\n- [%s] %s", f.Severity, f.Summary)
		if f.Severity == "blocking" {
			recommend = "A"
		}
	}
	options := []task.Option{
		{Label: "A", Description: "Address the remaining findings in another round"},
		{Label: "B", Description: "Leave the code as is; treat the remaining findings as accepted"},
	}
	impact := "A spends more agent time on disputed findings; B sends the remaining findings to your review unfixed."
	if local {
		options[1], recommend = stopBeforePROption, "A"
		ctx.WriteString("\nNo pull request is open. It opens only after a clean review.")
		impact = "A spends more agent time on disputed findings; B ends the task without a pull request."
	}
	rec.State = task.NeedsDecision
	rec.LastOutcome = &task.Outcome{Status: "blocked", Summary: "review not clean after " + roundsText(rounds), At: now}
	rec.Decisions = append(rec.Decisions, task.Decision{AskedAt: now, Question: task.Question{
		Issue:          fmt.Sprintf("The reviewer still requests changes after %s of fixes. How should %s proceed?", roundsText(rounds), rec.Delegation.Implementer.Agent),
		Context:        ctx.String(),
		Options:        options,
		Recommendation: recommend,
		ImpactIfWrong:  impact,
	}})
}

// escalateSelfReview asks the developer how to proceed when the implementer's own
// review still has findings at the fix limit. An unclean self-review is never accepted,
// so the choices are another round or a stop; a verified conflict in the implementer's
// context adds a choice of which statement governs.
func escalateSelfReview(rec *task.Record, rounds int, diag task.Diagnosis, now time.Time) {
	subject := fmt.Sprintf("PR #%d", rec.Delegation.PR)
	stop := stopAtLimitOption
	if rec.LocalReview() {
		subject, stop = "the local change", stopBeforePROption
	}
	implementer := rec.Delegation.Implementer.Agent
	var ctx strings.Builder
	fmt.Fprintf(&ctx, "%s self-reviewed %s after %s of fixes without a clean result. Latest findings, not yet addressed:", implementer, subject, roundsText(rounds))
	for _, f := range rec.LatestReview().Findings {
		fmt.Fprintf(&ctx, "\n- [%s] %s", f.Severity, f.Summary)
	}
	another := task.Option{Label: "A", Description: "Address the remaining findings in another round"}
	options := []task.Option{another}
	impact := "A spends more agent time on findings the implementer has not fixed; B ends the task with them unresolved."
	switch diag.Kind {
	case task.DiagnosedConflict:
		ctx.WriteString("\n\nThe context given to the implementer contradicts itself:")
		var first, second []string
		for i, c := range diag.Conflicts {
			fmt.Fprintf(&ctx, "\n%d. %s\n   - %s: %q\n   - %s: %q", i+1, c.Point, c.First.Source, c.First.Quote, c.Second.Source, c.Second.Quote)
			first = append(first, fmt.Sprintf("%s: %q", c.First.Source, c.First.Quote))
			second = append(second, fmt.Sprintf("%s: %q", c.Second.Source, c.Second.Quote))
		}
		options = []task.Option{
			{Label: "A", Description: "Follow the first statement (" + strings.Join(first, "; ") + ") and reconcile the code, plan, and description with it"},
			{Label: "B", Description: "Follow the second statement (" + strings.Join(second, "; ") + ") and reconcile the code, plan, and description with it"},
			{Label: "C", Description: "Address the remaining findings in another round without choosing between the statements"},
		}
		impact = "A or B settles the conflict the wrong way if the other statement governs; C may stall again on the same conflict; D ends the task with the findings unresolved."
	case task.DiagnosedPracticeGap:
		ctx.WriteString("\n\nThe findings look like a missed generic practice, not a conflict in the implementer's context. They go to learning.")
	default:
		ctx.WriteString("\n\nNo concrete conflict was found in the context given to the implementer.")
	}
	if diag.Summary != "" {
		ctx.WriteString(" " + diag.Summary)
	}
	if rec.LocalReview() {
		ctx.WriteString("\nNo pull request is open. It opens only after a clean review.")
	}
	stop.Label = string(rune('A' + len(options)))
	options = append(options, stop)
	rec.State = task.NeedsDecision
	rec.LastOutcome = &task.Outcome{Status: "blocked", Summary: "self-review not clean after " + roundsText(rounds), At: now}
	rec.Decisions = append(rec.Decisions, task.Decision{AskedAt: now, Diagnosis: &diag, Question: task.Question{
		Issue:          fmt.Sprintf("The implementer's own review still requests changes after %s of fixes. How should %s proceed?", roundsText(rounds), implementer),
		Context:        ctx.String(),
		Options:        options,
		Recommendation: "A",
		ImpactIfWrong:  impact,
	}})
}

// stopBeforePROption ends a task reviewed before its PR that the developer does not
// want fixed further. The next run fails the task, so no PR opens without a clean review.
var stopBeforePROption = task.Option{Label: "B", Description: "Stop the task without opening a pull request"}

// stopAtLimitOption ends a task with a PR whose own review is not clean. The PR and
// branch stay, and the task is never handed over, since only a clean competing review
// passes the gate.
var stopAtLimitOption = task.Option{Label: "B", Description: "Stop the task. The pull request and branch stay as they are, and the task is not handed over."}

// stoppedAtLimit reports a task whose developer chose to stop it at the fix limit, in
// an answer the runner has not acted on yet.
func stoppedAtLimit(rec *task.Record) bool {
	return slices.ContainsFunc(rec.Undelivered(), stopAnswer)
}

// stopAnswer reports a decision answered with a stop at the fix limit.
func stopAnswer(d task.Decision) bool {
	for _, o := range d.Question.Options {
		if (o.Description == stopBeforePROption.Description || o.Description == stopAtLimitOption.Description) && d.Answer == optionText(o) {
			return true
		}
	}
	return false
}

// stopSummary is the failure a stop at the limit records, with the findings left open.
func stopSummary(rec *task.Record) string {
	summary := "stopped by the maintainer at the fix limit"
	if rec.Delegation.PR == 0 {
		summary = "stopped by the maintainer; no PR opened"
	}
	if latest := rec.LatestReview(); latest != nil && !latest.Clean {
		var open []string
		for _, f := range latest.Findings {
			open = append(open, f.Summary)
		}
		summary += "; unresolved findings: " + strings.Join(open, "; ")
	}
	return summary
}

func roundsText(n int) string {
	if n == 1 {
		return "1 round"
	}
	return fmt.Sprintf("%d rounds", n)
}
