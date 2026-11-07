package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

const replaceSessionQuestion = "How should Foreman recover the pending agent session?"

type missingRoleSession struct{ role task.Role }

func (e missingRoleSession) Error() string {
	return fmt.Sprintf("the %s session is missing or its launch could not be confirmed", e.role)
}

func (a App) beginAction(ctx context.Context, rec *task.Record, action task.Action) (*task.Record, error) {
	id, err := newToken()
	if err != nil {
		return nil, err
	}
	return a.update(ctx, rec.Key, func(cur *task.Record) (*task.Record, error) {
		if cur == nil || cur.Delegation == nil || cur.Delegation.RunnerPID != rec.Delegation.RunnerPID {
			return cur, errors.New("the runner no longer owns this task")
		}
		d := cur.Delegation
		if d.Action == nil || d.Action.ID == "" || d.Action.Kind != action.Kind ||
			(action.Role() == task.SelfReviewRole || action.Role() == task.AdversarialRole) &&
				(d.Action.BaseHead != action.BaseHead || d.Action.Head != action.Head || d.Action.FromHead != action.FromHead) {
			action.ID = id
			d.Action = &action
		}
		return cur, nil
	})
}

func (a App) startAction(ctx context.Context, rec *task.Record) (*task.Record, error) {
	id := rec.Delegation.Action.ID
	return a.update(ctx, rec.Key, func(cur *task.Record) (*task.Record, error) {
		if cur == nil || cur.Delegation == nil || cur.Delegation.Action == nil ||
			cur.Delegation.Action.ID != id || cur.Delegation.RunnerPID != rec.Delegation.RunnerPID {
			return cur, errors.New("the pending action is no longer owned by this runner")
		}
		cur.Delegation.Action.Started = true
		return cur, nil
	})
}

func (a App) saveRoleSession(ctx context.Context, rec *task.Record, role task.Role, s task.Session) error {
	id := rec.Delegation.Action.ID
	_, err := a.update(context.WithoutCancel(ctx), rec.Key, func(cur *task.Record) (*task.Record, error) {
		if cur == nil || cur.Delegation == nil || cur.Delegation.Action == nil ||
			cur.Delegation.Action.ID != id || cur.Delegation.RunnerPID != rec.Delegation.RunnerPID {
			return cur, errors.New("the pending action is no longer owned by this runner")
		}
		*cur.Delegation.RoleSession(role) = s
		return cur, nil
	})
	return err
}

func (a App) callRole(ctx context.Context, rec *task.Record, adapter AgentAdapter, in agent.StartInput, action task.Action) (agent.Turn, error) {
	rec, err := a.beginAction(ctx, rec, action)
	if err != nil {
		return agent.Turn{}, err
	}
	role := action.Role()
	session := *rec.Delegation.RoleSession(role)
	if rec.Delegation.Action.Started && session.ID == "" {
		return agent.Turn{}, missingRoleSession{role}
	}
	if err := a.checkQuota(ctx, adapter.Name(), in.Log); err != nil {
		return agent.Turn{}, err
	}
	if a.metered {
		_, cancel, err := a.callContext(ctx, rec.Key, in.Step)
		if err != nil {
			return agent.Turn{}, err
		}
		cancel()
	}
	if err := a.startAttempt(ctx, rec); err != nil {
		return agent.Turn{}, err
	}
	if rec.Delegation.Action.Started {
		in.Prompt = "The previous turn for this action was interrupted. Check existing work and tracker state before writing. Reuse a completed commit or pull request when it satisfies the request.\n\n" + in.Prompt
	}
	if rec, err = a.startAction(ctx, rec); err != nil {
		return agent.Turn{}, err
	}
	if session.ID != "" {
		turn, err := adapter.Resume(ctx, session, in)
		if errors.Is(err, agent.ErrNoSession) {
			err = missingRoleSession{role}
		}
		return turn, err
	}
	var saveErr error
	turn, err := adapter.Start(ctx, in, func(s task.Session) {
		if s.ID != "" {
			saveErr = a.saveRoleSession(ctx, rec, role, s)
		}
	})
	if errors.Is(err, agent.ErrNoSession) {
		err = missingRoleSession{role}
	}
	return turn, errors.Join(err, saveErr)
}

func askForSession(rec *task.Record, role task.Role, now time.Time) {
	rec.State = task.NeedsDecision
	rec.LastOutcome = &task.Outcome{Status: "session_unavailable", Summary: missingRoleSession{role}.Error(), At: now}
	rec.Decisions = append(rec.Decisions, task.Decision{AskedAt: now, Question: task.Question{
		Issue: replaceSessionQuestion, Context: rec.LastOutcome.Summary,
		Options: []task.Option{
			{Label: "A", Description: "Start a replacement session for this role"},
			{Label: "B", Description: "Pause with the current session and work saved"},
		},
		Recommendation: "B", ImpactIfWrong: "Replacing the session loses its conversation history.",
	}})
}
