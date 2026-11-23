package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"

	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/db"
	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/localissues"
	"github.com/brymartinez/foreman/internal/store"
	"github.com/brymartinez/foreman/internal/workspace"
)

// state is everything Foreman persists, opened once per process from the one
// SQLite database.
type state struct {
	DB        *sql.DB
	Tasks     store.SQLite
	Triage    store.SQLiteTriage
	Issues    localissues.Store
	Knowledge knowledge.SQLStore
	// Workspace is the shared store in notion or obsidian mode, and nil in sqlite mode.
	Workspace app.Workspace
}

func (s state) Close() { _ = s.DB.Close() }

// openState opens the database, applies its schema, and moves the Markdown records
// of an earlier version into it the first time. A notice on w says what an upgrade
// moved or deleted.
func openState(cfg config.Config, w io.Writer) (state, error) {
	d, err := db.Open(cfg.Database)
	if err != nil {
		return state{}, err
	}
	rep, err := store.MigrateLegacy(d, store.Legacy{TasksDir: cfg.TasksDir, TriageDir: cfg.TriageDir, KnowledgeDir: cfg.KnowledgeDir})
	if err != nil {
		d.Close()
		return state{}, fmt.Errorf("upgrade stored work into %s: %w", cfg.Database, err)
	}
	if rep.Tasks+rep.Triage+rep.Repositories+rep.LinearRemoved > 0 {
		fmt.Fprintf(w, "foreman: moved %d tasks, %d triage records, and %d repositories' learnings into %s; deleted %d Linear records.",
			rep.Tasks, rep.Triage, rep.Repositories, cfg.Database, rep.LinearRemoved)
		if rep.Tasks > 0 {
			fmt.Fprintf(w, " Your original task files are in %s/.pre-sqlite.", cfg.TasksDir)
		}
		fmt.Fprintln(w)
	}
	if err := workspace.Gate(d, cfg.Storage); err != nil {
		d.Close()
		return state{}, err
	}
	st := state{
		DB:        d,
		Tasks:     store.SQLite{DB: d},
		Triage:    store.SQLiteTriage{DB: d},
		Issues:    localissues.Store{DB: d},
		Knowledge: knowledge.SQLStore{DB: d, Dir: cfg.KnowledgeDir},
	}
	if cfg.Storage.Backend == config.BackendNotion || cfg.Storage.Backend == config.BackendObsidian {
		backend, err := newBackend(cfg.Storage)
		if err != nil {
			d.Close()
			return state{}, err
		}
		st.Workspace = &workspace.Sync{DB: d, Backend: backend, Tasks: st.Tasks, Issues: st.Issues, Knowledge: st.Knowledge, Log: workspaceLog(cfg)}
	}
	return st, nil
}

// publishIssue writes a local issue's entry to the shared store before a command
// returns. A failure never fails the command: the entry waits for the next refresh.
func (s state) publishIssue(ctx context.Context, stderr io.Writer, id string) {
	if s.Workspace == nil {
		return
	}
	if err := s.Workspace.Publish(ctx, task.Key{Source: task.Local, ExternalID: id}); err != nil {
		fmt.Fprintf(stderr, "foreman: the shared store was not updated for %s and it will be at the next refresh: %v\n", id, err)
	}
}

// publishKnowledge is publishIssue for a repository's knowledge.
func (s state) publishKnowledge(ctx context.Context, stderr io.Writer, repo string) {
	if s.Workspace == nil {
		return
	}
	if err := s.Workspace.PublishKnowledge(ctx, repo); err != nil {
		fmt.Fprintf(stderr, "foreman: the shared store was not updated for the knowledge of %s and it will be at the next refresh: %v\n", repo, err)
	}
}

// workspaceLog appends problems with the shared store to workspace.log in the log
// directory. The dashboard owns the terminal, so they never go to stderr.
func workspaceLog(cfg config.Config) func(string, ...any) {
	if cfg.LogDir == "" {
		return nil
	}
	return func(format string, args ...any) {
		if err := os.MkdirAll(cfg.LogDir, 0o700); err != nil {
			return
		}
		f, err := os.OpenFile(filepath.Join(cfg.LogDir, "workspace.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer f.Close()
		fmt.Fprintf(f, "%s "+format+"\n", append([]any{time.Now().UTC().Format(time.RFC3339)}, args...)...)
	}
}
