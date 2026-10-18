package app

import (
	"context"
	"fmt"

	"github.com/brymartinez/foreman/internal/task"
)

// Resume continues a REVIEWED task so its agent answers the comments on its pull
// request. Unlike Start it rebuilds nothing: the branch, worktree, PR, and session all
// stay, because answering a comment is the same run going on. The runner picks
// the comments up from the task file.
func (a App) Resume(ctx context.Context, key task.Key) (*task.Record, error) {
	rec, err := a.Store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if err := a.resumable(key, rec); err != nil {
		return nil, err
	}
	var token string
	if _, err := a.update(ctx, key, func(cur *task.Record) (*task.Record, error) {
		if err := a.resumable(key, cur); err != nil {
			return nil, err
		}
		cur.State = task.Implementing
		cur.LastOutcome = &task.Outcome{Status: "resumed", Summary: feedbackNote(len(cur.UnhandledFeedback()), cur.Delegation.PR), At: a.Now()}
		var err error
		token, err = a.claimLaunch(cur)
		return cur, err
	}); err != nil {
		return nil, err
	}
	return a.launch(ctx, key, token)
}

func (a App) resumable(key task.Key, rec *task.Record) error {
	ref := fmt.Sprintf("%s#%d", key.Repo, key.Number)
	switch {
	case !a.ReplyToPRFeedback:
		return fmt.Errorf("%s: automatic PR feedback replies are disabled", ref)
	case rec == nil:
		return fmt.Errorf("%s is not tracked; refresh the dashboard first", ref)
	case rec.State != task.Reviewed:
		return fmt.Errorf("%s is %s, with no PR feedback to answer", ref, rec.State)
	case rec.Delegation == nil:
		return fmt.Errorf("%s has never been delegated", ref)
	case a.liveRunner(rec):
		return fmt.Errorf("%s is already running (pid %d)", ref, rec.Delegation.RunnerPID)
	}
	return nil
}
