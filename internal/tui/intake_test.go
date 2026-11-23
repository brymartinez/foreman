package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestIntakeReopensAndSavesOnlyAfterReview(t *testing.T) {
	m := Model{Width: 80, Height: 12}
	enter := Key{Kind: KeyEnter}
	tab := Key{Kind: KeyTab}
	escape := Key{Kind: KeyEscape}
	open := rune_('F')
	requireIntakeAction(t, m.Key(open), ActionLoadIntake)
	m.applyIntake(result{intake: ActionLoadIntake})
	if frame := strings.Join(Frame(m), "\n"); !strings.Contains(frame, "Paste a link to the issue") {
		t.Fatal(frame)
	}

	msg := "First line\nSecond line"
	requireIntakeAction(t, m.Key(Key{Kind: KeyPaste, Text: msg}), ActionNone)
	reply := m.Key(enter)
	requireIntakeAction(t, reply, ActionReplyIntake)
	if reply.IntakeRevision != 0 || reply.Answer != msg {
		t.Fatalf("reply = %+v", reply)
	}
	requireIntakeAction(t, m.Key(enter), ActionNone)
	requireIntakeAction(t, m.Key(escape), ActionNone)
	requireIntakeAction(t, m.Key(open), ActionNone)

	loaded := IntakeView{
		Revision: 2,
		Messages: []IntakeMessage{{User: true, Text: msg}, {Text: "Here's a draft."}},
		Draft: &IntakeDraft{
			Title: "Review before saving", Description: strings.Repeat("Part of the issue.\n", 12),
			Decisions: "Keep the decision intact.\x1b[?2004l",
		},
	}
	m.applyIntake(result{intake: ActionReplyIntake, intakeView: loaded})
	requireIntakeAction(t, m.Key(escape), ActionNone)
	requireIntakeAction(t, m.Key(open), ActionLoadIntake)
	m.applyIntake(result{intake: ActionLoadIntake, intakeView: loaded})

	requireIntakeAction(t, m.Key(tab), ActionNone)
	if frame := strings.Join(Frame(m), "\n"); !strings.Contains(frame, "Review before saving") {
		t.Fatal(frame)
	}
	requireIntakeAction(t, m.Key(enter), ActionNone)
	requireIntakeAction(t, m.Key(Key{Kind: KeyEnd}), ActionNone)
	if frame := strings.Join(Frame(m), "\n"); !strings.Contains(frame, "Keep the decision intact.") || strings.Contains(frame, "\x1b[?2004l") {
		t.Fatal(frame)
	}
	save := m.Key(rune_('y'))
	requireIntakeAction(t, save, ActionSaveIntake)
	if save.IntakeRevision != loaded.Revision {
		t.Fatalf("save = %+v", save)
	}
	m.applyIntake(result{intake: ActionSaveIntake, intakeID: "L-00000001"})
	if m.intake.panel != intakeClosed || m.Message != "Saved local issue L-00000001" {
		t.Fatalf("save left intake panel %v, message %q", m.intake.panel, m.Message)
	}
}

func TestIntakeRestoresPendingTextAndConfirmsDiscard(t *testing.T) {
	m := Model{Width: 80, Height: 12}
	requireIntakeAction(t, m.Key(rune_('F')), ActionLoadIntake)
	m.applyIntake(result{intake: ActionLoadIntake, intakeView: IntakeView{Revision: 3, Pending: "pending"}})
	m.Key(rune_('!'))
	reply := m.Key(Key{Kind: KeyEnter})
	requireIntakeAction(t, reply, ActionReplyIntake)
	if reply.Answer != "pending!" || reply.IntakeRevision != 3 {
		t.Fatalf("reply = %+v", reply)
	}
	m.applyIntake(result{intake: ActionReplyIntake, intakeView: IntakeView{Revision: 3, Pending: "pending"}, err: errors.New("service unavailable")})
	if m.intake.input != "pending!" {
		t.Fatalf("failed reply changed input to %q", m.intake.input)
	}
	m.Key(Key{Kind: KeyCtrlD})
	requireIntakeAction(t, m.Key(Key{Kind: KeyEnter}), ActionNone)
	m.Key(Key{Kind: KeyEscape})
	if m.intake.input != "pending!" {
		t.Fatalf("canceling discard changed input to %q", m.intake.input)
	}
	m.Key(Key{Kind: KeyCtrlD})
	discard := m.Key(rune_('y'))
	requireIntakeAction(t, discard, ActionDiscardIntake)
	if discard.IntakeRevision != 3 {
		t.Fatalf("discard = %+v", discard)
	}
	m.applyIntake(result{intake: ActionDiscardIntake, intakeDiscarded: true, intakeView: IntakeView{Revision: 4}})
	if m.intake.panel != intakeChat || m.intake.input != "" || m.intake.view.Revision != 4 {
		t.Fatalf("discard left %+v", m.intake)
	}
}

func TestIntakePasteKeepsNewlinesWithoutSending(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		want        Key
	}{
		{"multiline", strings.Repeat("x", 80) + "\r\nnext\tcolumn\x03",
			Key{Kind: KeyPaste, Text: strings.Repeat("x", 80) + "\nnext\tcolumn"}},
		{"too large", strings.Repeat("y\n", maxIntakeInput), Key{Kind: keyPasteTooLarge}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys := make(chan Key)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				defer close(keys)
				data := "\x1b[200~" + tc.input + "\x1b[201~\r"
				readKeys(ctx, strings.NewReader(data), keys, escapeWait)
			}()
			if got := <-keys; got != tc.want {
				t.Fatalf("paste = %+v, want %+v", got, tc.want)
			}
			if got := <-keys; got.Kind != KeyEnter {
				t.Fatalf("after paste = %+v", got)
			}
			if _, open := <-keys; open {
				t.Fatal("paste produced extra key events")
			}
		})
	}
}

func requireIntakeAction(t *testing.T, got Action, want ActionKind) {
	t.Helper()
	if got.Kind != want {
		t.Fatalf("action = %v, want %v", got.Kind, want)
	}
}
