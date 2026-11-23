package main

import (
	"context"
	"fmt"
	"io"

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

// storageMove is set by the move service.
var storageMove = func(ctx context.Context, cfg config.Config, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, "foreman: storage move is not available yet")
	return 1
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
