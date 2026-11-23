package main

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/config"
)

func TestOpenStateRefusesNotionWhileTheDatabaseStillHoldsWork(t *testing.T) {
	t.Setenv(config.NotionTokenEnv, "secret_x")
	dir := t.TempDir()
	cfg := config.Config{Database: filepath.Join(dir, "foreman.db"), TasksDir: filepath.Join(dir, "t"), TriageDir: filepath.Join(dir, "tr"), KnowledgeDir: filepath.Join(dir, "k")}
	st, err := openState(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	st.DB.Exec(`INSERT INTO local_issues (id, title, status, created_at, updated_at) VALUES ('L-1','t','open','x','x')`)
	st.Close()
	cfg.Storage = config.Storage{Backend: config.BackendNotion}
	cfg.Storage.Notion.ParentPageID = "0123456789abcdef0123456789abcdef"
	if _, err := openState(cfg, io.Discard); err == nil || !strings.Contains(err.Error(), "foreman storage move") {
		t.Fatalf("err = %v", err)
	}
}
