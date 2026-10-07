// Package quota reads Claude and Codex availability from the optional quota-axi CLI.
package quota

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

// DefaultTimeout bounds one quota-axi call, which asks each vendor in turn.
const DefaultTimeout = 10 * time.Second

// Axi runs `quota-axi --provider claude,codex --json`.
type Axi struct {
	Command string
	Exec    agent.Exec
	// Timeout is DefaultTimeout when zero.
	Timeout time.Duration
}

type report struct {
	Providers []struct {
		Provider string `json:"provider"`
		NotSetUp bool   `json:"notSetUp"`
		State    struct {
			Stale bool `json:"stale"`
		} `json:"state"`
		QuotaSemantics struct {
			EffectiveAvailability []struct {
				Scope   string   `json:"scope"`
				Status  string   `json:"status"`
				Percent *float64 `json:"effectivePercentRemaining"`
			} `json:"effectiveAvailability"`
		} `json:"quotaSemantics"`
	} `json:"providers"`
}

// Quota reads both agents. quota-axi exits 1 when a provider fails but still prints
// the report, so a report is read whatever the exit code. An agent is exhausted
// only when a fresh reading says nothing is left across all of its models.
func (q Axi) Quota(ctx context.Context) (map[task.Agent]app.Quota, error) {
	ctx, cancel := context.WithTimeout(ctx, cmp.Or(q.Timeout, DefaultTimeout))
	defer cancel()
	var out bytes.Buffer
	runErr := q.Exec(ctx, agent.Command{Name: cmp.Or(q.Command, "quota-axi"), Args: []string{"--provider", "claude,codex", "--json"}, Stdout: &out, Stderr: io.Discard})
	if errors.Is(runErr, exec.ErrNotFound) {
		return nil, errors.New("quota-axi is not installed")
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("quota-axi: %w", ctx.Err())
	}
	var r report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.Providers == nil {
		return nil, fmt.Errorf("quota-axi printed no report: %v", cmp.Or(runErr, err))
	}
	quotas := map[task.Agent]app.Quota{}
	for _, p := range r.Providers {
		a, err := task.ParseAgent(p.Provider)
		if err != nil || p.State.Stale {
			continue
		}
		if p.NotSetUp {
			quotas[a] = app.Quota{Status: app.QuotaNotSetUp}
			continue
		}
		for _, e := range p.QuotaSemantics.EffectiveAvailability {
			if e.Scope != "all_models" || e.Status != "known" || e.Percent == nil {
				continue
			}
			status := app.QuotaAvailable
			if *e.Percent <= 0 {
				status = app.QuotaExhausted
			}
			quotas[a] = app.Quota{Status: status, Percent: *e.Percent}
		}
	}
	return quotas, nil
}
