package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

// workspaceTimeout bounds one call to the shared store from a command that has
// other work to do.
const workspaceTimeout = 30 * time.Second

func (a App) workspaceLogf(format string, args ...any) {
	if a.WorkspaceLog != nil {
		a.WorkspaceLog(format, args...)
	}
}

// publish writes a task's saved state to the shared store, best effort: a failure
// is logged and never changes the task. The next refresh retries it.
func (a App) publish(ctx context.Context, key task.Key) {
	if a.Workspace == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workspaceTimeout)
	defer cancel()
	if err := a.Workspace.Publish(ctx, key); err != nil {
		a.workspaceLogf("workspace: publish %s: %v", key.ExternalID+fmt.Sprint(key.Number), err)
	}
}

// publishKnowledge writes a repository's knowledge to the shared store, best effort.
func (a App) publishKnowledge(ctx context.Context, repo string) {
	if a.Workspace == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workspaceTimeout)
	defer cancel()
	if err := a.Workspace.PublishKnowledge(ctx, repo); err != nil {
		a.workspaceLogf("workspace: publish knowledge of %s: %v", repo, err)
	}
}

// refreshWorkspace pulls from the shared store, applies the answers people typed,
// and starts the tasks they queued. It returns what went wrong as warnings, so a
// store outage never stops the inbox. A nil Workspace does nothing.
func (a App) refreshWorkspace(ctx context.Context) []error {
	if a.Workspace == nil {
		return nil
	}
	pull, err := a.Workspace.Pull(ctx)
	if err != nil {
		return []error{fmt.Errorf("workspace: %w", err)}
	}
	var warns []error
	for _, ans := range pull.Answers {
		if err := a.applyAnswer(ctx, ans); err != nil {
			warns = append(warns, fmt.Errorf("workspace: answer for %s: %w", ans.Key.ExternalID+fmt.Sprint(ans.Key.Number), err))
		}
	}
	warns = append(warns, a.startQueued(ctx, pull.Queued)...)
	return warns
}

// applyAnswer records an answer through Respond, which resumes the same run and
// provider session, then acks it. A question someone already answered elsewhere is
// acked too, so a stale answer is cleared.
func (a App) applyAnswer(ctx context.Context, ans WorkspaceAnswer) error {
	if _, err := a.respond(ctx, ans.Key, ans.Text, ans.Question); errors.Is(err, errQuestionMoved) {
		return nil // the next refresh judges the answer again against the new question
	} else if err != nil {
		cur, gerr := a.Store.Get(ctx, ans.Key)
		if gerr != nil || (cur != nil && cur.Pending() != nil) {
			return err
		}
	}
	return a.Workspace.Ack(ctx, WorkspaceAck{Key: ans.Key, Kind: AckAnswer, Token: ans.Token, Question: ans.Question})
}

// startQueued starts queued tasks, oldest first, under the rules auto mode uses:
// confirmed triage, one repository, routing and quota, the concurrency limit, and
// the branch proposal. Auto mode itself need not be on. A task that cannot start
// keeps its Queue box and shows why.
func (a App) startQueued(ctx context.Context, queued []WorkspaceQueued) []error {
	if len(queued) == 0 {
		return nil
	}
	recs, err := a.Store.List(ctx)
	if err != nil {
		return []error{fmt.Errorf("workspace: list tasks: %w", err)}
	}
	active := 0
	for _, r := range recs {
		if r.State.Active() {
			active++
		}
	}
	active += startingElsewhere(recs)
	var warns []error
	for _, q := range queued {
		if existing := findTask(recs, q.Key); existing != nil && existing.Delegation != nil && existing.State == task.Failed {
			// A start that failed leaves a delegation behind. The request was not met:
			// keep Queue ticked and show why. Starting over is the developer's call.
			reason := "the last start failed; resume or reset the task from the dashboard"
			if existing.LastOutcome != nil && existing.LastOutcome.Summary != "" {
				reason = existing.LastOutcome.Summary
			}
			a.blockQueued(ctx, q.Key, reason)
			continue
		} else if existing != nil && existing.Delegation != nil {
			// It already has a run; the request is met, or resuming is the developer's call.
			if err := a.Workspace.Ack(ctx, WorkspaceAck{Key: existing.Key, Kind: AckQueue, Token: q.Token}); err != nil {
				warns = append(warns, err)
			}
			continue
		}
		if a.MaxConcurrent > 0 && active >= a.MaxConcurrent {
			a.blockQueued(ctx, q.Key, fmt.Sprintf("waiting for a free slot: %d runs are active and auto.max_concurrent is %d", active, a.MaxConcurrent))
			continue
		}
		rec, reason, err := a.startOne(ctx, q.Key)
		switch {
		case err != nil:
			a.blockQueued(ctx, q.Key, err.Error())
			warns = append(warns, fmt.Errorf("workspace: start %s: %w", q.Key.ExternalID+fmt.Sprint(q.Key.Number), err))
		case reason != "":
			a.blockQueued(ctx, q.Key, reason)
		default:
			active++
			recs = append(recs, rec)
			if err := a.Workspace.Ack(ctx, WorkspaceAck{Key: rec.Key, Kind: AckQueue, Token: q.Token}); err != nil {
				warns = append(warns, err)
			}
		}
	}
	return warns
}

