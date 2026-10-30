package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// foundPlan is a planning document already on the issue.
type foundPlan struct {
	comment     task.PlanComment
	text        string // the document without its marker and heading
	fingerprint string // of the description it was written for; empty on older comments
}

// issuePlan is what the issue holds of the task's plan.
type issuePlan struct {
	fingerprint string // of the current description
	found       map[task.PlanDoc]foundPlan
	conflicts   []task.PlanDoc // found documents written for another description
}

// complete reports whether every planning document is on the issue.
func (p issuePlan) complete() bool {
	for _, doc := range task.PlanDocs {
		if _, ok := p.found[doc]; !ok {
			return false
		}
	}
	return true
}

// texts are the found documents for the planning prompt.
func (p issuePlan) texts() map[task.PlanDoc]string {
	if len(p.found) == 0 {
		return nil
	}
	out := map[task.PlanDoc]string{}
	for _, doc := range task.PlanDocs {
		if f, ok := p.found[doc]; ok {
			out[doc] = f.text
		}
	}
	return out
}

// readIssuePlan finds the planning documents on the issue. Only a comment that starts
// with a document's marker counts; the description and unmarked comments never do, so
// a recorded comment that lost its marker is left alone. A document's recorded comment
// wins while it is still marked, and is never a conflict: the task wrote or accepted
// it. Otherwise the newest marked comment wins. Both sources list comments oldest first.
func (a App) readIssuePlan(ctx context.Context, rec *task.Record) (issuePlan, error) {
	issue, err := a.issue(ctx, rec.Key)
	if err != nil {
		return issuePlan{}, err
	}
	comments, err := a.issueComments(ctx, rec.Key)
	if err != nil {
		return issuePlan{}, err
	}
	p := issuePlan{fingerprint: task.DescriptionFingerprint(issue.Body), found: map[task.PlanDoc]foundPlan{}}
	recorded := map[task.PlanDoc]bool{}
	for _, c := range comments {
		doc, fp, ok := task.ParsePlanMarker(c.Body)
		if !ok || recorded[doc] {
			continue
		}
		comment := planCommentFor(rec.IssueSource(), doc, c)
		cur := rec.PlanComment(doc)
		recorded[doc] = cur != nil && samePlanComment(*cur, comment)
		p.found[doc] = foundPlan{comment: comment, text: planText(doc, c.Body), fingerprint: fp}
	}
	for _, doc := range task.PlanDocs {
		f, ok := p.found[doc]
		if ok && !recorded[doc] && f.fingerprint != "" && f.fingerprint != p.fingerprint {
			p.conflicts = append(p.conflicts, doc)
		}
	}
	return p, nil
}

// planCommentFor is the record entry for a comment on the task's issue source.
func planCommentFor(source task.Source, doc task.PlanDoc, c TrackerComment) task.PlanComment {
	comment := task.PlanComment{Doc: doc, URL: c.URL}
	if source == task.Linear {
		comment.ExternalCommentID = c.ID
	} else {
		comment.CommentID, _ = strconv.ParseInt(c.ID, 10, 64)
	}
	return comment
}

func samePlanComment(a, b task.PlanComment) bool {
	return a.CommentID == b.CommentID && a.ExternalCommentID == b.ExternalCommentID
}

// planText strips the marker line and heading planCommentBody wrote.
func planText(doc task.PlanDoc, body string) string {
	_, rest, _ := strings.Cut(strings.TrimSpace(body), "\n")
	rest = strings.TrimSpace(rest)
	rest = strings.TrimPrefix(rest, "## "+doc.Title())
	if doc == task.PRD {
		rest = strings.TrimPrefix(rest, "## PRD")
	} else if doc == task.ImplementationPlan {
		rest = strings.TrimPrefix(rest, "## Implementation plan")
	}
	return strings.TrimSpace(rest)
}

// planConflictIssue is the question foreman asks when a plan on the issue was written
// for another description. Its answer is how a later run knows it was asked.
const planConflictIssue = "The issue description changed after the plan on the issue was written. Keep that plan or revise it?"

var (
	keepPlanOption   = task.Option{Label: "A", Description: "Keep the plan on the issue. Foreman adopts it and writes only missing documents."}
	revisePlanOption = task.Option{Label: "B", Description: "Revise the plan. A planning turn updates the documents for the current description and edits their comments in place."}
)

// planConflictAnswer is the developer's answer to the plan conflict question asked
// about the current description, if any. An answer about an earlier description does
// not cover a later edit.
func planConflictAnswer(rec *task.Record, fingerprint string) (string, bool) {
	for i := len(rec.Decisions) - 1; i >= 0; i-- {
		if d := rec.Decisions[i]; d.Question.Issue == planConflictIssue && d.Answer != "" && strings.HasSuffix(d.Question.Context, descriptionLine(fingerprint)) {
			return d.Answer, true
		}
	}
	return "", false
}

// descriptionLine ends the conflict question's context with the fingerprint of the
// description it was asked about.
func descriptionLine(fingerprint string) string {
	return "\n\nCurrent description fingerprint: " + fingerprint + "."
}

// askAboutPlanConflict blocks the task on a plan written for another description.
// The session is cleared so the planning turn that follows the answer starts fresh
// with the documents in its prompt.
func askAboutPlanConflict(rec *task.Record, p issuePlan, now time.Time) {
	var ctx strings.Builder
	ctx.WriteString("These planning comments were written for an earlier issue description:")
	for _, doc := range p.conflicts {
		fmt.Fprintf(&ctx, "\n- %s: %s", doc.Title(), p.found[doc].comment.URL)
	}
	ctx.WriteString(descriptionLine(p.fingerprint))
	rec.State = task.NeedsDecision
	rec.Delegation.Implementer.ID = ""
	rec.LastOutcome = &task.Outcome{Status: "blocked", Summary: "the plan on the issue was written for an earlier description", At: now}
	rec.Decisions = append(rec.Decisions, task.Decision{AskedAt: now, Question: task.Question{
		Issue:          planConflictIssue,
		Context:        ctx.String(),
		Options:        []task.Option{keepPlanOption, revisePlanOption},
		Recommendation: revisePlanOption.Label,
		ImpactIfWrong:  "A implements a plan that may not match the description; B spends a planning turn and may rewrite a plan you wanted kept.",
	}})
}

