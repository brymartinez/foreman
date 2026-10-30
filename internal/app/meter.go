package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// meterAgents wraps a runner's copy of the adapters. Other app users keep their
// original adapters, and each call is saved before its outcome is processed.
func (a App) meterAgents(key task.Key) App {
	a.metered = true
	adapters := make(map[task.Agent]AgentAdapter, len(a.Agents))
	for name, adapter := range a.Agents {
		adapters[name] = meteredAgent{AgentAdapter: adapter, app: a, key: key}
	}
	a.Agents = adapters
	return a
}

type meteredAgent struct {
	AgentAdapter
	app App
	key task.Key
}

func planningCall(step string) bool { return step == agent.StepPlan || step == "plan_check" }

func (a App) callContext(ctx context.Context, key task.Key, step string) (context.Context, context.CancelFunc, error) {
	rec, err := a.Store.Get(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	if rec == nil || rec.Delegation == nil {
		return nil, nil, errors.New("task no longer exists")
	}
	if a.TaskTokenLimit > 0 && rec.Usage.Tokens() >= a.TaskTokenLimit {
		return nil, nil, fmt.Errorf("task token limit reached (%d/%d); raise budgets.task_tokens to continue", rec.Usage.Tokens(), a.TaskTokenLimit)
	}
	if planningCall(step) && a.PlanningTokenLimit > 0 {
		var tokens int64
		for _, call := range rec.Calls {
			if planningCall(call.Step) {
				tokens += call.Usage.Tokens()
			}
		}
		if tokens >= a.PlanningTokenLimit {
			return nil, nil, fmt.Errorf("planning token limit reached (%d/%d); raise budgets.planning_tokens to continue", tokens, a.PlanningTokenLimit)
		}
	}
	if a.CallTimeout > 0 {
		child, cancel := context.WithTimeout(ctx, a.CallTimeout)
		return child, cancel, nil
	}
	child, cancel := context.WithCancel(ctx)
	return child, cancel, nil
}

func (a App) saveCall(ctx context.Context, key task.Key, name task.Agent, step string, start time.Time, usage task.Usage, callErr error) error {
	call := task.ModelCall{Agent: name, Step: step, Model: usage.Model, StartedAt: start, FinishedAt: a.Now(), Usage: usage}
	if call.Model == "" {
		call.Model = "CLI default (unreported)"
	}
	if callErr != nil {
		call.Error = callErr.Error()
	}
	_, err := a.update(context.WithoutCancel(ctx), key, existing(func(cur *task.Record) {
		cur.Calls = append(cur.Calls, call)
		addUsage(&cur.Usage, usage)
	}))
	return err
}

func (m meteredAgent) turn(ctx context.Context, in agent.StartInput, run func(context.Context) (agent.Turn, error)) (agent.Turn, error) {
	child, cancel, err := m.app.callContext(ctx, m.key, cmp.Or(in.Purpose, in.Step))
	if err != nil {
		return agent.Turn{}, err
	}
	defer cancel()
	start := m.app.Now()
	out, err := run(child)
	if child.Err() != nil {
		err = fmt.Errorf("agent call stopped (%v): %w", err, child.Err())
	}
	saveErr := m.app.saveCall(ctx, m.key, m.Name(), cmp.Or(in.Purpose, in.Step), start, out.Usage, err)
	return out, errors.Join(err, saveErr)
}

func (m meteredAgent) Start(ctx context.Context, in agent.StartInput, onSession func(task.Session)) (agent.Turn, error) {
	return m.turn(ctx, in, func(child context.Context) (agent.Turn, error) { return m.AgentAdapter.Start(child, in, onSession) })
}

func (m meteredAgent) Resume(ctx context.Context, session task.Session, in agent.StartInput) (agent.Turn, error) {
	return m.turn(ctx, in, func(child context.Context) (agent.Turn, error) { return m.AgentAdapter.Resume(child, session, in) })
}

func (m meteredAgent) Review(ctx context.Context, in agent.StartInput) (agent.ReviewOutcome, error) {
	child, cancel, err := m.app.callContext(ctx, m.key, cmp.Or(in.Purpose, in.Step))
	if err != nil {
		return agent.ReviewOutcome{}, err
	}
	defer cancel()
	start := m.app.Now()
	out, err := m.AgentAdapter.Review(child, in)
	if child.Err() != nil {
		err = fmt.Errorf("agent call stopped (%v): %w", err, child.Err())
	}
	saveErr := m.app.saveCall(ctx, m.key, m.Name(), cmp.Or(in.Purpose, in.Step), start, out.Usage, err)
	return out, errors.Join(err, saveErr)
}
