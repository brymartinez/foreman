package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

type fakeNotifier struct {
	sent   []string
	alerts []Alert
}

func (f *fakeNotifier) Notify(a Alert) error {
	f.sent = append(f.sent, fmt.Sprintf("Foreman %s#%d: %s", a.Key.Repo, a.Key.Number, a.Label()))
	f.alerts = append(f.alerts, a)
	return nil
}

func TestNotifiesEveryTransitionThatNeedsTheDeveloper(t *testing.T) {
	const title = "Foreman testorg/example#825"
	cases := []struct {
		name string
		run  func(t *testing.T, f fixture)
		want []string
	}{
		{"review round cap escalates", func(t *testing.T, f fixture) {
			key := started(t, f, startReq(t))
			f.claude.turn = prTurn("sid-1")
			f.codex.reviews = []agent.ReviewOutcome{findings("blocking"), findings("blocking"), findings("blocking"), findings("blocking")}
			if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
				t.Fatal(err)
			}
		}, []string{title + ": Needs your decision"}},
		{"a dead runner is reaped", func(t *testing.T, f fixture) {
			started(t, f, startReq(t))
			if _, _, err := f.app.Status(context.Background()); err != nil {
				t.Fatal(err)
			}
		}, []string{title + ": Run failed"}},
		{"the gate hands over a green PR once", func(t *testing.T, f fixture) {
			reviewed(t, f)
			setCI(f, task.CIStatus{State: task.CIPassing})
			for range 2 {
				if _, _, err := f.app.Status(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
		}, []string{title + ": Ready for human review"}},
		{"a clean review and a merge stay quiet", func(t *testing.T, f fixture) {
			reviewed(t, f)
			setPR(f, "merged")
			if _, _, err := f.app.Status(context.Background()); err != nil {
				t.Fatal(err)
			}
		}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture()
			c.run(t, f)
			if !slices.Equal(f.notes.sent, c.want) {
				t.Errorf("notifications = %q, want %q", f.notes.sent, c.want)
			}
		})
	}
}

func TestNotifyLinksThePRWhenOneExistsElseTheIssue(t *testing.T) {
	t.Run("a task without a PR links its issue", func(t *testing.T) {
		f := newFixture()
		started(t, f, startReq(t))
		if _, _, err := f.app.Status(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(f.notes.alerts) != 1 || f.notes.alerts[0].URL != "https://x/825" {
			t.Errorf("alerts = %+v, want one linking https://x/825", f.notes.alerts)
		}
	})
	t.Run("a task with a PR links the PR", func(t *testing.T) {
		f := newFixture()
		ready(t, f)
		if len(f.notes.alerts) != 1 || f.notes.alerts[0].URL != "https://github.com/testorg/example/pull/912" {
			t.Errorf("alerts = %+v, want one linking PR 912", f.notes.alerts)
		}
	})
}

func TestNotifyCarriesTheStateAndMarksCancelledRuns(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	ctx, cancel := context.WithCancelCause(context.Background())
	f.claude.err = errors.New("claude printed no result (signal: terminated)")
	f.claude.during = func() { cancel(errors.New("terminated signal received")) }
	if err := f.run(ctx, key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(f.notes.alerts) != 1 {
		t.Fatalf("alerts = %+v, want one", f.notes.alerts)
	}
	got := f.notes.alerts[0]
	if got.State != task.Failed || !got.Cancelled || got.Key != key || got.Label() != "Run cancelled" {
		t.Errorf("alert = %+v label %q, want a cancelled FAILED alert for %v", got, got.Label(), key)
	}
}
