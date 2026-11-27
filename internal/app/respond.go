package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

// Respond records the developer's answer to the oldest pending question. Once every
// question is answered it relaunches the runner, which resumes the agent's session
// with all the answers.
func (a App) Respond(ctx context.Context, key task.Key, raw string) (*task.Record, error) {
	return a.respond(ctx, key, raw, "")
}

// errQuestionMoved means the task's pending question is not the one an answer was
// written for.
var errQuestionMoved = errors.New("the task is waiting on a different question now")

// respond is Respond that, given expect (a QuestionKey), refuses inside the same
// transaction when the pending question is another one.
func (a App) respond(ctx context.Context, key task.Key, raw, expect string) (*task.Record, error) {
	now := a.Now()
	release, err := a.Launches.hold()
	if err != nil {
		return nil, err
	}
	defer release()
	var token string
	rec, err := a.update(ctx, key, func(cur *task.Record) (*task.Record, error) {
		if cur == nil || cur.State != task.NeedsDecision || cur.Pending() == nil {
			return nil, fmt.Errorf("%s#%d is not waiting for a decision", key.Repo, key.Number)
		}
		if expect != "" && QuestionKey(cur) != expect {
			return nil, errQuestionMoved
		}
		d := cur.Pending()
		answer, err := ResolveAnswer(d.Question, raw)
		if err != nil {
			return nil, err
		}
		if d.Question.Issue == replaceSessionQuestion && cur.LastOutcome != nil && cur.LastOutcome.Status == "session_unavailable" {
			if answer != optionText(d.Question.Options[0]) && answer != optionText(d.Question.Options[1]) {
				return nil, fmt.Errorf("choose A to replace the session or B to pause")
			}
			d.DeliveredAt = now
			if answer == optionText(d.Question.Options[1]) {
				d.Answer, d.AnsweredAt = answer, now
				failTask(cur, "paused with the pending agent session and work saved", now)
				return cur, nil
			}
			action := cur.Delegation.Action
			if action == nil {
				return nil, fmt.Errorf("the pending action is missing")
			}
			if session := cur.Delegation.RoleSession(action.Role()); session != nil {
				session.ID = ""
			}
			action.ID = ""
			action.Started = false
		}
		d.Answer, d.AnsweredAt = answer, now
		cur.Body = appendDecisionNote(cur.Body, *d)
		if cur.Pending() == nil {
			cur.State = cur.NextStage()
			if token, err = a.claimLaunch(cur); err != nil {
				return nil, err
			}
		}
		cur.LastOutcome = &task.Outcome{Status: "answered", Summary: answer, At: now}
		return cur, nil
	})
	if err != nil || token == "" {
		return rec, err
	}
	return a.launch(ctx, key, token)
}

// ResolveAnswer turns what the developer typed into the recorded answer: empty means
// the recommended option, a bare label expands to that option, anything else is kept.
func ResolveAnswer(q task.Question, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		o, ok := q.Recommended()
		if !ok {
			return "", fmt.Errorf("no recommended option to default to; type an option label or an answer")
		}
		return optionText(o), nil
	}
	if o, ok := q.Option(raw); ok {
		return optionText(o), nil
	}
	return raw, nil
}

func optionText(o task.Option) string {
	return fmt.Sprintf("%s. %s", strings.TrimSpace(o.Label), o.Description)
}

// appendDecisionNote adds a readable line under the body's "## Decisions" heading,
// creating the section at the end if missing. Existing text is never rewritten.
func appendDecisionNote(body string, d task.Decision) string {
	note := fmt.Sprintf("- %s: %s → %s\n", d.AnsweredAt.Local().Format(time.DateOnly), d.Question.Issue, d.Answer)
	const heading = "## Decisions\n"
	start := strings.Index(body, heading)
	if start < 0 || (start > 0 && body[start-1] != '\n') {
		if body != "" && !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		return body + "\n" + heading + "\n" + note
	}
	sectionStart := start + len(heading)
	end := len(body)
	if next := strings.Index(body[sectionStart:], "\n## "); next >= 0 {
		end = sectionStart + next + 1
	}
	section := strings.TrimRight(body[sectionStart:end], "\n")
	rest := body[end:]
	joined := heading + section + "\n" + note
	if section == "" {
		joined = heading + "\n" + note
	}
	if rest != "" {
		joined += "\n"
	}
	return body[:start] + joined + rest
}
