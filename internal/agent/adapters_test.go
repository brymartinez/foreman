package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

const prOpened = `{"status":"pr_opened","summary":"Added 4xx guard","prNumber":912,"prUrl":"https://github.com/a/b/pull/912","validation":[{"command":"go test ./...","passed":true}],"questions":[]}`

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
	c := Claude{Bin: "claude", Model: "haiku", Exec: exec, NewSessionID: func() string { return "sid-1" }}
	turn, err := c.Start(context.Background(), StartInput{Worktree: "/wt", Prompt: "do it", Log: &log, MaxBudgetUSD: 3}, func(s task.Session) { sessions = append(sessions, s) })
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
		!slices.Contains(got.Args, "--allowedTools=Bash,WebFetch,WebSearch") || argValue(got.Args, "--json-schema") != string(OutcomeSchema) || argValue(got.Args, "--max-budget-usd") != "3.00" {
		t.Errorf("argv = %q dir %q", got.Args, got.Dir)
	}
	if !strings.Contains(log.String(), "cost=$0.12") {
		t.Errorf("log = %q", log.String())
	}
	if turn.Usage.ClaudeCostUSD != 0.12 || turn.Usage.ClaudeTurns != 1 {
		t.Errorf("usage = %+v", turn.Usage)
	}
}

func TestClaudeStageModelsOnStartResumeAndReview(t *testing.T) {
	var commands []Command
	exec := func(_ context.Context, cmd Command) error {
		commands = append(commands, cmd)
		result := prOpened
		if slices.Contains(cmd.Args, "--no-session-persistence") {
			result = reviewJSON
		}
		fmt.Fprintf(cmd.Stdout, `{"type":"result","subtype":"success","structured_output":%s}`, result)
		return nil
	}
	c := Claude{Bin: "claude", Model: "opus", PlanModel: "sonnet", ReviewModel: "opus", Exec: exec, NewSessionID: func() string { return "sid-1" }}
	plan := StartInput{Worktree: "/wt", Stage: task.StagePlan}
	implement := StartInput{Worktree: "/wt", Stage: task.StageImplement}
	if _, err := c.Start(context.Background(), plan, func(task.Session) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resume(context.Background(), task.Session{Agent: task.Claude, ID: "sid-1"}, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(context.Background(), implement, func(task.Session) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resume(context.Background(), task.Session{Agent: task.Claude, ID: "sid-2"}, implement); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Review(context.Background(), StartInput{Worktree: "/wt"}); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"sonnet", "sonnet", "opus", "opus", "opus"} {
		if got := argValue(commands[i].Args, "--model"); got != want {
			t.Errorf("command %d model = %q, want %q", i, got, want)
		}
	}
}

func TestClaudeStreamReachesTheLog(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/cfg")
	exec := func(_ context.Context, c Command) error {
		if !slices.Contains(c.Args, "stream-json") || !slices.Contains(c.Args, "--verbose") {
			t.Errorf("argv = %q", c.Args)
		}
		io.WriteString(c.Stdout, "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"sid-1\"}\n")
		io.WriteString(c.Stdout, "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"Bash\"}]}}\n")
		fmt.Fprintf(c.Stdout, "{\"type\":\"result\",\"subtype\":\"success\",\"structured_output\":%s,\"total_cost_usd\":1.5,\"num_turns\":7}\n", prOpened)
		io.WriteString(c.Stdout, "{\"type\":\"system\",\"subtype\":\"task_summary\"}\n")
		return nil
	}
	var log bytes.Buffer
	c := Claude{Bin: "claude", Exec: exec, NewSessionID: func() string { return "sid-1" }}
	turn, err := c.Start(context.Background(), StartInput{Worktree: "/home/u/.local/wt/1-fix", Log: &log}, func(task.Session) {})
	if err != nil || turn.Outcome.Status != PROpened || turn.Usage.ClaudeCostUSD != 1.5 {
		t.Fatalf("turn = %+v, %v", turn, err)
	}
	for _, want := range []string{
		"claude: transcript /cfg/projects/-home-u--local-wt-1-fix/sid-1.jsonl\n",
		`"name":"Bash"`,
		`"subtype":"task_summary"`,
		"cost=$1.50 permission_denials=0 turns=7",
	} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log missing %q:\n%s", want, log.String())
		}
	}
}

