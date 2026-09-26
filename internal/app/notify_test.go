package app

import (
	"context"
	"io"
	"slices"
	"testing"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

type fakeNotifier struct{ sent []string }

func (f *fakeNotifier) Notify(title, body string) error {
	f.sent = append(f.sent, title+": "+body)
	return nil
}

func TestNotifiesEveryTransitionThatNeedsTheDeveloper(t *testing.T) {
	const title = "Foreman ridezoomo/armada-backend#825"
	cases := []struct {
		name string
		run  func(t *testing.T, f fixture)
		want []string
	}{
		{"review round cap escalates", func(t *testing.T, f fixture) {
			key := started(t, f, startReq(t))
			f.claude.turn = prTurn("sid-1")
			f.codex.reviews = []agent.ReviewOutcome{findings("blocking"), findings("blocking"), findings("blocking"), findings("blocking")}
			if err := f.app.RunTask(context.Background(), key, 4242, io.Discard); err != nil {
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
