package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/brymartinez/foreman/internal/cli"
	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/notion"
	"github.com/brymartinez/foreman/internal/obsidian"
	"github.com/brymartinez/foreman/internal/workspace"
)

// newBackend builds the workspace store the config names. It needs the token and
// target Validate checks, and it reads the token from the environment only.
func newBackend(s config.Storage) (workspace.Backend, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	switch s.Backend {
	case config.BackendNotion:
		return &notion.Backend{C: notion.New(s.NotionToken()), ParentID: s.Notion.ParentPageID}, nil
	case config.BackendObsidian:
		return &obsidian.Backend{Vault: s.Obsidian.Vault, Folder: s.Obsidian.Folder}, nil
	}
	return nil, fmt.Errorf("storage.backend %q has no shared store", s.Backend)
}

// ensureStore proves a destination is reachable by creating what the store needs.
// The gate runs it once, before it binds an empty workspace to the destination.
func ensureStore(s config.Storage) error {
	backend, err := newBackend(s)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return backend.Ensure(ctx)
}

// storageMove copies the SQLite workspace into the configured store. It opens the
// database without the gate and builds no runner, learner, or agent.
func storageMove(ctx context.Context, cfg config.Config, stdout, stderr io.Writer) int {
	st, err := openStateGated(cfg, stderr, false)
	if err != nil {
		fmt.Fprintf(stderr, "foreman: %v\n", err)
		return 1
	}
	defer st.Close()
	sync, ok := st.Workspace.(*workspace.Sync)
	if !ok {
		fmt.Fprintln(stderr, "foreman: storage move needs storage.backend notion or obsidian")
		return 1
	}
	sync.LockWait = time.Second
	_, err = workspace.Move{Sync: sync, Storage: cfg.Storage, Report: func(format string, args ...any) {
		fmt.Fprintf(stdout, format+"\n", args...)
	}}.Run(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "foreman: %v\n", err)
		return 1
	}
	return 0
}

// runStorageCommand runs `foreman storage check` and `foreman storage move`. Neither
// needs the stage files or an agent, and neither opens the database through the
// gate, which would refuse an unmoved workspace.
func runStorageCommand(ctx context.Context, cfg config.Config, cmd cli.Command, stdout, stderr io.Writer) int {
	if cfg.Storage.Backend == config.BackendSQLite || cfg.Storage.Backend == "" {
		fmt.Fprintln(stdout, "storage.backend is sqlite: the workspace lives in the local database, so there is nothing to check or move.")
		return 0
	}
	if cmd.Name == cli.StorageMove {
		return storageMove(ctx, cfg, stdout, stderr)
	}
	backend, err := newBackend(cfg.Storage)
	if err != nil {
		fmt.Fprintf(stderr, "foreman: %v\n", err)
		return 1
	}
	err = backend.Check(ctx, func(s workspace.Step) {
		if s.Err != nil {
			fmt.Fprintf(stdout, "failed  %s: %v\n", s.Name, s.Err)
			return
		}
		fmt.Fprintf(stdout, "passed  %s\n", s.Name)
	})
	if err != nil {
		fmt.Fprintf(stderr, "foreman: the storage check failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "The %s store works.\n", cfg.Storage.Backend)
	return 0
}
