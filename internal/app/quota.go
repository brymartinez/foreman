package app

import (
	"context"
	"fmt"
	"io"

	"github.com/brymartinez/foreman/internal/task"
)

type quotaUnavailable struct {
	Agent  task.Agent
	Status QuotaStatus
}

func (e quotaUnavailable) Error() string {
	if e.Status == QuotaNotSetUp {
		return fmt.Sprintf("%s not set up", e.Agent)
	}
	return fmt.Sprintf("%s quota exhausted; retry after its usage limit resets or continue with another agent", e.Agent)
}

func quotaError(name task.Agent, quotas map[task.Agent]Quota) error {
	switch quotas[name].Status {
	case QuotaExhausted, QuotaNotSetUp:
		return quotaUnavailable{Agent: name, Status: quotas[name].Status}
	}
	return nil
}

func (a App) readQuotas(ctx context.Context, log io.Writer) map[task.Agent]Quota {
	if a.Quota == nil {
		return nil
	}
	q, err := a.Quota.Quota(ctx)
	if err != nil {
		if log != nil {
			fmt.Fprintf(log, "foreman: quota unknown: %v\n", err)
		}
		return nil
	}
	return q
}

func (a App) checkQuota(ctx context.Context, name task.Agent, log io.Writer) error {
	return quotaError(name, a.readQuotas(ctx, log))
}

// quotaImplementer changes provider only on confirmed unavailability, and only when
// the other provider is positively known to be available.
func (a App) quotaImplementer(want task.Agent, q map[task.Agent]Quota) (task.Agent, error) {
	if err := quotaError(want, q); err != nil {
		other := want.Reviewer()
		if q[other].Status == QuotaAvailable && a.Agents[other] != nil {
			return other, nil
		}
		return want, err
	}
	return want, nil
}

func (a App) handoffForQuota(ctx context.Context, rec *task.Record, log io.Writer) (*task.Record, error) {
	from := rec.Delegation.Implementer.Agent
	to, err := a.quotaImplementer(from, a.readQuotas(ctx, log))
	if err != nil || to == from {
		return rec, err
	}
	updated, err := a.update(ctx, rec.Key, existing(func(cur *task.Record) {
		d := cur.Delegation
		d.Implementer = task.Session{Agent: to}
		d.Reviewer = to.Reviewer()
		d.Profile, d.RouteReason, d.RouteLogged = nil, "", false
	}))
	if err == nil {
		fmt.Fprintf(log, "foreman: %s unavailable; %s takes over the existing branch and pending stage\n", from, to)
	}
	return updated, err
}
