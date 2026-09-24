package render

import (
	"bytes"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

func TestInboxSectionsAndHiddenCount(t *testing.T) {
	k := func(n int) task.Key { return task.Key{Repo: "ridezoomo/armada-backend", Number: n} }
	var buf bytes.Buffer
	Inbox(&buf, app.InboxReport{
		New:     []app.InboxEntry{{Item: app.InboxItem{Key: k(825), Title: "Provider retries"}, Changes: []string{"assigned to you"}}},
		Changed: []app.InboxEntry{{Item: app.InboxItem{Key: k(812), Title: "Pagination"}, Changes: []string{"closed"}}},
		NeedsDecision: []*task.Record{{Key: k(830), Title: "Event pagination",
			Decisions: []task.Decision{{Question: task.Question{Issue: "Should retries apply to 4xx?"}}}}},
		Ready:     []*task.Record{{Key: k(790), Title: "Done thing", Delegation: &task.Delegation{PR: 905}}},
		Unchanged: 17,
	})
	want := `NEEDS DECISION
  #830  Event pagination
        ridezoomo/armada-backend · Should retries apply to 4xx?

READY FOR YOU
  #790  Done thing
        ridezoomo/armada-backend · PR #905

CHANGED
  #812  Pagination
        ridezoomo/armada-backend · closed

NEW
  #825  Provider retries
        ridezoomo/armada-backend · assigned to you

NO CHANGE
  17 tracked items hidden
`
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestInboxEmpty(t *testing.T) {
	var buf bytes.Buffer
	Inbox(&buf, app.InboxReport{Unchanged: 1})
	if buf.String() != "Nothing new.\n\nNO CHANGE\n  1 tracked item hidden\n" {
		t.Errorf("got %q", buf.String())
	}
}

func TestStatus(t *testing.T) {
	var buf bytes.Buffer
	Status(&buf, []*task.Record{{
		Key: task.Key{Repo: "ridezoomo/armada-backend", Number: 831}, Title: "Provider retries", State: task.Reviewing,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Reviewer: task.Codex, PR: 912},
	}})
	want := "#831   Claude  REVIEWING\n       Provider retries · ridezoomo/armada-backend\n       PR #912 · reviewer: Codex\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

func TestShowUndelegated(t *testing.T) {
	var buf bytes.Buffer
	Show(&buf, &task.Record{Key: task.Key{Repo: "a/b", Number: 1}, Title: "T", URL: "u", State: task.Discovered})
	for _, s := range []string{"#1 T\na/b · u", "State  DISCOVERED", "Agent  not delegated"} {
		if !strings.Contains(buf.String(), s) {
			t.Errorf("missing %q in:\n%s", s, buf.String())
		}
	}
}
