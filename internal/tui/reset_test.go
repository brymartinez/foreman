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
	m := Model{Width: 160, Height: 30, TaskFile: func(k task.Key) string { return "/tasks/a-b-12.md" }}
	m.SetInbox([]app.InboxEntry{entry("a/b", 1, "First")})
	rec := record(12, task.Failed)
	rec.Delegation.Worktree, rec.Delegation.Branch = "/wt/a-b/feat/12-retries", "feat/12-retries"
	m.SetTasks([]*task.Record{rec})
	m.Focus = TaskPane
	return m
}

// resetTargets are what the confirmation for failedOnly's task must name.
var resetTargets = []string{"/tasks/a-b-12.md", "/logs/b.log", "/wt/a-b/feat/12-retries", "feat/12-retries", "uncommitted or untracked work", "keeps only the PR link", "y reset"}

func TestResetConfirmsWhatItDeletesBeforeActing(t *testing.T) {
	m := failedOnly()
	if got := press(&m, rune_('x')); got.Kind != ActionNone || m.Mode != ConfirmingReset {
		t.Fatalf("action %+v mode %v", got, m.Mode)
	}
	text := frameText(m)
	for _, want := range resetTargets {
		if !strings.Contains(text, want) {
			t.Errorf("confirmation does not name %q:\n%s", want, strings.Join(plain(Frame(m)), "\n"))
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
	apply(&m, resetDone(context.Background(), resetSource{err: errors.New("nothing was deleted: HTTP 502")}, rec))
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

// frameText joins the frame's rows, so a phrase wrapped onto the next row still reads whole.
func frameText(m Model) string {
	var rows []string
	for _, l := range plain(Frame(m)) {
		rows = append(rows, strings.Join(strings.Fields(l), " "))
	}
	return strings.Join(rows, " ")
}

// Paths wrap at any column, so the rows are compared with spaces removed.
func TestResetConfirmationShowsEveryTargetAtTheSmallestSize(t *testing.T) {
	for _, logOpen := range []bool{false, true} {
		m := failedOnly()
		m.Width, m.Height, m.LogOpen = minWidth, minHeight, logOpen
		press(&m, rune_('x'))
		if got := len(Frame(m)); got != m.Height {
			t.Fatalf("log %v: frame has %d rows, want %d", logOpen, got, m.Height)
		}
		text := strings.Join(plain(Frame(m)), "")
		text = strings.Join(strings.Fields(text), " ")
		for _, want := range resetTargets {
			if !strings.Contains(strings.ReplaceAll(text, " ", ""), strings.ReplaceAll(want, " ", "")) {
				t.Errorf("log %v: confirmation does not show %q:\n%s", logOpen, want, strings.Join(plain(Frame(m)), "\n"))
			}
		}
	}
}

func TestResetIsNotConfirmedWhileTheTargetsDoNotFit(t *testing.T) {
	m := failedOnly()
	m.Tasks[0].Delegation.Worktree = "/" + strings.Repeat("deep/", 120) + "wt"
	m.Width, m.Height = minWidth, minHeight
	press(&m, rune_('x'))
	if !strings.Contains(frameText(m), "Enlarge the terminal") {
		t.Errorf("frame does not say the list is cut:\n%s", strings.Join(plain(Frame(m)), "\n"))
	}
	if got := press(&m, rune_('y')); got.Kind != ActionNone || !strings.Contains(m.Message, "Enlarge the terminal") {
		t.Fatalf("y reset a task whose targets were not all shown: %+v %q", got, m.Message)
	}
}

func TestFooterOffersResetForAFailedTaskAtTheSmallestWidth(t *testing.T) {
	for _, logOpen := range []bool{false, true} {
		m := failedOnly()
		m.Width, m.Height, m.LogOpen = minWidth, minHeight, logOpen
		if f := footer(m); !strings.Contains(f, "x reset") || textWidth(f) > m.Width {
			t.Errorf("log %v: footer %q", logOpen, f)
		}
	}
}

func TestResetConfirmsTheRunShownEvenAfterARefresh(t *testing.T) {
	m := failedOnly()
	press(&m, rune_('x'))
	newer := record(12, task.Failed)
	newer.Delegation.Worktree, newer.Delegation.Branch = "/wt/a-b/feat/12-newer", "feat/12-newer"
	m.SetTasks([]*task.Record{newer})
	if text := frameText(m); !strings.Contains(text, "feat/12-retries") || strings.Contains(text, "feat/12-newer") {
		t.Errorf("the confirmation must keep showing the run x was pressed on:\n%s", text)
	}
	got := press(&m, rune_('y'))
	if got.Kind != ActionReset || got.Reset.Delegation.Branch != "feat/12-retries" {
		t.Fatalf("action %+v; want the confirmed run", got)
	}
}

func TestResetIsNotConfirmedWhenTheTerminalIsTooSmallToDraw(t *testing.T) {
	m := failedOnly()
	m.Width, m.Height = minWidth-1, 40
	press(&m, rune_('x'))
	if strings.Contains(frameText(m), "feat/12-retries") {
		t.Fatal("test setup: the frame draws the targets below the minimum width")
	}
	if got := press(&m, rune_('y')); got.Kind != ActionNone {
		t.Fatalf("y reset a task whose targets were never drawn: %+v", got)
	}
}
