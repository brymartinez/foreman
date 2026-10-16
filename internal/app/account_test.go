package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

const workEmail = "42+octo-work@users.noreply.github.com"

// failed marks a started task FAILED with its runner gone, ready for a restart.
func failed(f fixture, key task.Key) {
	rec := f.store.recs[key]
	rec.State, rec.Delegation.RunnerPID = task.Failed, 0
}

func TestStartPinsTheActiveGHAccountAndSetsTheWorktreeIdentity(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	d := f.store.recs[key].Delegation
	if d.Account != "octo-work" || d.AccountEmail != workEmail {
		t.Fatalf("pinned %q <%q>", d.Account, d.AccountEmail)
	}
	want := d.Worktree + " octo-work <" + workEmail + ">"
	if len(f.wt.identities) != 1 || f.wt.identities[0] != want {
		t.Errorf("identities = %q, want %q", f.wt.identities, want)
	}
}

func TestRestartKeepsThePinnedAccount(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	failed(f, key)
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != task.Working || rec.Delegation.Account != "octo-work" || rec.Delegation.AccountEmail != workEmail {
		t.Fatalf("state %s account %q <%q>", rec.State, rec.Delegation.Account, rec.Delegation.AccountEmail)
	}
}

func TestRestartAfterGHSwitchesAccountsFailsWithoutLaunching(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	failed(f, key)
	f.accts.active = Account{Login: "octo-home", ID: 7}
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != task.Failed || len(f.runners.spawned) != 1 {
		t.Fatalf("state %s, spawned %d runners", rec.State, len(f.runners.spawned))
	}
	msg := rec.LastOutcome.Summary
	for _, want := range []string{"octo-work", "octo-home", "gh auth switch --user octo-work"} {
		if !strings.Contains(msg, want) {
			t.Errorf("summary %q lacks %q", msg, want)
		}
	}
	if rec.Delegation.Account != "octo-work" {
		t.Errorf("the pin moved to %q", rec.Delegation.Account)
	}
}

func TestResumeAfterGHSwitchesAccountsFailsWithoutLaunching(t *testing.T) {
	f := newFixture()
	key := waiting(t, f)
	f.accts.active = Account{Login: "octo-home", ID: 7}
	rec, err := f.app.Resume(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != task.Failed || len(f.runners.spawned) != 1 || !strings.Contains(rec.LastOutcome.Summary, "octo-home") {
		t.Fatalf("state %s, spawned %d, outcome %+v", rec.State, len(f.runners.spawned), rec.LastOutcome)
	}
}

func TestRunStopsBeforeTheAgentTurnWhenGHSwitchesAccounts(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.accts.active = Account{Login: "octo-home", ID: 7}
	f.claude.turn = prTurn("sid-1")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || f.claude.starts != 0 || !strings.Contains(got.LastOutcome.Summary, "gh auth switch") {
		t.Fatalf("state %s, agent starts %d, outcome %+v", got.State, f.claude.starts, got.LastOutcome)
	}
}

func TestRunStopsBeforeTheAgentTurnWhenAnySSHPushURLPushesAsAnotherAccount(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	f.accts.ssh = []SSHAccount{{Host: "git@github-work", Login: "octo-work"}, {Host: "git@github-home", Login: "octo-home"}}
	f.claude.turn = prTurn("sid-1")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := f.store.recs[key]
	if got.State != task.Failed || f.claude.starts != 0 {
		t.Fatalf("state %s, agent starts %d", got.State, f.claude.starts)
	}
	msg := got.LastOutcome.Summary
	for _, want := range []string{"octo-work", "octo-home", "github-home", "~/.ssh/config"} {
		if !strings.Contains(msg, want) {
			t.Errorf("summary %q lacks %q", msg, want)
		}
	}
}

func TestStartWithANonSSHRemoteSkipsThePushCheck(t *testing.T) {
	f := newFixture()
	f.accts.ssh = nil
	key := started(t, f, startReq(t))
	if got := f.store.recs[key]; got.State != task.Working || got.Delegation.Account != "octo-work" {
		t.Fatalf("state %s account %q", got.State, got.Delegation.Account)
	}
}

func TestAccountLookupFailuresFailTheStartClosed(t *testing.T) {
	cases := map[string]func(a *fakeAccounts){
		"gh":  func(a *fakeAccounts) { a.err = errors.New("gh: not logged in") },
		"ssh": func(a *fakeAccounts) { a.sshErr = errors.New("Permission denied (publickey)") },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			breakIt(f.accts)
			rec, err := f.app.Start(context.Background(), startReq(t))
			if err != nil {
				t.Fatal(err)
			}
			if rec.State != task.Failed || len(f.runners.spawned) != 0 {
				t.Fatalf("state %s, spawned %d", rec.State, len(f.runners.spawned))
			}
		})
	}
}

func TestATaskStartedBeforePinningPinsTheActiveAccountAtItsNextLaunch(t *testing.T) {
	f := newFixture()
	key := started(t, f, startReq(t))
	failed(f, key)
	d := f.store.recs[key].Delegation
	d.Account, d.AccountEmail = "", ""
	f.accts.active = Account{Login: "octo-home", ID: 7}
	f.accts.ssh = []SSHAccount{{Host: "git@github-home", Login: "octo-home"}}
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != task.Working || rec.Delegation.Account != "octo-home" || rec.Delegation.AccountEmail != "7+octo-home@users.noreply.github.com" {
		t.Fatalf("state %s account %q <%q>", rec.State, rec.Delegation.Account, rec.Delegation.AccountEmail)
	}
}

func TestAccountMatchingIgnoresLoginCase(t *testing.T) {
	f := newFixture()
	f.accts.ssh = []SSHAccount{{Host: "git@github.com", Login: "Octo-Work"}}
	key := started(t, f, startReq(t))
	if got := f.store.recs[key]; got.State != task.Working {
		t.Fatalf("state %s: %+v", got.State, got.LastOutcome)
	}
}
