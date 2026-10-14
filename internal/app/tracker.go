package app

import (
	"context"
	"fmt"

	"github.com/brymartinez/foreman/internal/task"
)

func (a App) issue(ctx context.Context, key task.Key) (Issue, error) {
	if key.Source == task.Linear {
		if a.Linear == nil {
			return Issue{}, fmt.Errorf("Linear issue source is not configured")
		}
		return a.Linear.GetIssue(ctx, key)
	}
	return a.GitHub.GetIssue(ctx, key)
}

func (a App) issueComments(ctx context.Context, key task.Key) ([]TrackerComment, error) {
	if key.Source == task.Linear {
		if a.Linear == nil {
			return nil, fmt.Errorf("Linear issue source is not configured")
		}
		return a.Linear.IssueComments(ctx, key)
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

func issueLabel(key task.Key, identifier string) string {
	if key.Source == task.Linear {
		return identifier
	}
	return fmt.Sprintf("#%d", key.Number)
}
