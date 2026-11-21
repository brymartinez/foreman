package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/brymartinez/foreman/internal/task"
)

var errLocalNoComments = errors.New("local issues have no tracker comments")

func (a App) issue(ctx context.Context, key task.Key) (Issue, error) {
	if key.Source == task.Local {
		if a.LocalIssues == nil {
			return Issue{}, fmt.Errorf("local issue store is not configured")
		}
		return a.LocalIssues.GetIssue(ctx, key)
	}
	return a.GitHub.GetIssue(ctx, key)
}

// issueComments reads an issue's comments. A local issue has none: its confirmed
// triage lives in the triage store.
func (a App) issueComments(ctx context.Context, key task.Key) ([]TrackerComment, error) {
	if key.Source == task.Local {
		return nil, nil
	}
	comments, err := a.GitHub.IssueComments(ctx, key)
	if err != nil {
		return nil, err
	}
	out := make([]TrackerComment, len(comments))
	for i, c := range comments {
		out[i] = TrackerComment{ID: fmt.Sprint(c.ID), URL: c.URL, Body: c.Body}
	}
	return out, nil
}

// issueComment reads one comment on the task's issue source. A missing comment
// returns ErrGone.
func (a App) issueComment(ctx context.Context, key task.Key, id string) (TrackerComment, error) {
	if key.Source == task.Local {
		return TrackerComment{}, errLocalNoComments
	}
	number, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return TrackerComment{}, fmt.Errorf("invalid GitHub comment ID %q: %w", id, err)
	}
	c, err := a.GitHub.GetIssueComment(ctx, key.Repo, number)
	return TrackerComment{ID: strconv.FormatInt(c.ID, 10), URL: c.URL, Body: c.Body}, err
}

func issueLabel(key task.Key, identifier string) string {
	if key.Source == task.Local {
		return identifier
	}
	return fmt.Sprintf("#%d", key.Number)
}

func (a App) postIssueComment(ctx context.Context, key task.Key, id, body string) (TrackerComment, error) {
	if key.Source == task.Local {
		return TrackerComment{}, errLocalNoComments
	}
	number := int64(0)
	if id != "" {
		var err error
		number, err = strconv.ParseInt(id, 10, 64)
		if err != nil {
			return TrackerComment{}, fmt.Errorf("invalid GitHub comment ID %q: %w", id, err)
		}
	}
	comment, err := a.GitHub.PostIssueComment(ctx, key, number, body)
	return TrackerComment{ID: strconv.FormatInt(comment.ID, 10), URL: comment.URL, Body: comment.Body}, err
}
