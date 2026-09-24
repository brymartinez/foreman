package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

const prOpened = `{"status":"pr_opened","summary":"Added 4xx guard","prNumber":912,"prUrl":"https://github.com/a/b/pull/912","validation":[{"command":"go test ./...","passed":true}],"question":null}`

func argValue(args []string, flag string) string {
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		return ""
	}
	return args[i+1]
}

func TestClaudeStart(t *testing.T) {
	var got Command
	exec := func(_ context.Context, c Command) error {
		got = c
		fmt.Fprintf(c.Stdout, `{"type":"result","subtype":"success","is_error":false,"session_id":"sid-1","result":"...","structured_output":%s,"permission_denials":[],"total_cost_usd":0.12}`, prOpened)
		return nil
	}
	var sessions []task.Session
	var log bytes.Buffer
	c := Claude{Bin: "claude", Model: "haiku", AllowedTools: []string{"Bash", "WebFetch"}, Exec: exec, NewSessionID: func() string { return "sid-1" }}
	turn, err := c.Start(context.Background(), StartInput{Worktree: "/wt", Prompt: "do it", Log: &log}, func(s task.Session) { sessions = append(sessions, s) })
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0] != (task.Session{Agent: task.Claude, ID: "sid-1"}) {
		t.Errorf("onSession = %v", sessions)
	}
	if turn.Outcome.Status != PROpened || turn.Outcome.PRNumber != 912 {
		t.Errorf("outcome = %+v", turn.Outcome)
	}
	if got.Dir != "/wt" || argValue(got.Args, "-p") != "do it" || argValue(got.Args, "--session-id") != "sid-1" ||
		argValue(got.Args, "--permission-mode") != "acceptEdits" || argValue(got.Args, "--model") != "haiku" ||
		!slices.Contains(got.Args, "--allowedTools=Bash,WebFetch") || argValue(got.Args, "--json-schema") != string(OutcomeSchema) {
		t.Errorf("argv = %q dir %q", got.Args, got.Dir)
	}
	if !strings.Contains(log.String(), "cost=$0.12") {
		t.Errorf("log = %q", log.String())
	}
}

func TestClaudeStartWithoutStructuredOutputFails(t *testing.T) {
	exec := func(_ context.Context, c Command) error {
		io.WriteString(c.Stdout, `{"subtype":"error_max_turns","is_error":true,"session_id":"sid","result":"ran out of turns"}`)
		return errors.New("exit status 1")
	}
	var recorded task.Session
	turn, err := Claude{Bin: "claude", Exec: exec}.Start(context.Background(), StartInput{Worktree: "/wt"}, func(s task.Session) { recorded = s })
	if err == nil || !strings.Contains(err.Error(), "ran out of turns") {
		t.Fatalf("err = %v", err)
	}
	if recorded.ID == "" || turn.Session.ID != recorded.ID {
		t.Errorf("session must be known even on failure: recorded %v turn %v", recorded, turn.Session)
	}
}

func TestCodexStart(t *testing.T) {
	var got Command
	exec := func(_ context.Context, c Command) error {
		got = c
		schema, err := os.ReadFile(argValue(c.Args, "--output-schema"))
		if err != nil || !bytes.Equal(schema, OutcomeSchema) {
			t.Errorf("schema file: %v", err)
		}
		io.WriteString(c.Stdout, "{\"type\":\"thread.started\",\"thread_id\":\"th-1\"}\n{\"type\":\"turn.started\"}\n")
		return os.WriteFile(argValue(c.Args, "-o"), []byte(prOpened), 0o600)
	}
	var sessions []task.Session
	var log bytes.Buffer
	turn, err := Codex{Bin: "codex", Exec: exec}.Start(context.Background(), StartInput{Worktree: "/wt", Prompt: "do it", Log: &log}, func(s task.Session) { sessions = append(sessions, s) })
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0] != (task.Session{Agent: task.Codex, ID: "th-1"}) || turn.Session.ID != "th-1" {
		t.Errorf("sessions = %v turn = %v", sessions, turn.Session)
	}
	if turn.Outcome.PRNumber != 912 {
		t.Errorf("outcome = %+v", turn.Outcome)
	}
	if got.Args[0] != "exec" || argValue(got.Args, "-C") != "/wt" || argValue(got.Args, "-s") != "workspace-write" ||
		!slices.Contains(got.Args, "sandbox_workspace_write.network_access=true") || got.Args[len(got.Args)-1] != "do it" {
		t.Errorf("argv = %q", got.Args)
	}
	if !strings.Contains(log.String(), "thread.started") {
		t.Errorf("events not logged: %q", log.String())
	}
}

