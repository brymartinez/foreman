package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

var day0 = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

// assigned is an open issue the assignee search found, created n days after day0.
func assigned(repo task.Repo, n, days int) app.InboxEntry {
	e := entry(repo, n, "Issue")
	e.Item.State = "open"
	e.Item.CreatedAt = day0.AddDate(0, 0, days)
	e.Item.Reasons = []task.InboxReason{task.Assigned}
	return e
}

func autoModel(max int, inbox ...app.InboxEntry) Model {
	m := Model{Width: 100, Height: 30}
	m.SetAuto(Auto{Enabled: true, Agent: task.Claude, Max: max})
	m.SetInbox(inbox)
	return m
}

func startedKeys(reqs []app.StartRequest) []task.Key {
	var keys []task.Key
	for _, r := range reqs {
		keys = append(keys, task.Key{Repo: r.Repo, Number: r.Issue})
	}
	return keys
}

func key(n int) task.Key { return task.Key{Repo: "a/b", Number: n} }

// own is an assigned issue alone in its repository, so the one start per repository
// rule does not hold it back.
func own(n, days int) app.InboxEntry { return assigned(ownKey(n).Repo, n, days) }

func ownKey(n int) task.Key { return task.Key{Repo: task.Repo(fmt.Sprintf("r%d/x", n)), Number: n} }

func ownRecord(n int, state task.State) *task.Record {
	rec := record(n, state)
	rec.Key = ownKey(n)
	return rec
}

func TestAutoStartsOnlyAssignedUndelegatedOpenIssues(t *testing.T) {
	mention := own(2, 0)
	mention.Item.Reasons = []task.InboxReason{task.Mentioned}
	failed := own(3, 0)
	failed.Delegated, failed.State = true, task.Failed
	reviewed := own(4, 0)
	reviewed.Delegated, reviewed.State = true, task.Reviewed
	pr := own(5, 0)
	pr.Item.Kind = task.PullRequest
	closed := own(6, 0)
	closed.Item.State = "closed"
	both := own(7, 1)
	both.Item.Reasons = []task.InboxReason{task.Mentioned, task.Assigned}

	m := autoModel(10, own(1, 0), mention, failed, reviewed, pr, closed, both)
	got := startedKeys(m.AutoStarts())
	if want := []task.Key{ownKey(1), ownKey(7)}; !slices.Equal(got, want) {
		t.Fatalf("auto started %v; want only the assigned, never delegated, open issues %v", got, want)
	}
}

func TestAutoStartsOldestIssueFirst(t *testing.T) {
	undated := assigned("u/u", 1, 0)
	undated.Item.CreatedAt = time.Time{}
	m := autoModel(10, undated, assigned("n/n", 9, 5), assigned("z/z", 3, 2), assigned("a/a", 4, 2), assigned("a/b", 2, 2))
	got := startedKeys(m.AutoStarts())
	want := []task.Key{{Repo: "a/a", Number: 4}, key(2), {Repo: "z/z", Number: 3}, {Repo: "n/n", Number: 9}, {Repo: "u/u", Number: 1}}
	if !slices.Equal(got, want) {
		t.Fatalf("auto start order = %v; want oldest first, repo and number breaking ties, undated last: %v", got, want)
	}
}

func TestAutoStartUsesConfiguredAgentAndNoContext(t *testing.T) {
	m := Model{}
	m.SetAuto(Auto{Enabled: true, Agent: task.Codex, Max: 3})
	m.SetInbox([]app.InboxEntry{assigned("a/b", 1, 0)})
	got := m.AutoStarts()
	if len(got) != 1 || got[0] != (app.StartRequest{Repo: "a/b", Issue: 1, Agent: task.Codex, NewOnly: true}) {
		t.Fatalf("auto start = %+v; want codex, no context file, and only if never delegated", got)
	}
}

func TestAutoFillsOnlyFreeSlots(t *testing.T) {
	m := autoModel(3, own(1, 0), own(2, 1), own(3, 2))
	m.SetTasks([]*task.Record{record(10, task.Working), record(11, task.Failed)})
	if got := startedKeys(m.AutoStarts()); !slices.Equal(got, []task.Key{ownKey(1), ownKey(2)}) {
		t.Fatalf("with one run active auto started %v; want the two oldest", got)
	}
	if got := m.AutoStarts(); len(got) != 0 {
		t.Fatalf("auto started %v while every slot is taken", got)
	}

	// Both starts land as WORKING, then the first run finishes.
	m.Done(ownKey(1))
	m.Done(ownKey(2))
	m.SetTasks([]*task.Record{record(10, task.Working), ownRecord(1, task.Working), ownRecord(2, task.Working)})
	if got := m.AutoStarts(); len(got) != 0 {
		t.Fatalf("auto started %v with three runs active", got)
	}
	m.SetTasks([]*task.Record{record(10, task.Working), ownRecord(1, task.NeedsDecision), ownRecord(2, task.Working)})
	if got := startedKeys(m.AutoStarts()); !slices.Equal(got, []task.Key{ownKey(3)}) {
		t.Fatalf("a freed slot started %v; want the next issue", got)
	}
}

