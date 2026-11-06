package app

import (
	"context"
	"errors"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// ErrRoutingOff means there is no routing.yaml, so dashboard starts use the agent
// the developer or auto.agent names.
var ErrRoutingOff = errors.New("routing is off")

type RouteRequest struct {
	Key         task.Key // source-qualified inbox identity; legacy callers use Repo and Issue
	Repo        task.Repo
	Issue       int
	ContextFile string
	// Prior reuses an earlier selection, so a route retried after a wait does not
	// ask the selector again.
	Prior *agent.Selection
}

// Decision is a routing result. Exactly one of Profile and Wait is set.
type Decision struct {
	Profile *task.Profile
	Reason  string
	// Wait says why no suitable profile can run now.
	Wait string
	// Selection is the selector's answer, kept for a retry.
	Selection agent.Selection
}

// Route resolves the configured default profile without a model call.
func (a App) Route(ctx context.Context, _ RouteRequest) (Decision, error) {
	if a.RoutingErr != nil {
		return Decision{}, a.RoutingErr
	}
	cfg := a.Routing
	if cfg == nil {
		return Decision{}, ErrRoutingOff
	}
	p := cfg.Resolve(cfg.Default, a.RoutingDefaults)
	if err := a.checkQuota(ctx, p.Agent, nil); err != nil {
		return Decision{Wait: err.Error(), Reason: "default profile unavailable"}, nil
	}
	return Decision{Profile: &p, Reason: "configured default profile"}, nil
}