// startsInFlight holds the tasks this process is starting. A start takes a slot
// only when its first update commits, so a queued start that counts runs from the
// database alone can pass the limit while the dashboard has another start under way.
var (
	startsMu       sync.Mutex
	startsInFlight = map[task.Key]int{}
)

func trackStart(key task.Key, repo task.Repo, issue int) func() {
	if key == (task.Key{}) {
		key = task.Key{Repo: repo, Number: issue}
	}
	startsMu.Lock()
	startsInFlight[key]++
	startsMu.Unlock()
	return func() {
		startsMu.Lock()
		if startsInFlight[key]--; startsInFlight[key] <= 0 {
			delete(startsInFlight, key)
		}
		startsMu.Unlock()
	}
}

// startingElsewhere counts starts under way that the task list does not show active.
func startingElsewhere(recs []*task.Record) int {
	startsMu.Lock()
	defer startsMu.Unlock()
	n := 0
	for k := range startsInFlight {
		if r := findTask(recs, k); r == nil || !r.State.Active() {
			n++
		}
	}
	return n
}

func findTask(recs []*task.Record, key task.Key) *task.Record {
	for _, r := range recs {
		if r.Key == key || key.Source == task.Local && r.Key.Source == task.Local && r.Key.ExternalID == key.ExternalID {
			return r
		}
	}
	return nil
}

func (a App) blockQueued(ctx context.Context, key task.Key, reason string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workspaceTimeout)
	defer cancel()
	if err := a.Workspace.Blocked(ctx, key, reason); err != nil {
		a.workspaceLogf("workspace: publish the reason for %s: %v", key.ExternalID+fmt.Sprint(key.Number), err)
	}
}

// startOne starts one queued task. A reason with a nil error means the task cannot
// start yet and why.
func (a App) startOne(ctx context.Context, key task.Key) (*task.Record, string, error) {
	req := StartRequest{Repo: key.Repo, Issue: key.Number, NewOnly: true}
	if key.Source != task.Local && !a.repoVisible(key.Repo, false) {
		return nil, fmt.Sprintf("%s is excluded by the repository filters", key.Repo), nil
	}
	if key.Source == task.Local {
		if a.Triage != nil {
			text, err := a.Triage.Read(key)
			if err != nil {
				return nil, "", err
			}
			if strings.TrimSpace(text) == "" {
				return nil, "confirmed triage is missing: fill Confirmed triage and tick Scope confirmed", nil
			}
		}
		req.Key = key
		if key.Repo == "" {
			p, err := a.ProposeRepository(ctx, key)
			if err != nil {
				return nil, "", fmt.Errorf("repository selection: %w", err)
			}
			if p.Repo == "" {
				return nil, "repository needs a choice: " + p.Reason, nil
			}
			req.Repo, req.Key.Repo = p.Repo, p.Repo
		}
		if !a.repoVisible(req.Repo, false) {
			return nil, fmt.Sprintf("%s is excluded by the repository filters", req.Repo), nil
		}
	}
	d, err := a.Route(ctx, RouteRequest{Key: req.Key, Repo: req.Repo, Issue: req.Issue})
	switch {
	case errors.Is(err, ErrRoutingOff):
		req.Agent = a.Models.Implementation.Agent
		if req.Agent == "" {
			req.Agent = task.Claude
		}
	case err != nil:
		return nil, "", fmt.Errorf("route: %w", err)
	case d.Profile == nil:
		return nil, "waiting: " + d.Wait, nil
	default:
		req.Profile, req.RouteReason = d.Profile, d.Reason
	}
	p, err := a.ProposeBranch(ctx, req)
	if err != nil {
		return nil, "", fmt.Errorf("branch proposal: %w", err)
	}
	if p.Uncertain {
		return nil, "branch needs a choice: " + p.Reason, nil
	}
	req.BranchName = p.Name
	rec, err := a.Start(ctx, req)
	if err != nil {
		if errors.Is(err, ErrAwaitingTriage) {
			return nil, "confirmed triage is missing: fill Confirmed triage and tick Scope confirmed", nil
		}
		return nil, "", err
	}
	if rec.State == task.Failed {
		summary := "the start failed"
		if rec.LastOutcome != nil && rec.LastOutcome.Summary != "" {
			summary = rec.LastOutcome.Summary
		}
		return nil, summary, nil
	}
	if rec == nil {
		return nil, "the start returned no task", nil
	}
	return rec, "", nil
}