func TestCodexStartReportsTurnFailure(t *testing.T) {
	exec := func(_ context.Context, c Command) error {
		io.WriteString(c.Stdout, "{\"type\":\"thread.started\",\"thread_id\":\"th-2\"}\n{\"type\":\"turn.failed\",\"error\":{\"message\":\"usage limit reached\"}}\n")
		return errors.New("exit status 1")
	}
	turn, err := Codex{Bin: "codex", Exec: exec}.Start(context.Background(), StartInput{Worktree: "/wt"}, func(task.Session) {})
	if err == nil || !strings.Contains(err.Error(), "usage limit reached") || turn.Session.ID != "th-2" {
		t.Fatalf("turn %v err %v", turn, err)
	}
}

func TestImplementationPromptCarriesDurableContext(t *testing.T) {
	p := ImplementationPrompt(TaskBrief{
		Key: task.Key{Repo: "ridezoomo/armada-backend", Number: 825}, Title: "Provider retries", URL: "https://x/825",
		IssueBody: "Retry failed requests.", Context: "Preserve the existing 5xx-only retry semantics.",
		Branch: "agent/825-provider-retries", BaseBranch: "main", Worktree: "/wt/825-provider-retries",
	})
	for _, want := range []string{
		"ridezoomo/armada-backend#825",
		"Retry failed requests.",
		"gh issue view 825 --repo ridezoomo/armada-backend --comments",
		"authoritative",
		"Preserve the existing 5xx-only retry semantics.",
		"/wt/825-provider-retries on branch agent/825-provider-retries, created from origin/main",
		"git push -u origin agent/825-provider-retries",
		"gh pr create --base main --head agent/825-provider-retries",
		"Closes #825",
		`status "blocked"`,
		"recommendation: the label of the option you recommend, and nothing else",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Contains(ImplementationPrompt(TaskBrief{Key: task.Key{Repo: "a/b", Number: 1}}), "## Implementation context") {
		t.Error("context section rendered without context")
	}
}

func TestResumeArgvAndMissingSession(t *testing.T) {
	var got Command
	claudeExec := func(_ context.Context, c Command) error {
		got = c
		return errors.New("exit status 1")
	}
	_, err := Claude{Bin: "claude", Exec: claudeExec}.Resume(context.Background(), task.Session{Agent: task.Claude, ID: "sid-1"}, StartInput{Worktree: "/wt", Prompt: "answer"})
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("claude missing session err = %v", err)
	}
	if argValue(got.Args, "--resume") != "sid-1" || argValue(got.Args, "-p") != "answer" || slices.Contains(got.Args, "--session-id") {
		t.Errorf("claude resume argv = %q", got.Args)
	}

	codexExec := func(_ context.Context, c Command) error {
		got = c
		if c.Dir != "/wt" {
			t.Errorf("codex resume dir = %q", c.Dir)
		}
		io.WriteString(c.Stdout, "{\"type\":\"thread.started\",\"thread_id\":\"th-1\"}\n")
		return os.WriteFile(argValue(c.Args, "-o"), []byte(prOpened), 0o600)
	}
	turn, err := Codex{Bin: "codex", Exec: codexExec}.Resume(context.Background(), task.Session{Agent: task.Codex, ID: "th-1"}, StartInput{Worktree: "/wt", Prompt: "answer"})
	if err != nil || turn.Session.ID != "th-1" {
		t.Fatalf("codex resume: %v %v", turn, err)
	}
	n := len(got.Args)
	if got.Args[0] != "exec" || got.Args[1] != "resume" || got.Args[n-2] != "th-1" || got.Args[n-1] != "answer" ||
		!slices.Contains(got.Args, `sandbox_mode="workspace-write"`) || slices.Contains(got.Args, "-C") {
		t.Errorf("codex resume argv = %q", got.Args)
	}

	noThread := func(_ context.Context, c Command) error { return errors.New("exit status 1") }
	if _, err := (Codex{Bin: "codex", Exec: noThread}).Resume(context.Background(), task.Session{ID: "gone"}, StartInput{}); !errors.Is(err, ErrNoSession) {
		t.Errorf("codex missing session err = %v", err)
	}
}

func TestPromptIncludesAnsweredDecisions(t *testing.T) {
	p := ImplementationPrompt(TaskBrief{Key: task.Key{Repo: "a/b", Number: 1}, Decisions: []task.Decision{
		{Question: task.Question{Issue: "Retry 4xx?"}, Answer: "A. 5xx only"},
		{Question: task.Question{Issue: "Unanswered?"}},
	}})
	if !strings.Contains(p, "- Q: Retry 4xx?\n  A: A. 5xx only") || strings.Contains(p, "Unanswered?") {
		t.Errorf("decisions section wrong:\n%s", p)
	}
	if !strings.Contains(ResumeMessage(task.Decision{Question: task.Question{Issue: "Retry 4xx?"}, Answer: "A"}), "Decision: A") {
		t.Error("resume message lacks the answer")
	}
}
