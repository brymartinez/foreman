package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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
func (s stubSource) Resume(context.Context, task.Key) (*task.Record, error) {
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
	m.Key(Key{Kind: KeyTab})
	m.Key(Key{Kind: KeyEnter})
	readLog(&m)
	if len(m.Log) != 1 || !strings.Contains(m.Log[0], "runner 42 started") {
		t.Fatalf("log = %q err = %q", m.Log, m.LogErr)
	}

	rec.Delegation.RunnerLog = path + ".gone"
	readLocal(context.Background(), stubSource{recs: []*task.Record{rec}}, &m)
	if len(m.Log) != 0 || !strings.Contains(m.LogErr, "no such file") {
		t.Errorf("missing log gave %q / %q", m.Log, m.LogErr)
	}
}

func TestReadLogOpensOnlyAfterEnter(t *testing.T) {
	dir := t.TempDir()
	var recs []*task.Record
	for i, text := range []string{"first task working", "second task working"} {
		path := filepath.Join(dir, string(rune('a'+i))+".log")
		if err := os.WriteFile(path, []byte("2026-09-29T10:00:00Z foreman: "+text+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		rec := record(10+i, task.Working)
		rec.Delegation.RunnerLog = path
		recs = append(recs, rec)
	}
	m := Model{Width: 100, Height: 30, Focus: TaskPane}
	readLocal(context.Background(), stubSource{recs: recs}, &m)
	if len(m.Log) != 0 {
		t.Fatalf("log loaded before enter: %q", m.Log)
	}
	m.Key(Key{Kind: KeyEnter})
	readLog(&m)
	if len(m.Log) != 1 || !strings.Contains(m.Log[0], "first task") {
		t.Fatalf("log = %q", m.Log)
	}
	// The same step Run takes on a key that moves the cursor.
	m.Key(Key{Kind: KeyDown})
	readLog(&m)
	if m.LogOpen || len(m.Log) != 0 {
		t.Errorf("moving to another task kept the old log open: %q", m.Log)
	}
	m.Key(Key{Kind: KeyEnter})
	readLog(&m)
	if len(m.Log) != 1 || !strings.Contains(m.Log[0], "second task") {
		t.Errorf("enter on the second task loaded %q", m.Log)
	}
}

func TestAScrolledBackLogHoldsItsLinesAsTheLogGrows(t *testing.T) {
	path := t.TempDir() + "/runner.log"
	var log strings.Builder
	for i := range 200 {
		fmt.Fprintf(&log, "line %d\n", i)
	}
	if err := os.WriteFile(path, []byte(log.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := record(10, task.Working)
	rec.Delegation.RunnerLog = path
	m := Model{Width: 100, Height: 30, Focus: TaskPane}
	s := stubSource{recs: []*task.Record{rec}}
	readLocal(context.Background(), s, &m)
	m.Key(Key{Kind: KeyEnter})
	readLog(&m)

	m.Key(Key{Kind: KeyPageUp})
	top := visibleLog(&m)
	if len(top) == 0 || top[len(top)-1] == m.Log[len(m.Log)-1] {
		t.Fatalf("page up did not scroll the log: %q", top)
	}
	// The runner writes on while the reader is scrolled back.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	for i := 200; i < 240; i++ {
		fmt.Fprintf(f, "line %d\n", i)
	}
	f.Close()
	readLocal(context.Background(), s, &m)
	if got := visibleLog(&m); !slices.Equal(got, top) {
		t.Errorf("the view moved under the reader:\ngot  %q\nwant %q", got, top)
	}

	m.Key(Key{Kind: KeyEnd})
	if got := visibleLog(&m); got[len(got)-1] != "line 239" {
		t.Errorf("end did not follow the new tail: %q", got[len(got)-1])
	}
}

// visibleLog is the log lines the pane draws, without their padding.
func visibleLog(m *Model) []string {
	lay, _ := layoutFor(m.Width, m.Height, true)
	var out []string
	for _, l := range logPane(*m, lay.logHeight)[1:] {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestStartMakesTheContextFileAbsolute(t *testing.T) {
	var got app.StartRequest
	s := recordingSource{rec: &task.Record{Key: task.Key{Repo: "a/b", Number: 1}, State: task.Working,
		Delegation: &task.Delegation{Branch: "feat/1"}}, seen: &got}
	start(context.Background(), s, app.StartRequest{Repo: "a/b", Issue: 1, ContextFile: "notes/825.md"})
	if !filepath.IsAbs(got.ContextFile) || !strings.HasSuffix(got.ContextFile, "notes/825.md") {
		t.Errorf("context file = %q; want an absolute path", got.ContextFile)
	}
	start(context.Background(), s, app.StartRequest{Repo: "a/b", Issue: 1})
	if got.ContextFile != "" {
		t.Errorf("an empty context file became %q", got.ContextFile)
	}
}

type recordingSource struct {
	stubSource
	rec  *task.Record
	seen *app.StartRequest
}

func (r recordingSource) Start(_ context.Context, req app.StartRequest) (*task.Record, error) {
	*r.seen = req
	return r.rec, nil
}

func TestFinishWaitsForAStartInFlight(t *testing.T) {
	// App.Start records WORKING before it spawns the runner, so quitting in that
	// window must not return until the call comes back.
	results := make(chan result, 1)
	m := Model{Width: 100, Height: 30}
	done := make(chan error, 1)
	go func() {
		done <- finish(context.Background(), io.Discard, &m, results, 1)
	}()
	select {
	case <-done:
		t.Fatal("finish returned while a start was still in flight")
	case <-time.After(50 * time.Millisecond):
	}
	results <- result{message: "Started a/b#1 on feat/1", finished: true}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("finish = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("finish never returned after the start came back")
	}
	if !strings.Contains(m.Message, "Started a/b#1") {
		t.Errorf("message = %q; want the start's result", m.Message)
	}
}

func TestReadKeysDecodesAcrossReads(t *testing.T) {
	// A terminal read can end anywhere in an escape sequence, including right after
	// the ESC byte, and the key must still arrive whole.
	for _, split := range [][]string{{"\x1b[", "Bq"}, {"\x1b", "[Bq"}, {"\x1b[B", "q"}} {
		keys := make(chan Key, 8)
		readKeys(context.Background(), io.MultiReader(strings.NewReader(split[0]), strings.NewReader(split[1])), keys, time.Minute)
		close(keys)
		var got []KeyKind
		for k := range keys {
			got = append(got, k.Kind)
		}
		if len(got) != 2 || got[0] != KeyDown || got[1] != KeyRune {
			t.Errorf("%q split gave keys = %v; want down then a rune", split, got)
		}
	}
}

func TestReadKeysTreatsALoneEscapeAsTheEscapeKey(t *testing.T) {
	// Nothing followed the ESC within the wait, so it was the escape key itself.
	keys := make(chan Key, 8)
	stall := make(chan struct{})
	t.Cleanup(func() { close(stall) })
	go readKeys(context.Background(), io.MultiReader(strings.NewReader("\x1b"), blockingReader{stall}), keys, time.Millisecond)
	select {
	case k := <-keys:
		if k.Kind != KeyEscape {
			t.Errorf("key = %+v; want escape", k)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a lone ESC never arrived")
	}
}

// blockingReader never returns, standing in for a terminal with nothing more to say.
type blockingReader struct{ until chan struct{} }

func (b blockingReader) Read([]byte) (int, error) { <-b.until; return 0, io.EOF }

func TestDrawPaintsEveryLineWithoutATrailingNewline(t *testing.T) {
	var b strings.Builder
	draw(&b, []string{"one", "two"})
	want := home + "one" + eraseLine + "\r\n" + "two" + eraseLine
	if b.String() != want {
		t.Errorf("draw = %q; want %q", b.String(), want)
	}
}
