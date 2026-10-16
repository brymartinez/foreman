package knowledge

import (
	"fmt"
	"slices"
	"time"
)

// apply folds ev into f and returns the evaluation as it should be stored: rejected
// proposals and revisions are replaced by a reason in Dropped, and free text is
// redacted.
//
// The rules, in order:
//   - The task's earlier evidence and its earlier retire or rewrite are withdrawn, so
//     a re-evaluation replaces them. Another task's retirement or a later rewrite
//     stays.
//   - A maintainer source counts only when it cites the task's own decisions or
//     comments and nothing else. Otherwise it counts as observed, so the evaluator
//     cannot invent a correction.
//   - A one-off proposal stays in the evaluation only.
//   - A reusable proposal reinforces the learning it supports or repeats, or starts a
//     new candidate. Observed work cannot revive a retired learning.
//   - A maintainer revision applies at once. An observed one applies only when this
//     task started after the learning's latest evidence from other tasks, so a
//     learning only this task supports cannot be retired by its own observation.
//   - A learning is active with maintainer evidence or evidence from two tasks, a
//     candidate with one, and retired with none.
func (s Store) apply(f *learningsFile, ev Evaluation) Evaluation {
	ev.Assessment = Redact(ev.Assessment)
	for i := range ev.Signals {
		ev.Signals[i] = Redact(ev.Signals[i])
	}
	for i := range f.Learnings {
		withdraw(&f.Learnings[i], ev.Task)
		revert(&f.Learnings[i], ev)
	}
	s.demote(&ev)

	var kept []Proposal
	for i, p := range ev.Proposals {
		text, err := s.check(p.Kind, p.Text, p.Source)
		if err != nil {
			ev.Dropped = append(ev.Dropped, s.drop("proposal", i+1, err))
			continue
		}
		p.Text, p.Why = text, Redact(p.Why)
		kept = append(kept, p)
		if p.Scope != Reusable {
			continue
		}
		evidence := Evidence{Task: ev.Task, At: ev.TaskAt, Source: p.Source, Note: p.Why, Cites: p.Cites}
		l := find(f, p)
		switch {
		case l == nil:
			f.Learnings = append(f.Learnings, Learning{ID: nextID(f), Kind: p.Kind, Text: text, Evidence: []Evidence{evidence}})
		case l.Status == Retired && p.Source != Maintainer:
		default:
			if l.Status == Retired {
				l.Status = Candidate
				l.History = append(l.History, Change{At: ev.At, Task: ev.Task, Action: "restore", Reason: p.Why})
			}
			l.Evidence = append(l.Evidence, evidence)
		}
	}
	ev.Proposals = kept

	var revisions []Revision
	for i, r := range ev.Revisions {
		r.Reason = Redact(r.Reason)
		l := byID(f, r.ID)
		if l == nil || (r.Action != Revise && r.Action != Retire) || (r.Source != Maintainer && r.Source != Observed) {
			ev.Dropped = append(ev.Dropped, s.drop("revision", i+1, fmt.Errorf("unknown learning or action")))
			continue
		}
		if r.Action == Revise {
			text, err := Sanitize(r.Text)
			if err != nil {
				ev.Dropped = append(ev.Dropped, s.drop("revision", i+1, err))
				continue
			}
			r.Text = text
		}
		revisions = append(revisions, r)
		if other := latest(*l, ev.Task); r.Source == Observed && (other.IsZero() || !ev.TaskAt.After(other)) {
			continue
		}
		if l.Status == Retired && r.Action == Revise {
			continue
		}
		// A retirement of a retired learning is still recorded, so withdrawing another
		// task's retirement later does not revive it.
		change := Change{At: ev.At, Task: ev.Task, Action: r.Action, Reason: r.Reason}
		if r.Action == Retire {
			l.Status = Retired
		} else {
			change.From, change.To = l.Text, r.Text
			l.Text = r.Text
			l.Evidence = append(l.Evidence, Evidence{Task: ev.Task, At: ev.TaskAt, Source: r.Source, Note: r.Reason})
		}
		l.History = append(l.History, change)
	}
	ev.Revisions = revisions

	for i := range f.Learnings {
		settle(&f.Learnings[i], ev)
	}
	return ev
}

// demote turns each maintainer source without a valid citation into an observed one
// and logs it by position. The evaluator still judges whether the cited decision or
// comment says what it claims; the citation keeps that judgment auditable.
func (s Store) demote(ev *Evaluation) {
	for i := range ev.Proposals {
		p := &ev.Proposals[i]
		if p.Source == Maintainer && !cited(p.Cites, ev.MaintainerRefs) {
			p.Source, p.Cites = Observed, nil
			s.note("proposal", i+1)
		}
	}
	for i := range ev.Revisions {
		r := &ev.Revisions[i]
		if r.Source == Maintainer && !cited(r.Cites, ev.MaintainerRefs) {
			r.Source, r.Cites = Observed, nil
			s.note("revision", i+1)
		}
	}
}

