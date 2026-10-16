package app

import (
	"slices"

	"github.com/brymartinez/foreman/internal/task"
)

// Delegable lists the items in the current refresh that the developer can hand over.
// Pull requests are left out, because only an issue can be delegated, and so are closed
// items. An item foreman has never delegated is offered unless only a tracked re-fetch
// found it. One it has delegated is offered only when the run has stopped and is the
// developer's to move: FAILED, which starts again, and REVIEWED, which resumes. A reset
// item is offered for its manual start. Work an
// agent still holds stays in DELEGATED WORK.
func Delegable(report InboxReport) []InboxEntry {
	var choices []InboxEntry
	for _, entry := range report.Current {
		if CanDelegate(entry) {
			choices = append(choices, entry)
		}
	}
	return choices
}

// CanDelegate reports one entry against those rules. It is exported so the renderer
// can report activity for exactly the items the picker leaves out.
func CanDelegate(entry InboxEntry) bool {
	if entry.Item.Kind == task.PullRequest || entry.Item.State == "closed" || entry.Item.State == "merged" {
		return false
	}
	if entry.Delegated {
		return entry.State == task.Failed || entry.State == task.Reviewed
	}
	// A reset run was offered before, often found only by tracking, and stays offered.
	if entry.Reset {
		return true
	}
	return !slices.Contains(entry.Item.Reasons, task.Tracked)
}
