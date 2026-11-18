package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/brymartinez/foreman/internal/task"
)

var ErrAwaitingTriage = errors.New("awaiting triage")

func (a App) localTriage(key task.Key) (string, error) {
	if a.Triage == nil {
		return "", errors.New("local triage storage is unavailable")
	}
	text, err := a.Triage.Read(key)
	if err != nil {
		return "", fmt.Errorf("read local triage: %w", err)
	}
	if text == "" {
		return "", ErrAwaitingTriage
	}
	return text, nil
}

func (a App) readRunScope(ctx context.Context, rec *task.Record) (confirmedScope, error) {
	if !rec.Delegation.LocalDocuments || rec.Delegation.TrackerTriage {
		return a.readScope(ctx, rec.Key)
	}
	text, err := a.localTriage(rec.Key)
	if err != nil {
		return confirmedScope{}, err
	}
	return confirmedScope{text: text, digest: task.Digest(text)}, nil
}

func (a App) markLocalTriage(entries []InboxEntry) {
	for i := range entries {
		e := &entries[i]
		if e.Delegated || e.Item.Kind != task.Issue || e.Item.State != "open" {
			continue
		}
		e.LocalTriage = true
		_, err := a.localTriage(e.Item.Key)
		e.Triaged = err == nil || errors.Is(err, ErrAwaitingTriage) && slices.Contains(e.Item.Labels, "triaged")
		if err != nil && !errors.Is(err, ErrAwaitingTriage) {
			e.TriageError = err.Error()
		}
	}
}
