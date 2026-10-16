package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

// failedOnly selects a FAILED task in DELEGATED WORK.
func failedOnly() Model {
	m := Model{Width: 160, Height: 30}
	m.SetInbox([]app.InboxEntry{entry("a/b", 1, "First")})
	m.SetTasks([]*task.Record{record(12, task.Failed)})
	m.Focus = TaskPane
	return m
}

func TestResetConfirmsWhatItDeletesBeforeActing(t *testing.T) {
	m := failedOnly()
	if got := press(&m, rune_('x')); got.Kind != ActionNone || m.Mode != ConfirmingReset {
		t.Fatalf("action %+v mode %v", got, m.Mode)
	}
	footer := footer(m)
	for _, want := range []string{"task file", "runner log", "worktree", "local branch", "uncommitted or untracked work", "[y/n]"} {
		if !strings.Contains(footer, want) {
			t.Errorf("confirmation %q does not name %q", footer, want)
		}
	}
	got := press(&m, rune_('y'))
	if got.Kind != ActionReset || got.Reset == nil || got.Reset.Number != 12 || m.Mode != Browsing {
		t.Fatalf("action %+v mode %v", got, m.Mode)
	}
}

func TestResetCancelsOnEscapeOrAnyOtherKey(t *testing.T) {
	for _, k := range []Key{{Kind: KeyEscape}, rune_('n'), rune_('x')} {
		m := failedOnly()
		press(&m, rune_('x'))
		if got := press(&m, k); got.Kind != ActionNone || m.Mode != Browsing {
			t.Errorf("%+v gave %+v in mode %v", k, got, m.Mode)
		}
	}
}

func TestResetIsOnlyOfferedForAFailedTask(t *testing.T) {
	m := failedOnly()
	m.SetTasks([]*task.Record{record(12, task.ReadyForHuman)})
	if got := press(&m, rune_('x')); got.Kind != ActionNone || m.Mode != Browsing || !strings.Contains(m.Message, "only a FAILED task") {
		t.Fatalf("action %+v mode %v message %q", got, m.Mode, m.Message)
	}
	if strings.Contains(footer(m), "x reset") {
		t.Errorf("footer offers reset for a READY_FOR_HUMAN task: %q", footer(m))
	}
	m.SetTasks([]*task.Record{record(12, task.Failed)})
	if !strings.Contains(footer(m), "x reset") {
		t.Errorf("footer does not offer reset for a FAILED task: %q", footer(m))
	}
}

func TestAResetTaskReturnsToTheInboxForAManualStartOnly(t *testing.T) {
	m := failedOnly()
	m.AutoOn, m.AutoMax, m.githubFresh = true, 2, true
	e := entry("a/b", 12, "Failed one")
	e.Item.Labels, e.Item.Reasons = []string{"triaged"}, []task.InboxReason{task.Assigned}
	e.Delegated, e.State = true, task.Failed
	m.SetInbox([]app.InboxEntry{e})
	rec := record(12, task.Failed)
	apply(&m, resetDone(context.Background(), resetSource{rec: &task.Record{Key: rec.Key, State: task.Discovered, Reset: &task.ResetMark{PR: 9}}}, rec))
	if !strings.Contains(m.Message, "Reset a/b#12") {
		t.Errorf("message = %q", m.Message)
	}
	if got := m.Inbox[0]; got.Delegated || !got.Reset {
		t.Fatalf("inbox entry = %+v", got)
	}
	m.SetTasks(nil)
	if reqs := m.AutoStarts(); len(reqs) != 0 {
		t.Fatalf("auto mode started a reset issue: %+v", reqs)
	}
}

func TestResetShowsWhyNothingWasDeleted(t *testing.T) {
	m := failedOnly()
	rec := record(12, task.Failed)
	apply(&m, resetDone(context.Background(), resetSource{err: errors.New("HTTP 502; nothing was deleted")}, rec))
	if !strings.Contains(m.Message, "nothing was deleted") {
		t.Errorf("message = %q", m.Message)
	}
}

type resetSource struct {
	stubSource
	rec *task.Record
	err error
}

func (s resetSource) Reset(context.Context, *task.Record) (*task.Record, error) { return s.rec, s.err }
