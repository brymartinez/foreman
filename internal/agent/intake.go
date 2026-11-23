package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

const intakeSchema = `{
  "type": "object", "additionalProperties": false, "required": ["reply", "draft"],
  "properties": {
    "reply": {"type": "string"},
    "draft": {"type": ["object", "null"], "additionalProperties": false,
      "required": ["title", "description", "source_url", "repo", "decisions"],
      "properties": {
        "title": {"type": "string"}, "description": {"type": "string"},
        "source_url": {"type": "string"}, "repo": {"type": "string"},
        "decisions": {"type": "string"}
      }
    }
  }
}`

type IntakeDraft struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	SourceURL   string `json:"source_url"`
	Repo        string `json:"repo"`
	Decisions   string `json:"decisions"`
}

type IntakeReply struct {
	Text  string       `json:"reply"`
	Draft *IntakeDraft `json:"draft"`
}

func ParseIntakeReply(raw []byte) (IntakeReply, error) {
	var reply IntakeReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return IntakeReply{}, errors.New("the triage agent returned invalid reply data")
	}
	if strings.TrimSpace(reply.Text) == "" {
		return IntakeReply{}, errors.New("the triage agent returned an empty reply")
	}
	return reply, nil
}

// Intake runs one resumable interview turn with web lookup available. Foreman
// presents the resulting draft and owns the write after user confirmation.
func (c Claude) Intake(ctx context.Context, in StartInput, session task.Session) (task.Session, IntakeReply, error) {
	c.Model = in.Model
	c.Exec = intakeStdin(c.Exec, in.Prompt)
	flag := "--resume"
	if session.ID == "" {
		session = task.Session{Agent: task.Claude, ID: NewUUID()}
		flag = "--session-id"
	}
	args := []string{
		"-p", flag, session.ID,
		"--output-format", "stream-json", "--verbose", "--json-schema", intakeSchema,
		"--safe-mode", "--restricted", "--strict-mcp-config", "--permission-mode", "dontAsk",
		"--tools", "WebFetch,WebSearch", "--allowedTools", "WebFetch,WebSearch",
	}
	raw, _, err := c.exec(ctx, in, args)
	if err != nil {
		return task.Session{}, IntakeReply{}, err
	}
	reply, err := ParseIntakeReply(raw)
	return session, reply, err
}

func (c Codex) Intake(ctx context.Context, in StartInput, session task.Session) (task.Session, IntakeReply, error) {
	c.Model, c.ReasoningEffort = in.Model, in.ReasoningEffort
	c.Exec = intakeStdin(c.Exec, in.Prompt)
	in.Prompt = "-"
	next, raw, _, err := c.exec(ctx, in, []byte(intakeSchema), func(schema, last string) []string {
		args := []string{"exec"}
		if session.ID != "" {
			args = append(args, "resume")
		}
		args = append(args, "--json", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules",
			"-c", `sandbox_mode="read-only"`, "-c", `web_search="live"`, "--output-schema", schema, "-o", last)
		for _, feature := range codexEvaluatorOff {
			args = append(args, "-c", "features."+feature+"=false")
		}
		return args
	}, session.ID, func(task.Session) {})
	if err != nil {
		return task.Session{}, IntakeReply{}, err
	}
	reply, err := ParseIntakeReply(raw)
	return next, reply, err
}

func intakeStdin(run Exec, prompt string) Exec {
	return func(ctx context.Context, cmd Command) error {
		cmd.Stdin = strings.NewReader(prompt)
		return run(ctx, cmd)
	}
}
