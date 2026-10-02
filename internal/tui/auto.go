package tui

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

// Auto is how the dashboard starts assigned INBOX issues on its own.
type Auto struct {
	Enabled bool
	Agent   task.Agent
	// Max caps active runs, manual ones included, before auto mode waits for a slot.
	Max int
	// Save keeps the choice for the next launch. Run calls it on each toggle.
	Save func(bool) error
	// LoadErr is why the saved choice could not be read. The dashboard shows it once.
	LoadErr error
}

// SetAuto applies the launch settings.
func (m *Model) SetAuto(a Auto) {
	m.AutoOn, m.AutoAgent, m.AutoMax = a.Enabled, a.Agent, a.Max
	if a.LoadErr != nil {
		m.Message = fmt.Sprintf("! read auto mode: %v; auto mode is on", a.LoadErr)
	}
}

// eligible is an open issue assigned to the developer that foreman has never
// delegated. FAILED and REVIEWED work also sits in INBOX, but resuming it is the
// developer's call.
func eligible(e app.InboxEntry) bool {
	return e.Item.Kind == task.Issue && e.Item.State == "open" && !e.Delegated &&
		slices.Contains(e.Item.Reasons, task.Assigned)
}

// active counts the slots in use: runs in WORKING or REVIEWING plus starts still in
// flight. A start that has landed but is not yet read from disk stays in starting
// until its result comes back, so it is never missed.
func (m *Model) active() int {
	n := len(m.starting)
	for _, rec := range m.Tasks {
		if (rec.State == task.Working || rec.State == task.Reviewing) && !m.starting[rec.Key] {
			n++
		}
	}
	return n
}

// AutoStarts returns the issues to start now, oldest first, and marks them as
// started. Each is tried once per dashboard session, so a failed start is not
// retried until the dashboard is reopened. The inbox is only as fresh as the last
// refresh, so an issue already in the task list is skipped even when the inbox
// still shows it as never delegated.
func (m *Model) AutoStarts() []app.StartRequest {
	if !m.AutoOn || m.tasksStale {
		return nil
	}
	free := m.AutoMax - m.active()
	if free <= 0 {
		return nil
	}
	tracked := map[task.Key]bool{}
	for _, rec := range m.Tasks {
		tracked[rec.Key] = true
	}
	var picks []app.InboxItem
	for _, e := range m.Inbox {
		k := e.Item.Key
		if eligible(e) && !m.tried[k] && !m.starting[k] && !tracked[k] {
			picks = append(picks, e.Item)
		}
	}
	slices.SortFunc(picks, func(a, b app.InboxItem) int {
		if a.CreatedAt.IsZero() != b.CreatedAt.IsZero() {
			if a.CreatedAt.IsZero() {
				return 1
			}
			return -1
		}
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.Key.Repo, b.Key.Repo), cmp.Compare(a.Key.Number, b.Key.Number))
	})
	// Starts in one repository share its managed checkout, which App.Start clones
	// when it is missing, so they go one at a time.
	busy := map[task.Repo]bool{}
	for k := range m.starting {
		busy[k.Repo] = true
	}
	var reqs []app.StartRequest
	for _, it := range picks {
		if len(reqs) == free {
			break
		}
		if busy[it.Key.Repo] {
			continue
		}
		busy[it.Key.Repo] = true
		m.tried = mark(m.tried, it.Key)
		m.starting = mark(m.starting, it.Key)
		// The task list leaves out inactive work such as PR_CLOSED, so only App.Start
		// can see that another command delegated this issue since the refresh.
		reqs = append(reqs, app.StartRequest{Repo: it.Key.Repo, Issue: it.Key.Number, Agent: m.AutoAgent, NewOnly: true})
	}
	return reqs
}

// TasksFailed records that the last task read failed. The task list on screen may
// then miss a run that just started, so auto mode cannot count its slots.
func (m *Model) TasksFailed() { m.tasksStale = true }

// Done frees the slot a start or resume held once its result comes back.
func (m *Model) Done(key task.Key) {
	delete(m.starting, key)
}

func (m *Model) toggleAuto() Action {
	m.AutoOn = !m.AutoOn
	if m.AutoOn {
		m.Message = "Auto mode on: assigned issues start on their own."
	} else {
		m.Message = "Auto mode off: running agents keep going."
	}
	return Action{Kind: ActionAuto}
}

func mark(set map[task.Key]bool, key task.Key) map[task.Key]bool {
	if set == nil {
		set = map[task.Key]bool{}
	}
	set[key] = true
	return set
}
