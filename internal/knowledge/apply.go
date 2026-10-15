package knowledge

import (
	"fmt"
	"time"
)

// apply folds ev into f and returns the evaluation as it should be stored: rejected
// proposals and revisions are replaced by a reason in Dropped, and free text is
// redacted.
//
// The rules, in order:
//   - The task's earlier evidence is withdrawn, so a re-evaluation replaces it.
//   - A one-off proposal stays in the evaluation only.
//   - A reusable proposal reinforces the learning it supports or repeats, or starts a
//     new candidate. Observed work cannot revive a retired learning.
//   - A maintainer revision applies at once. An observed one applies only when this
//     task started after the learning's latest evidence from other tasks.
//   - A learning is active with maintainer evidence or evidence from two tasks, a
//     candidate with one, and retired with none.
func (s Store) apply(f *learningsFile, ev Evaluation) Evaluation {
	ev.Assessment = Redact(ev.Assessment)
	for i := range ev.Signals {
		ev.Signals[i] = Redact(ev.Signals[i])
	}
	for i := range f.Learnings {
		withdraw(&f.Learnings[i], ev.Task)
	}

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
		evidence := Evidence{Task: ev.Task, At: ev.TaskAt, Source: p.Source, Note: p.Why}
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
		if l.Status == Retired || (r.Source == Observed && !ev.TaskAt.After(latest(*l, ev.Task))) {
			continue
		}
		change := Change{At: ev.At, Task: ev.Task, Action: r.Action, Reason: r.Reason}
		if r.Action == Retire {
			l.Status = Retired
		} else {
			change.From = l.Text
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

// settle derives a non-retired learning's status from its evidence and records a
// change of status in its history.
func settle(l *Learning, ev Evaluation) {
	if l.Status == Retired {
		return
	}
	next := Candidate
	switch {
	case len(l.Evidence) == 0:
		next = Retired
	case l.Tasks() >= 2:
		next = Active
	default:
		for _, e := range l.Evidence {
			if e.Source == Maintainer {
				next = Active
			}
		}
	}
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
