package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

// Respond records the developer's answer to the pending question and relaunches the
// runner, which resumes the agent's session with it.
func (a App) Respond(ctx context.Context, key task.Key, raw string) (*task.Record, error) {
	now := a.Now()
	_, err := a.Store.Update(ctx, key, func(cur *task.Record) (*task.Record, error) {
		if cur == nil || cur.State != task.NeedsDecision || cur.Pending() == nil {
			return nil, fmt.Errorf("%s#%d is not waiting for a decision", key.Repo, key.Number)
		}
		d := cur.Pending()
		answer, err := ResolveAnswer(d.Question, raw)
		if err != nil {
			return nil, err
		}
		d.Answer, d.AnsweredAt = answer, now
		cur.Body = appendDecisionNote(cur.Body, *d)
		cur.State = task.Working
		cur.LastOutcome = &task.Outcome{Status: "answered", Summary: answer, At: now}
		return cur, nil
	})
	if err != nil {
		return nil, err
	}
	return a.launch(ctx, key)
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
