package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func storageHome(t *testing.T, yaml string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FOREMAN_TASKS_DIR", "")
	t.Setenv("FOREMAN_NOTION_TOKEN", "")
	path := filepath.Join(home, "config.yaml")
	t.Setenv("FOREMAN_CONFIG", path)
	if yaml != "" {
		if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func TestBackendDefaultsToSQLiteWhenDatabaseExists(t *testing.T) {
	home := storageHome(t, "")
	db := filepath.Join(home, ".local/share/foreman/foreman.db")
	os.MkdirAll(filepath.Dir(db), 0o755)
	os.WriteFile(db, nil, 0o600)
	cfg, err := Load()
	if err != nil || cfg.Storage.Backend != BackendSQLite {
		t.Fatalf("backend = %q, err = %v", cfg.Storage.Backend, err)
	}
}

func TestBackendDefaultsToNotionWithoutDatabase(t *testing.T) {
	storageHome(t, "")
	cfg, err := Load()
	if err != nil || cfg.Storage.Backend != BackendNotion {
		t.Fatalf("backend = %q, err = %v", cfg.Storage.Backend, err)
	}
}

func TestExplicitBackendWinsOverExistingDatabase(t *testing.T) {
	home := storageHome(t, "storage:\n  backend: notion\n")
	db := filepath.Join(home, ".local/share/foreman/foreman.db")
	os.MkdirAll(filepath.Dir(db), 0o755)
	os.WriteFile(db, nil, 0o600)
	cfg, err := Load()
	if err != nil || cfg.Storage.Backend != BackendNotion {
		t.Fatalf("backend = %q, err = %v", cfg.Storage.Backend, err)
	}
}

func TestUnknownBackendFailsNamingTheKey(t *testing.T) {
	storageHome(t, "storage:\n  backend: dropbox\n")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "storage.backend") || !strings.Contains(err.Error(), "sqlite") {
		t.Fatalf("err = %v", err)
	}
}

func TestNotionTokenKeyInConfigIsRejected(t *testing.T) {
	storageHome(t, "storage:\n  notion:\n    token: secret_abc\n")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secret_abc") {
		t.Fatalf("error leaks the value: %v", err)
	}
}

func TestParentPageIDIsNormalized(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	for name, in := range map[string]string{
		"bare":   id,
		"dashed": "01234567-89ab-cdef-0123-456789abcdef",
		"url":    "https://www.notion.so/My-Page-Title-" + id + "?pvs=4",
		"upper":  strings.ToUpper(id),
	} {
		storageHome(t, "storage:\n  backend: notion\n  notion:\n    parent_page_id: \""+in+"\"\n")
		cfg, err := Load()
		if err != nil || cfg.Storage.Notion.ParentPageID != id {
			t.Errorf("%s: id = %q, err = %v", name, cfg.Storage.Notion.ParentPageID, err)
		}
	}
}

func TestInvalidParentPageIDNamesTheKey(t *testing.T) {
	storageHome(t, "storage:\n  notion:\n    parent_page_id: not-a-page\n")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "storage.notion.parent_page_id") {
		t.Fatalf("err = %v", err)
	}
}

func TestStorageValidateNotion(t *testing.T) {
	storageHome(t, "storage:\n  backend: notion\n")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	err = cfg.Storage.Validate()
	if err == nil {
		t.Fatal("missing parent page must fail")
	}
	for _, want := range []string{"storage.notion.parent_page_id", "foreman storage check", "sqlite"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q lacks %q", err, want)
		}
	}
	cfg.Storage.Notion.ParentPageID = "0123456789abcdef0123456789abcdef"
	err = cfg.Storage.Validate()
	if err == nil || !strings.Contains(err.Error(), "FOREMAN_NOTION_TOKEN") {
		t.Fatalf("missing token: %v", err)
	}
	t.Setenv("FOREMAN_NOTION_TOKEN", "secret_xyz")
	if err := cfg.Storage.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.NotionToken() != "secret_xyz" {
		t.Fatal("token must come from the environment")
	}
}

func TestStorageValidateObsidian(t *testing.T) {
	vault := t.TempDir()
	storageHome(t, "storage:\n  backend: obsidian\n  obsidian:\n    vault: "+vault+"\n")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.Obsidian.Folder != "Foreman" {
		t.Fatalf("folder default = %q", cfg.Storage.Obsidian.Folder)
	}
	if err := cfg.Storage.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Storage.Obsidian.Vault = filepath.Join(vault, "missing")
	err = cfg.Storage.Validate()
	if err == nil || !strings.Contains(err.Error(), "storage.obsidian.vault") || !strings.Contains(err.Error(), "foreman storage check") {
		t.Fatalf("err = %v", err)
	}
	cfg.Storage.Obsidian.Vault = ""
	if err := cfg.Storage.Validate(); err == nil {
		t.Fatal("empty vault must fail")
	}
}

func TestStorageValidateSQLiteNeedsNothing(t *testing.T) {
	storageHome(t, "storage:\n  backend: sqlite\n")
	cfg, err := Load()
	if err != nil || cfg.Storage.Validate() != nil {
		t.Fatalf("%v %v", err, cfg.Storage.Validate())
	}
}

func TestTokenNeverAppearsInErrors(t *testing.T) {
	t.Setenv("FOREMAN_NOTION_TOKEN", "secret_leakcheck")
	s := Storage{Backend: BackendNotion}
	if err := s.Validate(); err == nil || strings.Contains(err.Error(), "secret_leakcheck") {
		t.Fatalf("err = %v", err)
	}
}