// reconcilePlan reads the plan on the issue before a planning turn, so a task whose
// record lost its plan comments does not post them again. It reports whether the run
// goes on to a turn. A complete plan is adopted and the turn implements it; a partial
// one goes to the planning turn. A plan written for another description waits for the
// developer, and a failed read fails the task before anything is posted.
func (r *taskRun) reconcilePlan() (bool, error) {
	a := r.a
	p, readErr := a.readIssuePlan(r.ctx, r.rec)
	answer, answered := planConflictAnswer(r.rec, p.fingerprint)
	adopt := readErr == nil && p.complete() && (len(p.conflicts) == 0 || answer == optionText(keepPlanOption))
	var missingPlanErr error
	if readErr == nil && r.rec.PlanPosted() && !p.complete() && r.rec.Delegation.ClaimVerification {
		_, missingPlanErr = a.reviewPRD(r.ctx, r.rec)
	}
	var emptyPlanErr error
	if readErr == nil {
		for _, doc := range task.PlanDocs {
			if f, ok := p.found[doc]; ok && strings.TrimSpace(f.text) == "" {
				emptyPlanErr = fmt.Errorf("the %s planning comment %s is empty", doc.Title(), f.comment.URL)
				if doc == task.PRD && r.rec.Delegation.ClaimVerification {
					_, emptyPlanErr = a.reviewPRD(r.ctx, r.rec)
				}
				break
			}
		}
	}
	var scopeCheckErr error
	var scopeCheck agent.ReviewOutcome
	checkedNow := false
	if adopt {
		if r.rec.Delegation.PlanScopeDigest == r.scope.digest && r.rec.Delegation.PlanDigest == planDigest(p.texts()) {
			scopeCheck.Clean = true
			fmt.Fprintln(r.log, "foreman: reusing the plan agreement check for unchanged scope and documents")
		} else {
			checkedNow = true
			scopeCheck, scopeCheckErr = a.checkPlan(r.ctx, r.rec, r.scope, p.texts(), p, r.log)
		}
	}
	scopeAnswer := scopePlanAnswer(r.rec, r.scope.digest, planDigest(p.texts()))
	rec, err := a.update(r.final, r.rec.Key, existing(func(cur *task.Record) {
		if checkedNow && scopeCheckErr == nil {
			cur.PlanningAdvice = append([]task.Advice(nil), scopeCheck.Advice...)
		}
		switch {
		case readErr != nil:
			failTask(cur, fmt.Sprintf("reading the issue to reconcile its plan failed: %v", readErr), a.Now())
			applyStop(r.ctx, cur, "planning", r.log, a.Now())
		case emptyPlanErr != nil:
			failTask(cur, emptyPlanErr.Error(), a.Now())
		case r.rec.PlanPosted() && !p.complete():
			if missingPlanErr != nil {
				failTask(cur, missingPlanErr.Error(), a.Now())
			} else {
				var missing []string
				for _, doc := range task.PlanDocs {
					if _, ok := p.found[doc]; !ok {
						if ref := cur.PlanComment(doc); ref != nil {
							missing = append(missing, doc.Title()+": "+ref.URL)
						}
					}
				}
				failTask(cur, "recorded planning documents were deleted or lost their markers ("+strings.Join(missing, ", ")+"); restore or revise them before restarting", a.Now())
			}
		case scopeCheckErr != nil:
			failTask(cur, scopeCheckErr.Error(), a.Now())
		case adopt && !scopeCheck.Clean && len(scopeCheck.Findings) == 0:
			failTask(cur, "plan agreement with confirmed triage is inconclusive; inspect the planning comments and retry", a.Now())
		case adopt && !scopeCheck.Clean && strings.HasPrefix(scopeAnswer, "A."):
			// The developer chose to revise; the next planning turn writes
			// corrected documents from the issue and confirmed triage.
		case adopt && !scopeCheck.Clean && strings.HasPrefix(scopeAnswer, "B."):
			failTask(cur, "confirmed triage has not changed; correct the triage comment and restart", a.Now())
		case adopt && !scopeCheck.Clean:
			askScopePlanConflict(cur, r.scope, p, scopeCheck, a.Now())
		case len(p.conflicts) > 0 && !answered:
			askAboutPlanConflict(cur, p, a.Now())
		case adopt:
			for _, doc := range task.PlanDocs {
				cur.SetPlanComment(p.found[doc].comment)
			}
			cur.Delegation.PlanScopeDigest = r.scope.digest
			cur.Delegation.PlanDigest = planDigest(p.texts())
		}
	}))
	if err != nil {
		return false, err
	}
	r.rec = rec
	if rec.State != task.Planning {
		return false, nil
	}
	if adopt && scopeCheck.Clean {
		fmt.Fprintln(r.log, "foreman: the issue already has the plan; implementing it")
		return true, nil
	}
	r.plan = p
	for _, doc := range task.PlanDocs {
		if f, ok := p.found[doc]; ok {
			fmt.Fprintf(r.log, "foreman: the issue already has the %s: %s\n", doc.Title(), f.comment.URL)
		}
	}
	return true, nil
}