func TestClaudeStreamWithoutResultHasNoSession(t *testing.T) {
	exec := func(_ context.Context, c Command) error {
		io.WriteString(c.Stdout, "{\"type\":\"system\",\"subtype\":\"init\"}\n")
		return errors.New("signal: terminated")
	}
	_, err := Claude{Bin: "claude", Exec: exec}.Resume(context.Background(), task.Session{Agent: task.Claude, ID: "sid-1"}, StartInput{Worktree: "/wt"})
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("err = %v", err)
	}
	gone := func(_ context.Context, c Command) error {
		io.WriteString(c.Stdout, `{"type":"result","subtype":"error_during_execution","is_error":true,"num_turns":0,"total_cost_usd":0}`)
		return errors.New("exit status 1")
	}
	_, err = Claude{Bin: "claude", Exec: gone}.Resume(context.Background(), task.Session{Agent: task.Claude, ID: "gone"}, StartInput{Worktree: "/wt"})
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("missing session err = %v", err)
	}
}

func TestClaudeStartWithoutStructuredOutputFails(t *testing.T) {
	exec := func(_ context.Context, c Command) error {
		io.WriteString(c.Stdout, `{"type":"result","subtype":"error_max_turns","is_error":true,"session_id":"sid","result":"ran out of turns"}`)
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

func TestCodexStageModelsAndEffortOnStartResumeAndReview(t *testing.T) {
	var commands []Command
	exec := func(_ context.Context, cmd Command) error {
		commands = append(commands, cmd)
		fmt.Fprintln(cmd.Stdout, `{"type":"thread.started","thread_id":"th-1"}`)
		result := prOpened
		if slices.Contains(cmd.Args, "--ephemeral") {
			result = reviewJSON
		}
		return os.WriteFile(argValue(cmd.Args, "-o"), []byte(result), 0o600)
	}
	c := Codex{Bin: "codex", Model: "gpt-6-sol", PlanModel: "gpt-6-luna", ReviewModel: "gpt-6-sol",
		ReasoningEffort: "medium", PlanReasoningEffort: "xhigh", ReviewReasoningEffort: "medium", Exec: exec}
	plan := StartInput{Worktree: "/wt", Stage: task.StagePlan}
	implement := StartInput{Worktree: "/wt", Stage: task.StageImplement}
	if _, err := c.Start(context.Background(), plan, func(task.Session) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resume(context.Background(), task.Session{Agent: task.Codex, ID: "th-1"}, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(context.Background(), implement, func(task.Session) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resume(context.Background(), task.Session{Agent: task.Codex, ID: "th-2"}, implement); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Review(context.Background(), StartInput{Worktree: "/wt"}); err != nil {
		t.Fatal(err)
	}
	for i, want := range []struct{ model, effort string }{
		{"gpt-6-luna", "xhigh"}, {"gpt-6-luna", "xhigh"}, {"gpt-6-sol", "medium"}, {"gpt-6-sol", "medium"}, {"gpt-6-sol", "medium"},
	} {
		if got := argValue(commands[i].Args, "-m"); got != want.model {
			t.Errorf("command %d model = %q, want %q", i, got, want.model)
		}
		if !slices.Contains(commands[i].Args, fmt.Sprintf("model_reasoning_effort=%q", want.effort)) {
			t.Errorf("command %d missing %s effort: %q", i, want.effort, commands[i].Args)
		}
	}
}

// A linked worktree's git data lives in the main clone, outside the sandbox's
// workspace, so both Start and Resume must make it writable or commits fail (D112).
func TestCodexCanWriteTheWorktreesGitDir(t *testing.T) {
	dir := t.TempDir()
	clone, wt := filepath.Join(dir, "clone"), filepath.Join(dir, "wt")
	for _, args := range [][]string{
		{"init", "-q", clone},
		{"-C", clone, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
		{"-C", clone, "worktree", "add", "-q", "-b", "feat/1-x", wt},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	out, err := exec.Command("git", "-C", wt, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		t.Fatal(err)
	}
	want := "sandbox_workspace_write.writable_roots=[" + fmt.Sprintf("%q", strings.TrimSpace(string(out))) + "]"

	var got Command
	run := func(_ context.Context, c Command) error {
		got = c
		io.WriteString(c.Stdout, "{\"type\":\"thread.started\",\"thread_id\":\"th-1\"}\n")
		return os.WriteFile(argValue(c.Args, "-o"), []byte(prOpened), 0o600)
	}
	codex := Codex{Bin: "codex", Exec: run}
	if _, err := codex.Start(context.Background(), StartInput{Worktree: wt, Prompt: "p"}, func(task.Session) {}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got.Args, want) {
		t.Errorf("start argv = %q, want %q", got.Args, want)
	}
	if _, err := codex.Resume(context.Background(), task.Session{Agent: task.Codex, ID: "th-1"}, StartInput{Worktree: wt, Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got.Args, want) {
		t.Errorf("resume argv = %q, want %q", got.Args, want)
	}
	if args := gitWriteArgs(clone); args != nil {
		t.Errorf("a plain checkout's .git is inside the workspace; got %q", args)
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
		"authoritative",
		"Preserve the existing 5xx-only retry semantics.",
		"/wt/825-provider-retries on branch agent/825-provider-retries, created from origin/main",
		"git push -u origin agent/825-provider-retries",
		"gh pr create --base main --head agent/825-provider-retries",
		"Closes #825",
		"the worktree must be clean",
		"gh pr view --json number,url",
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
	// Workflow rules live in the stage files, which the developer can edit (D92).
	for _, rule := range []string{"gh issue view", "Commit work in progress", "earlier turn"} {
		if strings.Contains(p, rule) {
			t.Errorf("prompt hard-codes the stage-file rule %q", rule)
		}
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

func TestPlanningPromptWritesDocumentsOnly(t *testing.T) {
	p := ImplementationPrompt(TaskBrief{Key: task.Key{Repo: "a/b", Number: 7}, Planning: true, PlanInstructions: "Write a PRD with goals.", Branch: "feat/7-x", BaseBranch: "main"})
	for _, want := range []string{"## Plan first", "Do not change, commit, or push anything in this turn", "Write a PRD with goals.", `"planned" once the four documents are written`, "## Opening the pull request"} {
		if !strings.Contains(p, want) {
			t.Errorf("planning prompt missing %q", want)
		}
	}
	plan := []task.PlanComment{{Doc: task.Analysis, URL: "https://x/1"}, {Doc: task.ImplementationPlan, URL: "https://x/4"}}
	p = ImplementationPrompt(TaskBrief{Key: task.Key{Repo: "a/b", Number: 7}, Plan: plan})
	if strings.Contains(p, "## Plan first") || strings.Contains(p, `"planned"`) || !strings.Contains(p, "## The plan") || !strings.Contains(p, "- Implementation plan: https://x/4") {
		t.Errorf("a turn after the plan must follow it, not write it:\n%s", p)
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
	decs := []task.Decision{{Question: task.Question{Issue: "Retry 4xx?"}, Answer: "A"}}
	if msg := ResumeMessage(decs, nil, false); !strings.Contains(msg, "Decision: A") || strings.Contains(msg, "requested these changes") || !strings.Contains(msg, "pr_opened, blocked, or failed") {
		t.Errorf("resume message = %q", msg)
	}
	open := &task.Review{Round: 4, PR: 9, Findings: []task.Finding{{Severity: "blocking", Summary: "4xx retried", File: "retry.go", Line: 40}}}
	if msg := ResumeMessage(decs, nil, true); !strings.Contains(msg, "planned, blocked, or failed") {
		t.Errorf("a planning resume must end with the planned outcome: %q", msg)
	}
	if msg := ResumeMessage(decs, open, false); !strings.Contains(msg, "Review round 4 of PR #9") || !strings.Contains(msg, "1. [blocking] 4xx retried\n   retry.go:40") {
		t.Errorf("resume message must carry the unaddressed findings: %q", msg)
	}
}

const reviewJSON = `{"verdict":"changes_requested","summary":"one bug","findings":[{"severity":"blocking","file":"retry.go","line":40,"summary":"4xx retried","detail":"guard missing"}]}`

func TestReviewAdaptersAreReadOnlyAndDisposable(t *testing.T) {
	var got Command
	claudeExec := func(_ context.Context, c Command) error {
		got = c
		fmt.Fprintf(c.Stdout, `{"type":"result","subtype":"success","is_error":false,"session_id":"r","structured_output":%s}`, reviewJSON)
		return nil
	}
	r, err := Claude{Bin: "claude", Model: "opus", ReviewModel: "sonnet", Exec: claudeExec}.Review(context.Background(), StartInput{Worktree: "/wt", Prompt: "review"})
	if err != nil || r.Clean || len(r.Findings) != 1 || r.Findings[0].Line != 40 {
		t.Fatalf("claude review: %+v %v", r, err)
	}
	if argValue(got.Args, "--model") != "sonnet" {
		t.Errorf("claude review model = %q, want the review model", argValue(got.Args, "--model"))
	}
	if argValue(got.Args, "--permission-mode") != "dontAsk" || !slices.Contains(got.Args, "--no-session-persistence") ||
		argValue(got.Args, "--json-schema") != string(ReviewSchema) || slices.Contains(got.Args, "--allowedTools=Bash") ||
		!slices.Contains(got.Args, "--allowedTools="+strings.Join(ReviewTools, ",")) {
		t.Errorf("claude review argv = %q", got.Args)
	}

	codexExec := func(_ context.Context, c Command) error {
		got = c
		schema, _ := os.ReadFile(argValue(c.Args, "--output-schema"))
		if !bytes.Equal(schema, ReviewSchema) {
			t.Error("codex review not given the review schema")
		}
		io.WriteString(c.Stdout, "{\"type\":\"thread.started\",\"thread_id\":\"r1\"}\n")
		return os.WriteFile(argValue(c.Args, "-o"), []byte(reviewJSON), 0o600)
	}
	r, err = Codex{Bin: "codex", Model: "gpt-6-sol", Exec: codexExec}.Review(context.Background(), StartInput{Worktree: "/wt", Prompt: "review"})
	if err != nil || len(r.Findings) != 1 {
		t.Fatalf("codex review: %+v %v", r, err)
	}
	if argValue(got.Args, "-m") != "gpt-6-sol" {
		t.Errorf("codex review without review_model must use model: %q", argValue(got.Args, "-m"))
	}
	if argValue(got.Args, "-s") != "read-only" || !slices.Contains(got.Args, "--ephemeral") || slices.Contains(got.Args, "sandbox_workspace_write.network_access=true") {
		t.Errorf("codex review argv = %q", got.Args)
	}
}

func TestFindingsMessageCarriesEachFinding(t *testing.T) {
	msg := FindingsMessage(task.Review{Round: 2, PR: 912, Reviewer: task.Codex, Findings: []task.Finding{{Severity: "blocking", File: "retry.go", Line: 40, Summary: "4xx retried", Detail: "guard missing"}}})
	for _, want := range []string{"Review round 2 of PR #912 by codex", "1. [blocking] 4xx retried", "retry.go:40", "guard missing", "under your implementation instructions"} {
		if !strings.Contains(msg, want) {
			t.Errorf("findings message missing %q", want)
		}
	}
}

func TestImplementationPromptStageInstructions(t *testing.T) {
	p := ImplementationPrompt(TaskBrief{Key: task.Key{Repo: "a/b", Number: 1}, StageInstructions: "Write the failing test first.", Context: "Keep 5xx-only."})
	stage, ctx := strings.Index(p, "## Developer workflow instructions"), strings.Index(p, "## Implementation context")
	if stage < 0 || ctx < stage || !strings.Contains(p, "Write the failing test first.") {
		t.Errorf("stage instructions must appear, before the task context:\n%s", p)
	}
	if strings.Contains(ImplementationPrompt(TaskBrief{Key: task.Key{Repo: "a/b", Number: 1}, StageInstructions: " \n"}), "## Developer workflow instructions") {
		t.Error("blank instructions must add no section")
	}
}

func TestImplementationPromptPRInstructions(t *testing.T) {
	b := TaskBrief{Key: task.Key{Repo: "a/b", Number: 825}, Branch: "fix/825-x", BaseBranch: "main", PRInstructions: "## Summary\n2-4 bullets."}
	p := ImplementationPrompt(b)
	sec, decide := strings.Index(p, "## Pull request description"), strings.Index(p, "## When you need a decision")
	if sec < 0 || decide < sec || !strings.Contains(p, "## Summary\n2-4 bullets.") || !strings.Contains(p, `must include "Closes #825"; otherwise write it as`) {
		t.Errorf("PR instructions must sit with the PR rules and keep Closes #N:\n%s", p)
	}
	b.PRInstructions = ""
	p = ImplementationPrompt(b)
	if strings.Contains(p, "## Pull request description") || !strings.Contains(p, `must include "Closes #825".`) {
		t.Errorf("without PR instructions only Closes #N is required:\n%s", p)
	}
}

func TestImplementationPromptRequiresTestFirstBehaviorTests(t *testing.T) {
	p := ImplementationPrompt(TaskBrief{Key: task.Key{Repo: "a/b", Number: 7}, StageInstructions: "Run make test."})
	for _, want := range []string{
		"## Tests",
		"write the test that checks it before you write the code",
		"check that it fails for the expected reason",
		"rerun the relevant tests",
		"Commit a test only once it passes. Never commit a failing test.",
		"Name each test and subtest after the condition and the expected behavior",
		"The name must match what the assertions check.",
		"Do not rewrite existing tests only to meet these rules.",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("implementation prompt missing %q", want)
		}
	}
	if strings.Index(p, "Run make test.") > strings.Index(p, "## Tests") {
		t.Error("the test rules must follow the developer's stage instructions")
	}
}

func TestImplementationPromptTestRulesApplyWithoutStageInstructions(t *testing.T) {
	p := ImplementationPrompt(TaskBrief{Key: task.Key{Repo: "a/b", Number: 7}})
	if !strings.Contains(p, "## Tests") || !strings.Contains(p, "Never commit a failing test.") {
		t.Errorf("a prompt without stage instructions lacks the test rules:\n%s", p)
	}
}

func TestPlanningPromptAsksPlanToNameEachTestBehavior(t *testing.T) {
	const rule = "the implementation plan names each test by its readable name and the behavior it checks"
	if p := ImplementationPrompt(TaskBrief{Key: task.Key{Repo: "a/b", Number: 7}, Planning: true}); !strings.Contains(p, rule) {
		t.Errorf("planning prompt missing %q:\n%s", rule, p)
	}
	if p := ImplementationPrompt(TaskBrief{Key: task.Key{Repo: "a/b", Number: 7}}); strings.Contains(p, rule) {
		t.Error("an implementation prompt carries the planning test rule")
	}
}

func TestPlanningPromptOmitsTheImplementationTestSection(t *testing.T) {
	p := ImplementationPrompt(TaskBrief{Key: task.Key{Repo: "a/b", Number: 7}, Planning: true})
	if strings.Contains(p, "## Tests") || strings.Contains(p, "Never commit a failing test.") {
		t.Errorf("a planning prompt carries the implementation test section:\n%s", p)
	}
}

func TestConflictRepairPromptOmitsTheTestFirstSection(t *testing.T) {
	p := ImplementationPrompt(TaskBrief{Branch: "feat/1-x", BaseBranch: "main", PR: 912, Conflict: &Conflict{PR: 912, Base: "main", BaseHead: "ba5e001"}})
	if strings.Contains(p, "## Tests") {
		t.Errorf("a conflict repair prompt carries the test section:\n%s", p)
	}
}

func TestMergeBeforeReviewPromptOmitsTheTestFirstSection(t *testing.T) {
	p := ImplementationPrompt(TaskBrief{Branch: "feat/1-x", BaseBranch: "main", Merge: &MergeBrief{Base: "main", Files: []string{"retry.go"}}})
	if strings.Contains(p, "## Tests") {
		t.Errorf("a merge-before-review prompt carries the test section:\n%s", p)
	}
}

// turnLines returns the log lines in the agent:model:step form.
func turnLines(log string) []string {
	var lines []string
	for _, l := range strings.Split(log, "\n") {
		if strings.HasPrefix(l, "claude:") && !strings.HasPrefix(l, "claude: ") || strings.HasPrefix(l, "codex:") {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestClaudeLogsAgentModelAndStep(t *testing.T) {
	var commands []Command
	exec := func(_ context.Context, cmd Command) error {
		commands = append(commands, cmd)
		result := prOpened
		if slices.Contains(cmd.Args, "--no-session-persistence") {
			result = reviewJSON
		}
		fmt.Fprintf(cmd.Stdout, `{"type":"result","subtype":"success","structured_output":%s}`, result)
		return nil
	}
	var log bytes.Buffer
	c := Claude{Bin: "claude", Model: "opus", PlanModel: "sonnet", ReviewModel: "opus", Exec: exec, NewSessionID: func() string { return "sid-1" }}
	for _, in := range []StartInput{
		{Worktree: "/wt", Log: &log, Stage: task.StagePlan, Step: StepPlan},
		{Worktree: "/wt", Log: &log, Stage: task.StageImplement, Step: StepImplement},
		{Worktree: "/wt", Log: &log, Stage: task.StageImplement, Step: StepFeedback},
		{Worktree: "/wt", Log: &log, Stage: task.StageImplement, Step: StepMerge},
	} {
		if _, err := c.Start(context.Background(), in, func(task.Session) {}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Review(context.Background(), StartInput{Worktree: "/wt", Log: &log, Step: StepReview}); err != nil {
		t.Fatal(err)
	}
	want := []string{"claude:sonnet:plan", "claude:opus:implement", "claude:opus:feedback", "claude:opus:merge", "claude:opus:review"}
	got := turnLines(log.String())
	if !slices.Equal(got, want) {
		t.Fatalf("turn lines = %q, want %q", got, want)
	}
	for i, line := range got {
		if model := strings.Split(line, ":")[1]; model != argValue(commands[i].Args, "--model") {
			t.Errorf("line %q names model %q, argv has %q", line, model, argValue(commands[i].Args, "--model"))
		}
	}
}

func TestClaudeResumeLogsItsOwnLine(t *testing.T) {
	exec := func(_ context.Context, cmd Command) error {
		fmt.Fprintf(cmd.Stdout, `{"type":"result","subtype":"success","structured_output":%s}`, prOpened)
		return nil
	}
	var log bytes.Buffer
	c := Claude{Bin: "claude", Model: "opus", Exec: exec}
	in := StartInput{Worktree: "/wt", Log: &log, Stage: task.StageImplement, Step: StepFeedback}
	for range 2 {
		if _, err := c.Resume(context.Background(), task.Session{Agent: task.Claude, ID: "sid-1"}, in); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := turnLines(log.String()), []string{"claude:opus:feedback", "claude:opus:feedback"}; !slices.Equal(got, want) {
		t.Errorf("turn lines = %q, want %q", got, want)
	}
}

func TestCodexLogsAgentModelAndStep(t *testing.T) {
	var commands []Command
	exec := func(_ context.Context, cmd Command) error {
		commands = append(commands, cmd)
		fmt.Fprintln(cmd.Stdout, `{"type":"thread.started","thread_id":"th-1"}`)
		result := prOpened
		if slices.Contains(cmd.Args, "--ephemeral") {
			result = reviewJSON
		}
		return os.WriteFile(argValue(cmd.Args, "-o"), []byte(result), 0o600)
	}
	var log bytes.Buffer
	c := Codex{Bin: "codex", Model: "gpt-6-sol", PlanModel: "gpt-6-luna", ReviewModel: "gpt-6-sol", Exec: exec}
	if _, err := c.Start(context.Background(), StartInput{Worktree: "/wt", Log: &log, Stage: task.StagePlan, Step: StepPlan}, func(task.Session) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(context.Background(), StartInput{Worktree: "/wt", Log: &log, Stage: task.StageImplement, Step: StepImplement}, func(task.Session) {}); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{StepFeedback, StepMerge} {
		in := StartInput{Worktree: "/wt", Log: &log, Stage: task.StageImplement, Step: step}
		if _, err := c.Resume(context.Background(), task.Session{Agent: task.Codex, ID: "th-1"}, in); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Review(context.Background(), StartInput{Worktree: "/wt", Log: &log, Step: StepReview}); err != nil {
		t.Fatal(err)
	}
	want := []string{"codex:gpt-6-luna:plan", "codex:gpt-6-sol:implement", "codex:gpt-6-sol:feedback", "codex:gpt-6-sol:merge", "codex:gpt-6-sol:review"}
	got := turnLines(log.String())
	if !slices.Equal(got, want) {
		t.Fatalf("turn lines = %q, want %q", got, want)
	}
	for i, line := range got {
		if model := strings.Split(line, ":")[1]; model != argValue(commands[i].Args, "-m") {
			t.Errorf("line %q names model %q, argv has %q", line, model, argValue(commands[i].Args, "-m"))
		}
	}
}

func TestTurnLogShowsDefaultForEmptyModel(t *testing.T) {
	exec := func(_ context.Context, cmd Command) error {
		fmt.Fprintf(cmd.Stdout, `{"type":"result","subtype":"success","structured_output":%s}`, prOpened)
		return nil
	}
	var log bytes.Buffer
	c := Claude{Bin: "claude", Exec: exec, NewSessionID: func() string { return "sid-1" }}
	if _, err := c.Start(context.Background(), StartInput{Worktree: "/wt", Log: &log, Step: StepImplement}, func(task.Session) {}); err != nil {
		t.Fatal(err)
	}
	if got, want := turnLines(log.String()), []string{"claude:default:implement"}; !slices.Equal(got, want) {
		t.Errorf("turn lines = %q, want %q", got, want)
	}
}

func TestTurnLogOmittedWithoutStep(t *testing.T) {
	exec := func(_ context.Context, cmd Command) error {
		fmt.Fprintf(cmd.Stdout, `{"type":"result","subtype":"success","structured_output":%s}`, prOpened)
		return nil
	}
	var log bytes.Buffer
	c := Claude{Bin: "claude", Model: "opus", Exec: exec, NewSessionID: func() string { return "sid-1" }}
	if _, err := c.Start(context.Background(), StartInput{Worktree: "/wt", Log: &log}, func(task.Session) {}); err != nil {
		t.Fatal(err)
	}
	if got := turnLines(log.String()); len(got) != 0 {
		t.Errorf("turn lines = %q, want none", got)
	}
}
