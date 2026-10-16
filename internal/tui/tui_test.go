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

func (s stubSource) InboxView(context.Context, bool) (app.InboxReport, error) { return s.report, s.err }
func (s stubSource) InboxSources(context.Context, task.Source) (app.InboxReport, error) {
	return s.report, s.err
}
func (s stubSource) Tasks(context.Context) ([]*task.Record, error) { return s.recs, s.err }
func (s stubSource) Start(context.Context, app.StartRequest) (*task.Record, error) {
	return s.rec, s.err
}
func (s stubSource) ProposeBranch(_ context.Context, req app.StartRequest) (app.BranchProposal, error) {
	return app.BranchProposal{Name: fmt.Sprintf("feat/%d", req.Issue)}, nil
}
func (s stubSource) Route(context.Context, app.RouteRequest) (app.Decision, error) {
	return app.Decision{}, app.ErrRoutingOff
}
func (s stubSource) Cancel(context.Context, *task.Record) (*task.Record, error) {
	return s.rec, s.err
}
func (s stubSource) Resume(context.Context, task.Key) (*task.Record, error) {
	return s.rec, s.err
}
func (s stubSource) Respond(context.Context, task.Key, string) (*task.Record, error) {
	return s.rec, s.err
}
func (s stubSource) History(context.Context, bool) ([]app.HistoryRow, []error, error) {
	return nil, nil, s.err
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

func TestFailedRefreshKeepsTriagedIssueVisibleButStopsAutoStart(t *testing.T) {
	e := assigned("a/b", 1, 0)
	e.Item.Labels = []string{"triaged"}
	m := autoModel(1, e)
	apply(&m, result{report: &app.InboxReport{}, err: errors.New("gh down")})
	if len(m.Inbox) != 1 {
		t.Fatalf("inbox = %+v; want the issue still visible", m.Inbox)
	}
	if got := m.AutoStarts(); len(got) != 0 {
		t.Fatalf("auto started %v from labels retained after a failed refresh", startedKeys(got))
	}
}

func TestPartialSourceRefreshShowsSuccessfulIssuesAndStopsAutoStart(t *testing.T) {
	e := assigned("a/b", 1, 0)
	m := autoModel(1)
	apply(&m, result{report: &app.InboxReport{Current: []app.InboxEntry{e}, Warnings: []error{errors.New("Linear refresh: rate limited")}}})
	if len(m.Inbox) != 1 || len(m.Warnings) != 1 {
		t.Fatalf("inbox = %+v, warnings = %v", m.Inbox, m.Warnings)
	}
	if !strings.Contains(m.Message, "Linear refresh: rate limited") {
		t.Fatalf("warning message = %q", m.Message)
	}
	if got := m.AutoStarts(); len(got) != 0 {
		t.Fatalf("auto started %v after a partial refresh", got)
	}
	apply(&m, result{report: &app.InboxReport{Current: []app.InboxEntry{e}, SourcesFresh: true}})
	if got := m.AutoStarts(); len(got) != 1 {
		t.Fatalf("auto starts after recovery = %v", got)
	}
}

func TestSuccessfulRefreshUsesCurrentTriagedLabel(t *testing.T) {
	m := autoModel(1)
	e := assigned("a/b", 1, 0)
	e.Item.Labels = nil
	apply(&m, refreshed(e))
	if got := m.AutoStarts(); len(got) != 0 {
		t.Fatalf("auto started %v without the triaged label", startedKeys(got))
	}
	e.Item.Labels = []string{"triaged"}
	apply(&m, refreshed(e))
	if got := startedKeys(m.AutoStarts()); !slices.Equal(got, []task.Key{e.Item.Key}) {
		t.Fatalf("auto started %v after label was added; want the issue", got)
	}
	m = autoModel(1, e)
	e.Item.Labels = nil
	apply(&m, refreshed(e))
	if got := m.AutoStarts(); len(got) != 0 {
		t.Fatalf("auto started %v after the label was removed", startedKeys(got))
	}
}

func TestApplyDropsAReportFetchedForTheOtherView(t *testing.T) {
	m := Model{Width: 100, Height: 30, Refreshing: true, AllRepos: true}
	m.SetInbox([]app.InboxEntry{entry("a/b", 1, "Open")})
	stale := app.InboxReport{Current: []app.InboxEntry{entry("c/d", 2, "Filtered")}}
	apply(&m, result{report: &stale, allRepos: false})
	if len(m.Inbox) != 1 || m.Inbox[0].Item.Key.Repo != "a/b" {
		t.Errorf("a report from the configured view replaced the all view's inbox: %+v", m.Inbox)
	}
	if m.Refreshing || !m.RefreshedAt.IsZero() {
		t.Errorf("refreshing=%v at=%v; want the refresh ended without counting as fresh", m.Refreshing, m.RefreshedAt)
	}
}

func TestStartReportsTheBranchAndAFailure(t *testing.T) {
	started := &task.Record{Key: task.Key{Repo: "a/b", Number: 1}, State: task.Working,
		Delegation: &task.Delegation{Branch: "feat/1-add-a-dashboard"}}
	got := start(context.Background(), stubSource{rec: started}, app.StartRequest{Repo: "a/b", Issue: 1}, false)
	if got.err != nil || !strings.Contains(got.message, "feat/1-add-a-dashboard") {
		t.Errorf("start = %+v", got)
	}

	failed := &task.Record{Key: task.Key{Repo: "a/b", Number: 1}, State: task.Failed,
		LastOutcome: &task.Outcome{Summary: "could not launch runner"}}
	got = start(context.Background(), stubSource{rec: failed}, app.StartRequest{Repo: "a/b", Issue: 1}, false)
	if !strings.Contains(got.message, "could not launch runner") {
		t.Errorf("failed start = %+v", got)
	}

	got = start(context.Background(), stubSource{err: errors.New("issue is closed")}, app.StartRequest{Repo: "a/b", Issue: 1}, false)
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
	lay, _ := layoutFor(m.Width, m.Height, true, false)
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
	start(context.Background(), s, app.StartRequest{Repo: "a/b", Issue: 1, ContextFile: "notes/825.md"}, false)
	if !filepath.IsAbs(got.ContextFile) || !strings.HasSuffix(got.ContextFile, "notes/825.md") {
		t.Errorf("context file = %q; want an absolute path", got.ContextFile)
	}
	start(context.Background(), s, app.StartRequest{Repo: "a/b", Issue: 1}, false)
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

// autoSource starts every issue except the ones in fail, which return an error.
type autoSource struct {
	stubSource
	fail map[int]bool
}

func (s autoSource) Start(_ context.Context, req app.StartRequest) (*task.Record, error) {
	if s.fail[req.Issue] {
		return nil, errors.New("clone failed")
	}
	return &task.Record{Key: task.Key{Repo: req.Repo, Number: req.Issue}, State: task.Working,
		Delegation: &task.Delegation{Branch: fmt.Sprintf("feat/%d", req.Issue)}}, nil
}

func refreshed(entries ...app.InboxEntry) result {
	return result{report: &app.InboxReport{Current: entries, SourcesFresh: true}}
}

func receive(t *testing.T, results <-chan result) result {
	t.Helper()
	select {
	case r := <-results:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("no start came back")
	}
	return result{}
}

func TestAutoStartAfterARefreshCountsTowardPending(t *testing.T) {
	m := Model{Width: 100, Height: 30, Refreshing: true}
	m.SetAuto(Auto{Enabled: true, Agent: task.Claude, Max: 3})
	results := make(chan result, 8)
	src := autoSource{}
	launched := settle(context.Background(), src, &m, refreshed(assigned("a/b", 1, 0), assigned("c/d", 2, 1)), results)
	if launched != 2 {
		t.Fatalf("a refresh launched %d auto starts; want 2 counted as pending", launched)
	}
	for range 2 {
		r := receive(t, results)
		if !r.finished || !r.auto || !strings.HasPrefix(r.message, "auto: Started") {
			t.Fatalf("auto start result = %+v; want a finished auto result quitting waits for", r)
		}
		if n := settle(context.Background(), src, &m, r, results); n != 0 {
			t.Fatalf("a finished start launched %d more; each issue starts once", n)
		}
	}
	if n := settle(context.Background(), src, &m, result{report: &app.InboxReport{}, err: errors.New("gh down")}, results); n != 0 {
		t.Fatalf("a failed refresh launched %d auto starts", n)
	}
}

func TestAutoStartFailureShowsTheError(t *testing.T) {
	m := Model{Width: 100, Height: 30, Refreshing: true}
	m.SetAuto(Auto{Enabled: true, Agent: task.Claude, Max: 1})
	results := make(chan result, 8)
	src := autoSource{fail: map[int]bool{1: true}}
	refresh := refreshed(assigned("a/b", 1, 0), assigned("a/b", 2, 1))
	if n := settle(context.Background(), src, &m, refresh, results); n != 1 {
		t.Fatalf("launched %d; want the oldest issue only", n)
	}
	failed := receive(t, results)
	if n := settle(context.Background(), src, &m, failed, results); n != 1 {
		t.Fatalf("after a failed start launched %d; want the next issue", n)
	}
	if !strings.Contains(m.Message, "! auto: start a/b#1: clone failed") {
		t.Fatalf("message = %q; want the auto start error", m.Message)
	}
	settle(context.Background(), src, &m, receive(t, results), results)
	if !strings.Contains(m.Message, "clone failed") {
		t.Errorf("the next auto start's success replaced the error: %q", m.Message)
	}
	if n := settle(context.Background(), src, &m, refresh, results); n != 0 {
		t.Fatalf("the next refresh retried the failed start: launched %d", n)
	}
}

func TestAutoStartWaitsWhenTheTaskReadFails(t *testing.T) {
	m := Model{Width: 100, Height: 30, Refreshing: true}
	m.SetAuto(Auto{Enabled: true, Agent: task.Claude, Max: 1})
	results := make(chan result, 8)
	src := autoSource{}
	if n := settle(context.Background(), src, &m, refreshed(assigned("a/b", 1, 0), assigned("c/d", 2, 1)), results); n != 1 {
		t.Fatalf("launched %d; want one start for a limit of 1", n)
	}
	broken := autoSource{stubSource: stubSource{err: errors.New("tasks dir unreadable")}}
	if n := settle(context.Background(), broken, &m, receive(t, results), results); n != 0 {
		t.Fatalf("launched %d after the task read failed; the first run may still be WORKING", n)
	}
	if n := launchAuto(context.Background(), broken, &m, results); n != 0 {
		t.Fatalf("a local tick launched %d with the task read still failing", n)
	}
}

func TestAutoStartAfterARefreshCountsARunStartedElsewhere(t *testing.T) {
	m := Model{Width: 100, Height: 30, Refreshing: true}
	m.SetAuto(Auto{Enabled: true, Agent: task.Claude, Max: 1})
	// Another foreman command started a/b#10 while the refresh ran.
	src := autoSource{stubSource: stubSource{recs: []*task.Record{record(10, task.Working)}}}
	results := make(chan result, 8)
	if n := settle(context.Background(), src, &m, refreshed(assigned("c/d", 1, 0)), results); n != 0 {
		t.Fatalf("a refresh launched %d with the only slot taken by a run the dashboard had not read", n)
	}
}

func TestTurningAutoOnCountsARunStartedElsewhere(t *testing.T) {
	m := Model{Width: 100, Height: 30}
	m.SetAuto(Auto{Max: 1})
	m.SetInbox([]app.InboxEntry{assigned("c/d", 1, 0)})
	src := autoSource{stubSource: stubSource{recs: []*task.Record{record(10, task.Working)}}}
	m.Key(rune_('a'))
	if n := launchAutoFresh(context.Background(), src, &m, make(chan result, 8)); n != 0 {
		t.Fatalf("turning auto on launched %d with the only slot taken by a run the dashboard had not read", n)
	}
}

// respondingSource records the answer it was given and returns rec.
type respondingSource struct {
	stubSource
	rec  *task.Record
	err  error
	seen *[]string
}

func (r respondingSource) Respond(_ context.Context, key task.Key, raw string) (*task.Record, error) {
	*r.seen = append(*r.seen, fmt.Sprintf("%s#%d %q", key.Repo, key.Number, raw))
	return r.rec, r.err
}

func TestRespondPassesTheTypedAnswerAndReportsTheResult(t *testing.T) {
	key := task.Key{Repo: "a/b", Number: 10}
	failed := record(10, task.Failed)
	failed.LastOutcome = &task.Outcome{Summary: "runner did not start"}
	for _, tc := range []struct {
		name string
		rec  *task.Record
		err  error
		want string
	}{
		{"more questions waiting", answered(asking(10), "A. Retry"), nil, "Recorded the answer for a/b#10; 1 question still waiting"},
		{"last answer", answered(answered(asking(10), "A. Retry"), "B. No"), nil, "Resumed a/b#10"},
		{"runner failed to launch", failed, nil, "! a/b#10 could not resume: runner did not start"},
		{"respond error", nil, errors.New("no recommended option"), "! answer a/b#10: no recommended option"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen []string
			r := respond(context.Background(), respondingSource{rec: tc.rec, err: tc.err, seen: &seen}, key, "B")
			if !slices.Equal(seen, []string{`a/b#10 "B"`}) {
				t.Errorf("Respond calls = %v; want the typed answer for a/b#10", seen)
			}
			if !r.finished || r.key != key || !r.respond {
				t.Errorf("result = %+v; want a finished respond for a/b#10", r)
			}
			var m Model
			apply(&m, r)
			if !strings.HasPrefix(m.Message, tc.want) {
				t.Errorf("message = %q; want %q", m.Message, tc.want)
			}
		})
	}
}

func TestSettleAfterTheLastAnswerOpensTheLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	if err := os.WriteFile(path, []byte("agent resumed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := prompting()
	m.Selected().Delegation.RunnerLog = path
	press(&m, Key{Kind: KeyEnter})
	done := answered(answered(m.Selected(), "A. Retry"), "B. No")
	settle(context.Background(), stubSource{}, &m, result{answered: done, respond: true, finished: true, key: done.Key}, nil)
	if m.Mode != Browsing || !m.LogOpen {
		t.Fatalf("mode %v log %t; want the task's log", m.Mode, m.LogOpen)
	}
	if !slices.Equal(m.Log, []string{"agent resumed"}) {
		t.Errorf("log = %q; want it read at once", m.Log)
	}
	if m.starting[done.Key] {
		t.Error("the answer's slot was not freed")
	}
}

func TestSettleAfterAFailedAnswerKeepsThePrompt(t *testing.T) {
	m := prompting()
	press(&m, rune_('Z'), Key{Kind: KeyEnter})
	key := m.Selected().Key
	settle(context.Background(), stubSource{}, &m, result{err: errors.New("answer a/b#10: boom"), respond: true, finished: true, key: key}, nil)
	if m.Mode != AnsweringDecision || m.Input != "Z" || !strings.HasPrefix(m.Message, "!") {
		t.Fatalf("mode %v input %q message %q; want the prompt, the text, and the error", m.Mode, m.Input, m.Message)
	}
	if got := press(&m, Key{Kind: KeyEnter}); got.Kind != ActionRespond {
		t.Errorf("enter after the failure returned %+v", got)
	}
}

func TestFinishWaitsForAnAnswerInFlight(t *testing.T) {
	// The last answer records WORKING before App.Respond spawns the runner.
	results := make(chan result, 1)
	m := prompting()
	press(&m, Key{Kind: KeyEnter})
	done := make(chan error, 1)
	go func() { done <- finish(context.Background(), io.Discard, &m, results, 1) }()
	select {
	case <-done:
		t.Fatal("finish returned while an answer was still in flight")
	case <-time.After(50 * time.Millisecond):
	}
	results <- respond(context.Background(), respondingSource{rec: answered(asking(10), "A. Retry"), seen: new([]string)}, task.Key{Repo: "a/b", Number: 10}, "")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("finish never returned after the answer came back")
	}
}

func TestReadLocalShowsOnlyTheSelectedSourcesDelegatedWork(t *testing.T) {
	gh := record(10, task.Working)
	gh.Source = task.GitHub
	legacy := record(11, task.Working) // no source: a GitHub issue
	lin := record(12, task.Working)
	lin.Source = task.Linear
	s := stubSource{recs: []*task.Record{gh, legacy, lin}}
	for _, tc := range []struct {
		name   string
		source task.Source
		want   []int
	}{
		{"linear shows only Linear tasks", task.Linear, []int{12}},
		{"github shows GitHub and source-less tasks", task.GitHub, []int{10, 11}},
		{"no source shows every task", "", []int{10, 11, 12}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{Width: 100, Height: 30, IssueSource: tc.source}
			readLocal(context.Background(), s, &m)
			var got []int
			for _, rec := range m.Tasks {
				got = append(got, rec.Number)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("tasks = %v, want %v", got, tc.want)
			}
			if sel := m.Selected(); sel == nil || !slices.Contains(tc.want, sel.Number) {
				t.Errorf("selected = %v, want one of %v", sel, tc.want)
			}
		})
	}
}

func TestAHiddenTaskReturnsWhenTheSourceIncludesIt(t *testing.T) {
	lin := record(12, task.Working)
	lin.Source = task.Linear
	s := stubSource{recs: []*task.Record{record(10, task.Working), lin}}
	m := Model{Width: 100, Height: 30, IssueSource: task.GitHub}
	readLocal(context.Background(), s, &m)
	if len(m.Tasks) != 1 || len(s.recs) != 2 {
		t.Fatalf("github view tasks = %d, stored = %d", len(m.Tasks), len(s.recs))
	}
	m.IssueSource = ""
	readLocal(context.Background(), s, &m)
	if len(m.Tasks) != 2 {
		t.Errorf("both-source view tasks = %d, want 2", len(m.Tasks))
	}
}
