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
	return fmt.Sprintf("%s quota exhausted; retry after its usage limit resets", e.Agent)
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
