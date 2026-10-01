package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// readFeedback records the comments on a task's pull request and returns the record as
// it now stands with the comments still waiting for an answer, newest text first in
// GitHub's own order. The bodies are returned beside them, because the task file keeps
// only each comment's identity (D117).
func (a App) readFeedback(ctx context.Context, rec *task.Record) (*task.Record, []task.Feedback, map[int64]PRComment, error) {
	d := rec.Delegation
	comments, err := a.GitHub.PullRequestComments(ctx, task.Key{Repo: rec.Repo, Number: d.PR})
	if err != nil {
		return rec, nil, nil, fmt.Errorf("read %s#%d PR comments: %w", rec.Repo, rec.Number, err)
	}
	seen := make([]task.Feedback, 0, len(comments))
	bodies := map[int64]PRComment{}
	for _, c := range comments {
		if strings.TrimSpace(c.Body) == "" || strings.Contains(c.Body, agent.ReplyMarker) {
			continue
		}
		seen = append(seen, task.Feedback{
			Kind: c.Kind, ID: c.ID, Author: c.Author, URL: c.URL,
			At: c.UpdatedAt, Digest: task.Digest(c.Body),
		})
		bodies[c.ID] = c
	}
	updated, err := a.update(ctx, rec.Key, func(cur *task.Record) (*task.Record, error) {
		if cur == nil || cur.Delegation == nil || cur.Delegation.PR != d.PR {
			return cur, nil
		}
		cur.MergeFeedback(seen)
		return cur, nil
	})
	if err != nil || updated == nil {
		return rec, nil, nil, err
	}
	return updated, updated.UnhandledFeedback(), bodies, nil
}

// feedback is the gate's look at a handed-over task's pull request. Unanswered
// comments move it to REVIEWED, which is where INBOX offers it; a task whose comments
// have all been answered, or deleted, goes back to the developer.
func (a App) feedback(ctx context.Context, rec *task.Record) (*task.Record, string, error) {
	updated, waiting, _, err := a.readFeedback(ctx, rec)
	if err != nil {
		return rec, "", err
	}
	want := task.ReadyForHuman
	if len(waiting) > 0 {
		want = task.Reviewed
	}
	if updated.State == want {
		return updated, "", nil
	}
	var note string
	updated, err = a.update(ctx, rec.Key, func(cur *task.Record) (*task.Record, error) {
		// Read again under the lock: a start since the gate's list owns the task now.
		if cur == nil || cur.State != rec.State || cur.Delegation == nil || cur.Delegation.PR != rec.Delegation.PR {
			return cur, nil
		}
		cur.State = want
		if want == task.ReadyForHuman {
			note = fmt.Sprintf("every comment on PR #%d is answered", cur.Delegation.PR)
			cur.LastOutcome = &task.Outcome{Status: "feedback_answered", Summary: note, At: a.Now()}
			return cur, nil
		}
		note = feedbackNote(len(waiting), cur.Delegation.PR)
		cur.LastOutcome = &task.Outcome{Status: "reviewed", Summary: note, At: a.Now()}
		return cur, nil
	})
	return updated, note, err
}

func feedbackNote(n, pr int) string {
	return fmt.Sprintf("%d %s to answer on PR #%d", n, plural(n, "comment", "comments"), pr)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// handsOver reports a task whose pull request is the developer's to comment on: the
// agents are done with it and no runner is driving it, so a comment is feedback rather
// than something the current run is about to answer itself (D119).
func handsOver(r *task.Record) bool {
	d := r.Delegation
	if d == nil || d.PR == 0 || d.RunnerPID != 0 {
		return false
	}
	return r.State == task.ReadyForHuman || r.State == task.Reviewed
}