func TestAutoCountsManualAndInFlightStartsAgainstTheLimit(t *testing.T) {
	m := autoModel(2, own(1, 0), own(2, 1), own(3, 2))
	m.InboxCursor = 2
	if a := press(&m, Key{Kind: KeyEnter}, rune_('1'), Key{Kind: KeyEnter}); a.Kind != ActionStart {
		t.Fatalf("manual start = %+v", a)
	}
	if got := startedKeys(m.AutoStarts()); !slices.Equal(got, []task.Key{ownKey(1)}) {
		t.Fatalf("with a manual start in flight auto started %v; want one issue", got)
	}
	// The manual start lands before the next local read: it still holds its slot.
	m.Done(ownKey(3))
	m.SetTasks([]*task.Record{ownRecord(3, task.Working)})
	if got := m.AutoStarts(); len(got) != 0 {
		t.Fatalf("auto started %v with a manual run and an auto start in flight", got)
	}
}

func TestManualStartStillWorksWhenFull(t *testing.T) {
	m := autoModel(1, assigned("a/b", 1, 0), assigned("a/b", 2, 1))
	m.SetTasks([]*task.Record{record(10, task.Working)})
	if got := m.AutoStarts(); len(got) != 0 {
		t.Fatalf("auto started %v with the limit full", got)
	}
	m.InboxCursor = 1
	a := press(&m, Key{Kind: KeyEnter}, rune_('2'), Key{Kind: KeyEnter})
	if a.Kind != ActionStart || a.Start.Issue != 2 || a.Start.Agent != task.Codex {
		t.Fatalf("manual start with the limit full = %+v", a)
	}
}

func TestAutoOffStartsNothing(t *testing.T) {
	m := autoModel(3, assigned("a/b", 1, 0))
	m.AutoOn = false
	if got := m.AutoStarts(); len(got) != 0 {
		t.Fatalf("auto mode off started %v", got)
	}
}

func TestAutoOnStartsFromTheLastRefresh(t *testing.T) {
	m := autoModel(3, assigned("a/b", 1, 0))
	m.AutoOn = false
	if a := press(&m, rune_('a')); a.Kind != ActionAuto || !m.AutoOn {
		t.Fatalf("a with auto off = %+v, on %t", a, m.AutoOn)
	}
	if got := startedKeys(m.AutoStarts()); !slices.Equal(got, []task.Key{key(1)}) {
		t.Fatalf("turning auto on started %v; want the issue from the last refresh", got)
	}
}

func TestAutoDoesNotRetryAFailedStart(t *testing.T) {
	m := autoModel(3, assigned("a/b", 1, 0))
	m.AutoStarts()
	m.Done(key(1)) // Start returned an error, so no task was recorded.
	m.SetInbox([]app.InboxEntry{assigned("a/b", 1, 0)})
	if got := m.AutoStarts(); len(got) != 0 {
		t.Fatalf("a refresh retried the failed start: %v", got)
	}
}

func TestAutoContinuesAfterAFailedStart(t *testing.T) {
	m := autoModel(1, assigned("a/b", 1, 0), assigned("a/b", 2, 1))
	m.AutoStarts()
	m.Done(key(1))
	if got := startedKeys(m.AutoStarts()); !slices.Equal(got, []task.Key{key(2)}) {
		t.Fatalf("after a failed start auto started %v; want the next issue", got)
	}
}

func TestAutoNeverStartsTheSameIssueTwice(t *testing.T) {
	m := autoModel(3, assigned("a/b", 1, 0), assigned("a/b", 2, 1))
	m.SetTasks([]*task.Record{record(2, task.NeedsDecision)})
	if got := startedKeys(m.AutoStarts()); !slices.Equal(got, []task.Key{key(1)}) {
		t.Fatalf("auto started %v; want only the issue with no task", got)
	}
	m.Done(key(1))
	// Issue 1's run has not been read from disk yet, and the inbox is stale.
	m.SetTasks([]*task.Record{record(2, task.NeedsDecision)})
	if got := m.AutoStarts(); len(got) != 0 {
		t.Fatalf("auto started %v again", got)
	}
}

func TestAKeyTogglesAutoAndReportsTheState(t *testing.T) {
	m := autoModel(3)
	if a := press(&m, rune_('a')); a.Kind != ActionAuto || m.AutoOn || !strings.Contains(m.Message, "Auto mode off") {
		t.Fatalf("first a = %+v, on %t, message %q", a, m.AutoOn, m.Message)
	}
	if a := press(&m, rune_('a')); a.Kind != ActionAuto || !m.AutoOn || !strings.Contains(m.Message, "Auto mode on") {
		t.Fatalf("second a = %+v, on %t, message %q", a, m.AutoOn, m.Message)
	}
}

