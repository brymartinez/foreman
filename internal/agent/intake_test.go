package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func TestClaudeIntakeResumesAnExistingSession(t *testing.T) {
	session := task.Session{Agent: task.Claude, ID: "saved"}
	c := Claude{Bin: "claude", Exec: func(_ context.Context, cmd Command) error {
		resume := slices.Index(cmd.Args, "--resume")
		if resume < 0 || cmd.Args[resume+1] != session.ID || slices.Contains(cmd.Args, "--no-session-persistence") {
			t.Fatalf("resume args: %v", cmd.Args)
		}
		if data, err := io.ReadAll(cmd.Stdin); err != nil || string(data) != "Continue intake" {
			t.Fatalf("intake stdin: %q, %v", data, err)
		}
		_, err := fmt.Fprintln(cmd.Stdout, `{"type":"result","subtype":"success","structured_output":{"reply":"Paste the issue text.","draft":null}}`)
		return err
	}}
	next, reply, err := c.Intake(context.Background(), StartInput{Prompt: "Continue intake"}, session)
	if err != nil || next != session || reply.Text != "Paste the issue text." || reply.Draft != nil {
		t.Fatalf("reply: %+v, session: %+v, err: %v", reply, next, err)
	}
}

func TestCodexIntakeStartsAndResumesWithTheSameThread(t *testing.T) {
	var sent []string
	var prompt string
	c := Codex{Bin: "codex", Exec: func(_ context.Context, cmd Command) error {
		sent = cmd.Args
		data, err := io.ReadAll(cmd.Stdin)
		if err != nil {
			return err
		}
		prompt = string(data)
		output := slices.Index(cmd.Args, "-o")
		if output < 0 {
			t.Fatal("intake did not request structured output")
		}
		if err := os.WriteFile(cmd.Args[output+1], []byte(`{"reply":"A draft is ready.","draft":{"title":"Issue","description":"Body","source_url":"","repo":"","decisions":"Scope"}}`), 0o600); err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.Stdout, `{"type":"thread.started","thread_id":"saved"}`)
		return err
	}}
	in := StartInput{Prompt: "Start intake"}
	s, reply, err := c.Intake(context.Background(), in, task.Session{})
	if err != nil || s.ID != "saved" || reply.Draft == nil || reply.Draft.Description != "Body" || slices.Contains(sent, "--ephemeral") {
		t.Fatalf("first reply: %+v, session: %+v, err: %v, args: %v", reply, s, err, sent)
	}
	in.Prompt = "Continue intake"
	next, _, err := c.Intake(context.Background(), in, s)
	if err != nil || next != s || !slices.Equal(sent[:2], []string{"exec", "resume"}) || !slices.Equal(sent[len(sent)-2:], []string{s.ID, "-"}) || prompt != in.Prompt {
		t.Fatalf("resume: %+v, err: %v, args: %v", next, err, sent)
	}
}
