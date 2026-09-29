package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

type stubSource struct {
	report app.InboxReport
	recs   []*task.Record
	rec    *task.Record
	err    error
}

func (s stubSource) Inbox(context.Context) (app.InboxReport, error) { return s.report, s.err }
func (s stubSource) Tasks(context.Context) ([]*task.Record, error)  { return s.recs, s.err }
func (s stubSource) Start(context.Context, app.StartRequest) (*task.Record, error) {
	return s.rec, s.err
}
func (s stubSource) Cancel(context.Context, *task.Record) (*task.Record, error) {
	return s.rec, s.err
}

func TestApplyFillsTheInboxFromARefresh(t *testing.T) {
	report := app.InboxReport{Current: []app.InboxEntry{
		{Item: app.InboxItem{Key: task.Key{Repo: "a/b", Number: 1}, Kind: task.Issue, Title: "Open"}},
		{Item: app.InboxItem{Key: task.Key{Repo: "a/b", Number: 2}, Kind: task.PullRequest, Title: "A PR"}},
	}, Warnings: []error{errors.New("refresh a/b#9 PR: gh failed")}}
	m := Model{Width: 100, Height: 30, Refreshing: true}
	apply(&m, result{report: &report})
	if len(m.Inbox) != 1 || m.Inbox[0].Item.Key.Number != 1 {
		t.Fatalf("inbox = %+v; want only the delegable issue", m.Inbox)
	}
	if m.Refreshing || m.RefreshedAt.IsZero() || len(m.Warnings) != 1 {
		t.Errorf("refresh state: refreshing=%v at=%v warnings=%d", m.Refreshing, m.RefreshedAt, len(m.Warnings))
	}
}

func TestApplyKeepsTheInboxWhenARefreshFails(t *testing.T) {
	m := Model{Width: 100, Height: 30, Refreshing: true}
	m.SetInbox([]app.InboxEntry{entry("a/b", 1, "Open")})
	apply(&m, result{report: &app.InboxReport{}, err: errors.New("gh: rate limited")})
	if len(m.Inbox) != 1 {
		t.Errorf("a failed refresh emptied the inbox: %+v", m.Inbox)
	}
	if m.Refreshing || !strings.Contains(m.Message, "rate limited") {
		t.Errorf("refreshing=%v message=%q", m.Refreshing, m.Message)
	}
}

func TestStartReportsTheBranchAndAFailure(t *testing.T) {
	started := &task.Record{Key: task.Key{Repo: "a/b", Number: 1}, State: task.Working,
		Delegation: &task.Delegation{Branch: "feat/1-add-a-dashboard"}}
	got := start(context.Background(), stubSource{rec: started}, app.StartRequest{Repo: "a/b", Issue: 1})
	if got.err != nil || !strings.Contains(got.message, "feat/1-add-a-dashboard") {
		t.Errorf("start = %+v", got)
	}

	failed := &task.Record{Key: task.Key{Repo: "a/b", Number: 1}, State: task.Failed,
		LastOutcome: &task.Outcome{Summary: "could not launch runner"}}
	got = start(context.Background(), stubSource{rec: failed}, app.StartRequest{Repo: "a/b", Issue: 1})
	if !strings.Contains(got.message, "could not launch runner") {
		t.Errorf("failed start = %+v", got)
	}

	got = start(context.Background(), stubSource{err: errors.New("issue is closed")}, app.StartRequest{Repo: "a/b", Issue: 1})
	if got.err == nil || !strings.Contains(got.err.Error(), "start a/b#1") {
		t.Errorf("start error = %+v", got)
	}
}

func TestStopReportsTheCancelledTask(t *testing.T) {
	rec := &task.Record{Key: task.Key{Repo: "a/b", Number: 1}}
	got := stop(context.Background(), stubSource{rec: rec}, rec)
	if got.err != nil || !strings.Contains(got.message, "Cancelled a/b#1") {
		t.Errorf("stop = %+v", got)
	}
	got = stop(context.Background(), stubSource{err: errors.New("nothing to cancel")}, rec)
	if got.err == nil || !strings.Contains(got.err.Error(), "cancel a/b#1") {
		t.Errorf("stop error = %+v", got)
	}
}

func TestReadLocalTailsTheSelectedTasksLog(t *testing.T) {
	path := t.TempDir() + "/runner.log"
	if err := os.WriteFile(path, []byte("2026-09-29T10:00:00Z foreman: runner 42 started\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := record(10, task.Working)
	rec.Delegation.RunnerLog = path
	m := Model{Width: 100, Height: 30}
	readLocal(context.Background(), stubSource{recs: []*task.Record{rec}}, &m)
	if len(m.Log) != 1 || !strings.Contains(m.Log[0], "runner 42 started") {
		t.Fatalf("log = %q err = %q", m.Log, m.LogErr)
	}

	rec.Delegation.RunnerLog = path + ".gone"
	readLocal(context.Background(), stubSource{recs: []*task.Record{rec}}, &m)
	if len(m.Log) != 0 || !strings.Contains(m.LogErr, "no such file") {
		t.Errorf("missing log gave %q / %q", m.Log, m.LogErr)
	}
}

func TestReadKeysDecodesAcrossReads(t *testing.T) {
	keys := make(chan Key, 8)
	// An escape sequence split across two reads must still decode as one key.
	readKeys(context.Background(), io.MultiReader(strings.NewReader("\x1b["), strings.NewReader("Bq")), keys)
	close(keys)
	var got []KeyKind
	for k := range keys {
		got = append(got, k.Kind)
	}
	if len(got) != 2 || got[0] != KeyDown || got[1] != KeyRune {
		t.Fatalf("keys = %v", got)
	}
}

func TestDrawPaintsEveryLineWithoutATrailingNewline(t *testing.T) {
	var b strings.Builder
	draw(&b, []string{"one", "two"})
	want := home + "one" + eraseLine + "\r\n" + "two" + eraseLine
	if b.String() != want {
		t.Errorf("draw = %q; want %q", b.String(), want)
	}
}