func TestHeaderShowsAutoStateAndSlots(t *testing.T) {
	m := autoModel(3)
	m.SetTasks([]*task.Record{record(10, task.Working), record(11, task.Failed)})
	if got := plain(Frame(m))[0]; !strings.Contains(got, "auto on 1/3") {
		t.Errorf("header = %q; want auto on 1/3", got)
	}
	m.AutoOn = false
	if got := plain(Frame(m))[0]; !strings.Contains(got, "auto off") {
		t.Errorf("header = %q; want auto off", got)
	}
}

func TestAutoSkipsAnIssueStartedByHand(t *testing.T) {
	m := autoModel(3, assigned("a/b", 1, 0))
	m.AutoOn = false
	press(&m, Key{Kind: KeyEnter}, rune_('1'), Key{Kind: KeyEnter})
	m.Done(key(1)) // the run finished and left the task list before the next refresh
	m.AutoOn = true
	if got := m.AutoStarts(); len(got) != 0 {
		t.Fatalf("auto started %v, which was started by hand this session", got)
	}
}

func TestAnUnreadableAutoStateIsShownAndLeavesAutoOn(t *testing.T) {
	var m Model
	m.SetAuto(Auto{Enabled: true, Agent: task.Claude, Max: 3, LoadErr: errors.New("state.json: bad json")})
	if !m.AutoOn || !strings.Contains(m.Message, "state.json: bad json") {
		t.Fatalf("on %t, message %q; want auto on and the read error shown", m.AutoOn, m.Message)
	}
}

func TestAutoStartsOneIssuePerRepositoryAtATime(t *testing.T) {
	// Starts in one repository share its checkout, which App.Start may be cloning.
	m := autoModel(3, assigned("a/b", 1, 0), assigned("a/b", 2, 1), assigned("c/d", 3, 2))
	if got := startedKeys(m.AutoStarts()); !slices.Equal(got, []task.Key{key(1), {Repo: "c/d", Number: 3}}) {
		t.Fatalf("auto started %v; want one issue per repository", got)
	}
	if got := m.AutoStarts(); len(got) != 0 {
		t.Fatalf("auto started %v while a/b#1 is still starting", got)
	}
	m.Done(key(1))
	if got := startedKeys(m.AutoStarts()); !slices.Equal(got, []task.Key{key(2)}) {
		t.Fatalf("after a/b#1 came back auto started %v; want a/b#2", got)
	}
}

func TestAutoWaitsForAManualStartInTheSameRepository(t *testing.T) {
	m := autoModel(3, assigned("a/b", 1, 0), assigned("a/b", 2, 1))
	m.InboxCursor = 1
	press(&m, Key{Kind: KeyEnter}, rune_('1'), Key{Kind: KeyEnter})
	if got := m.AutoStarts(); len(got) != 0 {
		t.Fatalf("auto started %v while a manual start in a/b is in flight", got)
	}
}

func TestAutoWaitsWhileTheTaskListCannotBeRead(t *testing.T) {
	m := autoModel(3, assigned("a/b", 1, 0))
	m.TasksFailed()
	if got := m.AutoStarts(); len(got) != 0 {
		t.Fatalf("auto started %v with a stale task list", got)
	}
	m.SetTasks(nil)
	if got := startedKeys(m.AutoStarts()); !slices.Equal(got, []task.Key{key(1)}) {
		t.Fatalf("after a good read auto started %v; want a/b#1", got)
	}
}

func TestAutoStartsNothingInTheAllView(t *testing.T) {
	m := autoModel(3)
	m.AllRepos = true
	all := app.InboxReport{Current: []app.InboxEntry{assigned("a/b", 1, 0)}}
	apply(&m, result{report: &all, allRepos: true})
	if got := m.AutoStarts(); len(got) != 0 {
		t.Fatalf("auto starts = %v; want none from the all repositories view", startedKeys(got))
	}
	if got := plain(Frame(m))[0]; !strings.Contains(got, "auto paused") {
		t.Errorf("header = %q; want auto paused", got)
	}

	// Back in the configured view, nothing starts until its refresh fills the inbox.
	press(&m, rune_('v'))
	if got := m.AutoStarts(); len(got) != 0 {
		t.Fatalf("auto starts = %v; want none before the configured view's refresh", startedKeys(got))
	}
	configured := app.InboxReport{Current: []app.InboxEntry{assigned("a/b", 1, 0)}}
	apply(&m, result{report: &configured})
	if got := m.AutoStarts(); len(got) != 1 {
		t.Errorf("auto starts = %v; want the configured view's issue", startedKeys(got))
	}
}
