package main

import (
	"database/sql"
	"fmt"
	"io"

	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/db"
	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/localissues"
	"github.com/brymartinez/foreman/internal/store"
)

// state is everything Foreman persists, opened once per process from the one
// SQLite database.
type state struct {
	DB        *sql.DB
	Tasks     store.SQLite
	Triage    store.SQLiteTriage
	Issues    localissues.Store
	Knowledge knowledge.SQLStore
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
		fmt.Fprintf(w, "foreman: moved %d tasks, %d triage records, and %d repositories' learnings into %s; deleted %d Linear records. Your original task files are in %s/.pre-sqlite.\n",
			rep.Tasks, rep.Triage, rep.Repositories, cfg.Database, rep.LinearRemoved, cfg.TasksDir)
	}
	return state{
		DB:        d,
		Tasks:     store.SQLite{DB: d},
		Triage:    store.SQLiteTriage{DB: d},
		Issues:    localissues.Store{DB: d},
		Knowledge: knowledge.SQLStore{DB: d, Dir: cfg.KnowledgeDir},
	}, nil
}
