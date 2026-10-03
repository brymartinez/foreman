package tui

import (
	"context"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

// Source is everything the dashboard asks foreman to do. app.App satisfies it, and
// tests use a stub.
type Source interface {
	// InboxView is the GitHub refresh: the searches, the readiness gate, and the
	// configured cleanup. It is the expensive call, so the dashboard makes it rarely.
	// allRepos ignores the configured repository lists.
	InboxView(ctx context.Context, allRepos bool) (app.InboxReport, error)
	// Tasks re-reads delegated work from the task files. It makes no GitHub call.
	Tasks(ctx context.Context) ([]*task.Record, error)
	Start(ctx context.Context, req app.StartRequest) (*task.Record, error)
	// Route picks the profile a never-delegated issue starts with. It returns
	// app.ErrRoutingOff when there is no routing.yaml.
	Route(ctx context.Context, req app.RouteRequest) (app.Decision, error)
	Cancel(ctx context.Context, rec *task.Record) (*task.Record, error)
	// Resume continues a REVIEWED task so its agent answers the comments on its PR.
	Resume(ctx context.Context, key task.Key) (*task.Record, error)
	// Respond records an answer to the oldest pending question, as foreman respond
	// does, and relaunches the runner after the last one.
	Respond(ctx context.Context, key task.Key, raw string) (*task.Record, error)
}

var _ Source = app.App{}
