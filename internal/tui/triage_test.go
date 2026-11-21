package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

func awaiting(repo task.Repo, n int, title string) app.InboxEntry {
	e := entry(repo, n, title)
	e.Item.State = "open"
	e.LocalTriage = true
	return e
}

func TestFTriagesTheSelectedIssueAwaitingTriage(t *testing.T) {
	m := Model{Width: 100, Height: 30}
	m.SetInbox([]app.InboxEntry{awaiting("a/b", 1, "First")})
	a := press(&m, rune_('f'))
	if a.Kind != ActionTriage || a.Triage.Key != (task.Key{Repo: "a/b", Number: 1}) || a.Triage.Title != "First" {
		t.Fatalf("f = %+v", a)
	}
}

func TestFTriagesALocalIssueByItsStableID(t *testing.T) {
	e := awaiting("", 0, "Local work")
	e.Item.Key = task.Key{Source: task.Local, ExternalID: "L-0000002A"}
	m := Model{Width: 100, Height: 30}
	m.SetInbox([]app.InboxEntry{e})
	if a := press(&m, rune_('f')); a.Kind != ActionTriage || a.Triage.Key != e.Item.Key {
		t.Fatalf("f on a local issue = %+v", a)
	}
}

func TestFOnlyActsOnOpenUndelegatedIssuesAwaitingTriage(t *testing.T) {
	triaged := awaiting("a/b", 1, "Triaged")
	triaged.Triaged = true
	delegated := awaiting("a/b", 2, "Delegated")
	delegated.Delegated = true
	closed := awaiting("a/b", 3, "Closed")
	closed.Item.State = "closed"
	pr := awaiting("a/b", 4, "A PR")
	pr.Item.Kind = task.PullRequest
	broken := awaiting("a/b", 5, "Broken")
	broken.TriageError = "invalid confirmed triage record"
	for name, e := range map[string]app.InboxEntry{"triaged": triaged, "delegated": delegated, "closed": closed, "pull request": pr, "broken record": broken} {
		m := Model{Width: 100, Height: 30}
		m.SetInbox([]app.InboxEntry{e})
		if a := press(&m, rune_('f')); a.Kind != ActionNone || m.Message == "" {
			t.Errorf("%s: f = %+v, message %q; want no session and a reason", name, a, m.Message)
		}
	}
	m := Model{Width: 100, Height: 30}
	m.SetInbox([]app.InboxEntry{awaiting("a/b", 1, "First")})
	m.SetTasks([]*task.Record{record(10, task.Implementing)})
	m.Focus = TaskPane
	if a := press(&m, rune_('f')); a.Kind != ActionNone {
		t.Errorf("f in the task pane = %+v", a)
	}
	empty := Model{Width: 100, Height: 30}
	if a := press(&empty, rune_('f')); a.Kind != ActionNone || empty.Message == "" {
		t.Errorf("f with an empty inbox = %+v, message %q", a, empty.Message)
	}
}

func TestShiftFStartsLocalIntakeFromAnywhere(t *testing.T) {
	for _, focus := range []Pane{InboxPane, TaskPane} {
		m := model()
		m.Focus = focus
		if a := press(&m, rune_('F')); a.Kind != ActionTriage || a.Triage.Key != (task.Key{}) {
			t.Errorf("F with focus %v = %+v", focus, a)
		}
	}
}

func TestCStillCancelsAndFDoesNotShadowIt(t *testing.T) {
	m := model()
	m.Focus = TaskPane
	if a := press(&m, rune_('c')); a.Kind != ActionNone || m.Mode != ConfirmingCancel {
		t.Fatalf("c = %+v, mode %v; want the cancel confirmation", a, m.Mode)
	}
}

func TestHelpAndFooterListTheTriageKeys(t *testing.T) {
	m := model()
	m.Width = 200
	if got := footer(m); !strings.Contains(got, "f triage") || !strings.Contains(got, "F new") {
		t.Errorf("footer = %q", got)
	}
	var f, shiftF bool
	for _, k := range helpKeys {
		f = f || k.keys == "f"
		shiftF = shiftF || k.keys == "F"
	}
	if !f || !shiftF {
		t.Error("the help screen does not list f and F")
	}
}

func TestTriageMessageSaysHowTheSessionEnded(t *testing.T) {
	if got := triageMessage(TriageRequest{}, nil); !strings.Contains(got, "Local intake ended") {
		t.Errorf("intake = %q", got)
	}
	if got := triageMessage(TriageRequest{Key: task.Key{Repo: "a/b", Number: 1}}, nil); !strings.Contains(got, "Triage session ended") {
		t.Errorf("triage = %q", got)
	}
	if got := triageMessage(TriageRequest{}, os.ErrInvalid); !strings.HasPrefix(got, "! triage session:") {
		t.Errorf("failure = %q", got)
	}
}

// A paused reader must leave the session's keystrokes in the terminal's buffer.
func TestPausedInputLeavesBytesUnreadUntilResumed(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	in := &pausable{f: r}
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 16)
		n, _ := in.Read(buf)
		got <- string(buf[:n])
	}()
	time.Sleep(120 * time.Millisecond) // the reader is polling an empty pipe
	start := time.Now()
	in.Pause()
	if took := time.Since(start); took > time.Second {
		t.Fatalf("Pause took %v; it must wait only for a poll", took)
	}
	w.WriteString("x")
	select {
	case s := <-got:
		t.Fatalf("a paused reader took %q from the session", s)
	case <-time.After(250 * time.Millisecond):
	}
	in.Resume()
	select {
	case s := <-got:
		if s != "x" {
			t.Fatalf("read %q, want the byte that waited", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the reader never resumed")
	}
}
