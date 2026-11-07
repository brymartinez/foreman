package app

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

func mergePlanTexts(base, changed map[task.PlanDoc]string) map[task.PlanDoc]string {
	out := maps.Clone(base)
	if out == nil {
		out = map[task.PlanDoc]string{}
	}
	for doc, text := range changed {
		if strings.TrimSpace(text) != "" {
			out[doc] = text
		}
	}
	return out
}

func clonePlanDraft(d *task.PlanDraft) *task.PlanDraft {
	if d == nil {
		return nil
	}
	out := *d
	out.Documents = maps.Clone(d.Documents)
	out.Sources = maps.Clone(d.Sources)
	out.Findings = slices.Clone(d.Findings)
	out.Advice = slices.Clone(d.Advice)
	return &out
}

// restorePlanDraft continues local planning work only while triage, the issue
// description, and every existing target comment still match its saved sources.
func (r *taskRun) restorePlanDraft() (bool, step, error) {
	draft := clonePlanDraft(r.rec.Delegation.PlanDraft)
	if draft == nil {
		return false, stepImplement, nil
	}
	current, err := r.a.readIssuePlan(r.ctx, r.rec)
	if err != nil {
		rec, saveErr := r.a.update(r.final, r.rec.Key, existing(func(cur *task.Record) {
			failTask(cur, fmt.Sprintf("reading saved planning sources failed: %v", err), r.a.Now())
		}))
		r.rec = rec
		return true, stepDone, saveErr
	}
	valid := draft.Scope == r.scope.digest && draft.Fingerprint == current.fingerprint
	for _, doc := range task.PlanDocs {
		found, exists := current.found[doc]
		source, wasPresent := draft.Sources[doc]
		if exists && wasPresent && draft.Documents[doc] == source {
			id := found.comment.ExternalCommentID
			if r.rec.IssueSource() != task.Linear {
				id = fmt.Sprint(found.comment.CommentID)
			}
			still, err := r.a.stillPlanComment(r.ctx, r.rec.Key, doc, id)
			if err != nil {
				r.rec, err = r.a.update(r.final, r.rec.Key, existing(func(cur *task.Record) {
					failTask(cur, "reading saved plan comment: "+err.Error(), r.a.Now())
				}))
				return true, stepDone, err
			}
			exists = still != ""
		}
		if (!exists && wasPresent) || (exists && found.text != source && found.text != draft.Documents[doc]) {
			valid = false
		}
	}
	if !valid {
		r.rec, err = r.a.update(r.final, r.rec.Key, existing(func(cur *task.Record) { cur.Delegation.PlanDraft = nil; cur.Delegation.Action = nil }))
		return false, stepImplement, err
	}
	r.plan = current
	r.rec, err = r.a.postPlan(r.ctx, r.final, r.rec, draft.Documents, current, r.log)
	if err != nil || r.rec.State != task.Planning {
		return true, stepDone, err
	}
	return true, stepImplement, nil
}
