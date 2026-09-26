package render

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

func TestInboxShowsWaitingTaskChangesOnce(t *testing.T) {
	for _, state := range []task.State{task.NeedsDecision, task.ReadyForHuman} {
		for _, withItem := range []bool{false, true} {
			t.Run(string(state)+map[bool]string{true: "/with-item", false: "/note-only"}[withItem], func(t *testing.T) {
				key := task.Key{Repo: "acme/backend", Number: 5}
				rec := &task.Record{Key: key, State: state, Title: "Retry requests",
					GitHub:     &task.Snapshot{State: "open", Reasons: []task.InboxReason{task.Assigned}},
					Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Reviewer: task.Codex, PR: 9},
				}
				var items []app.InboxItem
				if withItem {
					items = []app.InboxItem{{Key: key, Kind: task.Issue, Title: rec.Title, State: "open", Reasons: []task.InboxReason{task.Tracked}}}
				}
				notes := map[task.Key][]string{key: {"CI now passing"}}
				report, _ := app.Reconcile(items, []*task.Record{rec}, notes, true, time.Now())
				var out bytes.Buffer
				Inbox(&out, report)
				if strings.Count(out.String(), "CI now passing") != 1 || strings.Count(out.String(), "acme/backend#5") != 1 || strings.Contains(out.String(), "CHANGED") {
					t.Fatalf("waiting task must show its note once in its own section:\n%s", out.String())
				}
				if withItem && !strings.Contains(out.String(), "no longer assigned to you") {
					t.Fatalf("lost assignment hidden:\n%s", out.String())
				}
				if withItem && !strings.Contains(strings.Join(report.Current[0].Changes, ", "), "CI now passing") {
					t.Fatalf("interactive entry lost CI note: %+v", report.Current)
				}
				out.Reset()
				InboxActivity(&out, report)
				if strings.Count(out.String(), "CI now passing") != 1 || !strings.Contains(out.String(), "acme/backend#5") {
					t.Fatalf("interactive waiting activity hidden: %s", out.String())
				}
			})
		}
	}
}

func TestInteractiveActivityKeepsNonSelectableChanges(t *testing.T) {
	report := app.InboxReport{
		Changed: []app.InboxEntry{
			{Item: app.InboxItem{Key: task.Key{Repo: "acme/backend", Number: 1}, Reasons: []task.InboxReason{task.Tracked}}, Changes: []string{"no longer assigned to you"}},
			{Item: app.InboxItem{Key: task.Key{Repo: "acme/backend", Number: 2}, Reasons: []task.InboxReason{task.Assigned}}, Changes: []string{"updated"}},
			{Item: app.InboxItem{Key: task.Key{Repo: "acme/backend", Number: 4}}, Changes: []string{"PR #9 merged"}},
		},
		Warnings: []error{errors.New("refresh acme/backend#3: unavailable")},
	}
	var out bytes.Buffer
	InboxActivity(&out, report)
	if !strings.Contains(out.String(), "no longer assigned to you") || !strings.Contains(out.String(), "PR #9 merged") || strings.Contains(out.String(), "acme/backend#2") || strings.Count(out.String(), "unavailable") != 1 {
		t.Fatalf("activity: %s", out.String())
	}
}
