package app

import (
	"context"
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

func (a App) savePlanDraft(ctx context.Context, key task.Key, draft *task.PlanDraft) (*task.Record, error) {
	return a.update(ctx, key, existing(func(cur *task.Record) {
		cur.Delegation.PlanDraft = clonePlanDraft(draft)
		cur.PlanningAdvice = slices.Clone(draft.Advice)
	}))
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
		if (!exists && wasPresent) || (exists && found.text != source && found.text != draft.Documents[doc]) {
			valid = false
		}
	}
	if !valid {
		r.rec, err = r.a.update(r.final, r.rec.Key, existing(func(cur *task.Record) { cur.Delegation.PlanDraft = nil; cur.Delegation.Implementer.ID = "" }))
		return false, stepImplement, err
	}
	if draft.Stopped || draft.Revisions >= 3 {
		answer := scopePlanAnswer(r.rec, r.scope.digest, planDigest(draft.Documents))
		if strings.HasPrefix(answer, "B.") {
			r.rec, err = r.a.update(r.final, r.rec.Key, existing(func(cur *task.Record) {
				failTask(cur, "confirmed triage has not changed; correct the triage comment and restart", r.a.Now())
			}))
			return true, stepDone, err
		}
		if strings.HasPrefix(answer, "A.") {
			draft.Stopped = false
			draft.Revisions = 0
			r.rec, err = r.a.savePlanDraft(r.final, r.rec.Key, draft)
			if err != nil {
				return true, stepDone, err
			}
		}
	}
	r.plan = current
	r.rec, err = r.a.postPlan(r.ctx, r.final, r.rec, draft.Documents, current, r.log)
	if err != nil || r.rec.State != task.Planning {
		return true, stepDone, err
	}
	if r.rec.Delegation.PR != 0 {
		return true, stepReview, nil
	}
	return true, stepImplement, nil
}
