package tui

import (
	"context"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

// Source is everything the dashboard asks foreman to do. app.App satisfies it, and
// tests use a stub.
type Source interface {
	// InboxView retains the repository view for existing callers and history tests.
	InboxView(ctx context.Context, allRepos bool) (app.InboxReport, error)
	// InboxSources refreshes selected tracker issues and the readiness gate.
	InboxSources(ctx context.Context, source task.Source) (app.InboxReport, error)
	// Tasks re-reads delegated work from the task files. It makes no GitHub call.
	Tasks(ctx context.Context) ([]*task.Record, error)
	Start(ctx context.Context, req app.StartRequest) (*task.Record, error)
	ProposeBranch(ctx context.Context, req app.StartRequest) (app.BranchProposal, error)
	// Route picks the profile a never-delegated issue starts with. It returns
	// app.ErrRoutingOff when there is no routing.yaml.
	Route(ctx context.Context, req app.RouteRequest) (app.Decision, error)
	Cancel(ctx context.Context, rec *task.Record) (*task.Record, error)
	// Resume continues a REVIEWED task so its agent answers the comments on its PR.
	Resume(ctx context.Context, key task.Key) (*task.Record, error)
	// Respond records an answer to the oldest pending question, as App.Respond
	// does, and relaunches the runner after the last one.
	Respond(ctx context.Context, key task.Key, raw string) (*task.Record, error)
	// History lists the merged and closed PRs foreman opened, newest first. It reads
	// each PR from GitHub, so the dashboard calls it only when the view opens or reloads.
	History(ctx context.Context, allRepos bool) ([]app.HistoryRow, []error, error)
}

var _ Source = app.App{}
