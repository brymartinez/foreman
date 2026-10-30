package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// postPlan puts each planning document on the issue as its own comment, editing the
// marked comment found on the issue when there is one. A document the turn left empty
// keeps the comment found on the issue as it is. Each comment is recorded as soon as
// it is posted. A failed post keeps the checked draft for the next run.
func (a App) postPlan(ctx, final context.Context, rec *task.Record, docs map[task.PlanDoc]string, onIssue issuePlan, log io.Writer) (*task.Record, error) {
	// Every kept document is checked before anything is posted, so a plan that can't
	// be completed leaves the issue as it was.
	for _, doc := range task.PlanDocs {
		if strings.TrimSpace(docs[doc]) != "" {
			continue
		}
		found, ok := onIssue.found[doc]
		reason := fmt.Sprintf("the planning turn wrote no %s", doc.Title())
		if ok {
			// The comment may have been deleted or unmarked while the turn ran, and
			// the turn left no text to post in its place.
			id := found.comment.ExternalCommentID
			if rec.IssueSource() != task.Linear {
				id = strconv.FormatInt(found.comment.CommentID, 10)
			}
			kept, err := a.stillPlanComment(ctx, rec.Key, doc, id)
			switch {
			case err != nil:
				reason = fmt.Sprintf("reading the %s on the issue failed: %v", doc.Title(), err)
			case kept == "":
				reason = fmt.Sprintf("the %s left on the issue was removed during planning; restart the task to plan it again", doc.Title())
			default:
				continue
			}
		}
		return a.update(final, rec.Key, existing(func(cur *task.Record) {
			failTask(cur, reason, a.Now())
			applyStop(ctx, cur, "planning", log, a.Now())
		}))
	}
	scope, scopeErr := a.readScope(ctx, rec.Key)
	if scopeErr == nil && scope.digest != rec.Delegation.ScopeDigest {
		scopeErr = errors.New("confirmed triage changed during planning; restart to revise the documents")
	}
	if scopeErr != nil {
		return a.update(final, rec.Key, existing(func(cur *task.Record) { failTask(cur, scopeErr.Error(), a.Now()) }))
	}
	texts := onIssue.texts()
	if texts == nil {
		texts = map[task.PlanDoc]string{}
	}
	for doc, text := range docs {
		if strings.TrimSpace(text) != "" {
			texts[doc] = text
		}
	}
	draft := clonePlanDraft(rec.Delegation.PlanDraft)
	if draft == nil || draft.Scope != scope.digest || draft.Fingerprint != onIssue.fingerprint {
		draft = &task.PlanDraft{Scope: scope.digest, Fingerprint: onIssue.fingerprint, Sources: onIssue.texts()}
	}
	draft.Documents = texts
	var draftErr error
	rec, draftErr = a.savePlanDraft(final, rec.Key, draft)
	if draftErr != nil {
		return rec, draftErr
	}
	const maxPlanRevisions = 3
	var checked agent.ReviewOutcome
	var checkErr error
	for {
		if draft.CheckedDigest == planDigest(texts) && (draft.Clean || len(draft.Findings) > 0) {
			checked = agent.ReviewOutcome{Clean: draft.Clean, Findings: draft.Findings, Advice: draft.Advice}
		} else {
			checked, checkErr = a.checkPlan(ctx, rec, scope, texts, onIssue, log)
			if checkErr == nil {
				draft.CheckedDigest, draft.Clean, draft.Findings = planDigest(texts), checked.Clean, checked.Findings
				draft.Advice = checked.Advice
				rec, draftErr = a.savePlanDraft(final, rec.Key, draft)
				if draftErr != nil {
					return rec, draftErr
				}
			}
		}
		if checkErr != nil || checked.Clean || len(checked.Findings) == 0 || draft.Stopped || draft.Revisions >= maxPlanRevisions {
			break
		}
		planner := a.Agents[rec.Delegation.Implementer.Agent]
		var message strings.Builder
		message.WriteString("The read-only plan check found gaps or disagreements. Fix Requirements and Work plan in this planning session using the confirmed scope you already received. Return planned with only changed documents; use an empty string for an unchanged document. Do not repeat code research unless a finding needs it. End with the same JSON outcome: planned, blocked, or failed.\n\n")
		for _, finding := range checked.Findings {
			fmt.Fprintf(&message, "- %s: %s\n", finding.Summary, finding.Detail)
		}
		for _, decision := range rec.Undelivered() {
			fmt.Fprintf(&message, "\nQuestion: %s\nDeveloper decision: %s\n", decision.Question.Issue, decision.Answer)
		}
		fmt.Fprintln(log, "foreman: resuming the planner to correct the plan check")
		draft.Revisions++
		rec, draftErr = a.savePlanDraft(final, rec.Key, draft)
		if draftErr != nil {
			return rec, draftErr
		}
		input := agent.StartInput{
			Worktree: rec.Delegation.Worktree, Log: log, Prompt: message.String(),
			Stage: task.StagePlan, Step: agent.StepPlan, Profile: rec.Delegation.Profile,
			MaxBudgetUSD: a.ImplementerBudgetUSD,
		}
		var turn agent.Turn
		turnErr := agent.ErrNoSession
		if rec.Delegation.Implementer.ID != "" {
			turn, turnErr = planner.Resume(ctx, rec.Delegation.Implementer, input)
		}
		if errors.Is(turnErr, agent.ErrNoSession) {
			instructions, instructionsErr := a.Stages.Resolve(rec.Delegation.Worktree, task.StagePlan)
			if instructionsErr != nil {
				return a.update(final, rec.Key, existing(func(cur *task.Record) { failTask(cur, instructionsErr.Error(), a.Now()) }))
			}
			input.Prompt += "\n## Planning instructions\n\n" + instructions.Content() + "\n"
			input.Prompt += "\n## Saved draft\n\n"
			for _, doc := range task.PlanDocs {
				input.Prompt += "### " + doc.Title() + "\n\n" + texts[doc] + "\n\n"
			}
			input.Prompt += "## Confirmed scope\n\n" + agent.ConfirmedScopeRule + "\n\n" + scope.text + "\n\nThis is a fresh planning correction session. Fix only the named gaps. Do not repeat earlier code research unless a finding needs it. Write short B1 English.\n"
			turn, turnErr = planner.Start(ctx, input, func(s task.Session) {
				_, _ = a.update(final, rec.Key, existing(func(cur *task.Record) { cur.Delegation.Implementer = s }))
			})
		}
		if turnErr == nil && turn.Outcome.Status != agent.Planned && turn.Outcome.Status != agent.Blocked && turn.Outcome.Status != agent.FailedRun {
			turnErr = fmt.Errorf("planning revision returned %s instead of planned, blocked, or failed", turn.Outcome.Status)
		}
		result := turnResult{turn: turn, err: turnErr}
		var updateErr error
		rec, updateErr = a.finishTurn(ctx, final, rec.Key, "planning", result, func(cur *task.Record) {
			applyTurnResult(cur, result, a.Now())
			if turnErr == nil && turn.Outcome.Status == agent.Planned {
				nextDraft := clonePlanDraft(draft)
				nextDraft.Documents = mergePlanTexts(texts, turn.Outcome.Documents)
				cur.Delegation.PlanDraft = nextDraft
			}
		}, log)
		if updateErr != nil || rec.State != task.Planning {
			return rec, updateErr
		}
		for doc, text := range turn.Outcome.Documents {
			if strings.TrimSpace(text) != "" {
				docs[doc] = text
				texts[doc] = text
			}
		}

		current, err := a.readScope(ctx, rec.Key)
		if err == nil && current.digest != scope.digest {
			err = errors.New("confirmed triage changed during planning; restart to revise the documents")
		}
		if err != nil {
			return a.update(final, rec.Key, existing(func(cur *task.Record) { failTask(cur, err.Error(), a.Now()) }))
		}
		draft.Documents = texts
		if planDigest(texts) == draft.CheckedDigest {
			draft.Stopped = true
			rec, draftErr = a.savePlanDraft(final, rec.Key, draft)
			if draftErr != nil {
				return rec, draftErr
			}
			break
		}
	}
	if checkErr != nil || !checked.Clean {
		return a.update(final, rec.Key, existing(func(cur *task.Record) {
			switch {
			case checkErr != nil:
				failTask(cur, checkErr.Error(), a.Now())
			case len(checked.Findings) == 0:
				failTask(cur, "plan agreement with confirmed triage is inconclusive; revise and restart", a.Now())
			default:
				proposed := onIssue
				proposed.found = make(map[task.PlanDoc]foundPlan, len(texts))
				for doc, text := range texts {
					f := onIssue.found[doc]
					f.text = text
					proposed.found[doc] = f
				}
				askScopePlanConflict(cur, scope, proposed, checked, a.Now())
			}
		}))
	}
	currentScope, err := a.readScope(ctx, rec.Key)
	if err != nil || currentScope.digest != scope.digest {
		if err == nil {
			err = errors.New("confirmed triage changed during planning; restart to revise the documents")
		}
		return a.update(final, rec.Key, existing(func(cur *task.Record) { failTask(cur, err.Error(), a.Now()) }))
	}

	for _, doc := range task.PlanDocs {
		found, isFound := onIssue.found[doc]
		if strings.TrimSpace(docs[doc]) == "" || (isFound && strings.TrimSpace(docs[doc]) == strings.TrimSpace(found.text)) {
			var err error
			if rec, err = a.update(final, rec.Key, existing(func(cur *task.Record) { cur.SetPlanComment(found.comment) })); err != nil {
				return rec, err
			}
			fmt.Fprintf(log, "foreman: kept the %s: %s\n", doc.Title(), found.comment.URL)
			continue
		}
		// Only a comment still marked as this document is edited. A recorded one that
		// lost its marker belongs to someone else now, so the document is posted anew.
		var id string
		if isFound {
			if rec.IssueSource() == task.Linear {
				id = found.comment.ExternalCommentID
			} else if found.comment.CommentID > 0 {
				id = strconv.FormatInt(found.comment.CommentID, 10)
			}
		}
		body := planCommentBody(doc, onIssue.fingerprint, docs[doc])
		var posted TrackerComment
		var err error
		if id != "" {
			// The marker may have been removed while the planning turn ran.
			id, err = a.stillPlanComment(ctx, rec.Key, doc, id)
		}
		if err == nil {
			posted, err = a.postIssueComment(ctx, rec.Key, id, body)
		}
		if errors.Is(err, ErrGone) && id != "" {
			posted, err = a.postIssueComment(ctx, rec.Key, "", body)
		}
		var updateErr error
		if rec, updateErr = a.update(final, rec.Key, existing(func(cur *task.Record) {
			if err != nil {
				failTask(cur, fmt.Sprintf("posting the %s on the issue failed: %v", doc.Title(), err), a.Now())
				applyStop(ctx, cur, "planning", log, a.Now())
				return
			}
			cur.SetPlanComment(planCommentFor(rec.IssueSource(), doc, posted))
		})); updateErr != nil || err != nil {
			return rec, updateErr
		}
		fmt.Fprintf(log, "foreman: posted the %s: %s\n", doc.Title(), posted.URL)
	}
	return a.update(final, rec.Key, existing(func(cur *task.Record) {
		cur.Delegation.PlanScopeDigest = scope.digest
		cur.Delegation.PlanDigest = planDigest(texts)
		cur.Delegation.PlanDraft = nil
	}))
}

// stillPlanComment returns id when the comment still starts with doc's marker, and
// an empty id, which posts a new comment, when it was deleted or lost the marker.
func (a App) stillPlanComment(ctx context.Context, key task.Key, doc task.PlanDoc, id string) (string, error) {
	c, err := a.issueComment(ctx, key, id)
	if errors.Is(err, ErrGone) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if got, _, ok := task.ParsePlanMarker(c.Body); !ok || got != doc {
		return "", nil
	}
	return id, nil
}

// maxCommentChars is GitHub's limit on an issue comment's length.
const maxCommentChars = 65536

// planCommentBody is one document's comment: a marker foreman can recognize, with the
// fingerprint of the description the plan was written for, the document's heading,
// and the agent's text, cut to fit GitHub's limit.
func planCommentBody(doc task.PlanDoc, fingerprint, text string) string {
	head := task.PlanMarker(doc, fingerprint) + "\n## " + doc.Title() + "\n\n"
	const cut = "\n\n_Truncated at GitHub's comment length limit._"
	text = strings.TrimSpace(text)
	if r := []rune(text); len([]rune(head))+len(r) > maxCommentChars {
		text = string(r[:maxCommentChars-len([]rune(head))-len([]rune(cut))]) + cut
	}
	return head + text
}