func cited(cites, refs []string) bool {
	for _, c := range cites {
		if !slices.Contains(refs, c) {
			return false
		}
	}
	return len(cites) > 0
}

func (s Store) note(what string, n int) {
	if s.Log != nil {
		fmt.Fprintf(s.Log, "knowledge: %s %d claims a maintainer source but cites no maintainer evidence of this task; treated as observed\n", what, n)
	}
}

func (s Store) check(kind Kind, text string, source Source) (string, error) {
	if !validKind(kind) {
		return "", fmt.Errorf("unknown kind")
	}
	if source != Maintainer && source != Observed {
		return "", fmt.Errorf("unknown source")
	}
	return Sanitize(text)
}

// drop logs why an item was rejected and returns the same note for the evaluation
// record. Neither carries the item's text.
func (s Store) drop(what string, n int, err error) string {
	note := fmt.Sprintf("%s %d: %v", what, n, err)
	if s.Log != nil {
		fmt.Fprintf(s.Log, "knowledge: dropped %s\n", note)
	}
	return note
}

func withdraw(l *Learning, task string) {
	kept := l.Evidence[:0]
	for _, e := range l.Evidence {
		if e.Task != task {
			kept = append(kept, e)
		}
	}
	l.Evidence = kept
}

// find returns the learning a proposal supports, or one with the same text.
func find(f *learningsFile, p Proposal) *Learning {
	for _, id := range p.Supports {
		if l := byID(f, id); l != nil {
			return l
		}
	}
	want := normalize(p.Text)
	for i := range f.Learnings {
		if normalize(f.Learnings[i].Text) == want {
			return &f.Learnings[i]
		}
	}
	return nil
}

func byID(f *learningsFile, id string) *Learning {
	for i := range f.Learnings {
		if f.Learnings[i].ID == id {
			return &f.Learnings[i]
		}
	}
	return nil
}

// latest is when the newest other task behind l started.
func latest(l Learning, task string) time.Time {
	var t time.Time
	for _, e := range l.Evidence {
		if e.Task != task && e.At.After(t) {
			t = e.At
		}
	}
	return t
}

// revert undoes the task's own retire or rewrite of l before its re-evaluation is
// applied. l stays retired while a retirement by another task is still in force.
// The text becomes the newest rewrite still in force, or the original text when
// none is left.
func revert(l *Learning, ev Evaluation) {
	own := func(c Change) bool { return c.Task == ev.Task }
	if slices.ContainsFunc(live(l.History, Revise, "revert-rewrite"), own) {
		l.History = append(l.History, Change{At: ev.At, Task: ev.Task, Action: "revert-rewrite", From: l.Text, Reason: "re-evaluation replaced this task's rewrite"})
		text := originalText(*l)
		if rewrites := live(l.History, Revise, "revert-rewrite"); len(rewrites) > 0 {
			text = rewrites[len(rewrites)-1].To
		}
		l.Text = text
	}
	if l.Status == Retired && slices.ContainsFunc(live(l.History, Retire, "revert-retire"), own) {
		l.History = append(l.History, Change{At: ev.At, Task: ev.Task, Action: "revert-retire", Reason: "re-evaluation replaced this task's retirement"})
		if len(live(l.History, Retire, "revert-retire")) == 0 {
			l.Status = derive(*l)
		}
	}
}

// live returns the history's action entries, oldest first, that are still in force:
// no later undo entry by the same task withdrew them. A restore ends every earlier
// retirement.
func live(history []Change, action, undo string) []Change {
	var out []Change
	for i, c := range history {
		if action == Retire && c.Action == "restore" {
			out = nil
		}
		if c.Action != action {
			continue
		}
		withdrawn := slices.ContainsFunc(history[i+1:], func(w Change) bool { return w.Action == undo && w.Task == c.Task })
		if !withdrawn {
			out = append(out, c)
		}
	}
	return out
}

// originalText is l's text before its first rewrite.
func originalText(l Learning) string {
	for _, c := range l.History {
		if c.Action == Revise {
			return c.From
		}
	}
	return l.Text
}

// derive is the status l's evidence gives it: active with maintainer evidence or
// two tasks, a candidate with one task, retired with none.
func derive(l Learning) Status {
	switch {
	case len(l.Evidence) == 0:
		return Retired
	case l.Tasks() >= 2:
		return Active
	}
	for _, e := range l.Evidence {
		if e.Source == Maintainer {
			return Active
		}
	}
	return Candidate
}

// settle derives a non-retired learning's status from its evidence and records a
// change of status in its history.
func settle(l *Learning, ev Evaluation) {
	if l.Status == Retired {
		return
	}
	next := derive(*l)
	if next == l.Status {
		return
	}
	action := map[Status]string{Active: "promote", Candidate: "demote", Retired: "withdraw"}[next]
	if l.Status == "" {
		action = "propose"
	}
	l.History = append(l.History, Change{At: ev.At, Task: ev.Task, Action: action})
	l.Status = next
}
