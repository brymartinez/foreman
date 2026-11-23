package workspace

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/db"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "foreman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func notionCfg() config.Storage {
	t := config.Storage{Backend: config.BackendNotion}
	t.Notion.ParentPageID = "0123456789abcdef0123456789abcdef"
	return t
}

func TestGateSQLiteAndUnsetBackendAlwaysPass(t *testing.T) {
	d := openDB(t)
	d.Exec(`INSERT INTO local_issues (id, title, status, created_at, updated_at) VALUES ('L-1','t','open','x','x')`)
	for _, b := range []config.Backend{"", config.BackendSQLite} {
		if err := Gate(d, config.Storage{Backend: b}); err != nil {
			t.Errorf("backend %q: %v", b, err)
		}
	}
}

func TestGateMarksAnEmptyDatabaseOnFirstSetup(t *testing.T) {
	t.Setenv(config.NotionTokenEnv, "secret_x")
	d := openDB(t)
	if err := Gate(d, notionCfg()); err != nil {
		t.Fatal(err)
	}
	m, err := ReadMarker(d)
	if err != nil || m == nil || m.Backend != config.BackendNotion || m.Target != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("marker = %+v, %v", m, err)
	}
	if err := Gate(d, notionCfg()); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

func TestGateRefusesAnUnmovedWorkspaceForEachKindOfWork(t *testing.T) {
	t.Setenv(config.NotionTokenEnv, "secret_x")
	for name, stmt := range map[string]string{
		"task":        `INSERT INTO tasks (id, source, repo, number, external_id, state, title, document, updated_at) VALUES ('a/b#1','github','a/b',1,'1','x','t','{}','x')`,
		"local issue": `INSERT INTO local_issues (id, title, status, created_at, updated_at) VALUES ('L-1','t','open','x','x')`,
		"triage":      `INSERT INTO triage (id, decisions, confirmed_at) VALUES ('a/b#1','x','x')`,
		"learnings":   `INSERT INTO learnings (repo, data) VALUES ('a/b','{}')`,
	} {
		d := openDB(t)
		if _, err := d.Exec(stmt); err != nil {
			t.Fatal(name, err)
		}
		err := Gate(d, notionCfg())
		if err == nil || !strings.Contains(err.Error(), "foreman storage move") {
			t.Errorf("%s: err = %v", name, err)
		}
		if m, _ := ReadMarker(d); m != nil {
			t.Errorf("%s: marker written for a database with work", name)
		}
	}
}

func TestGateRefusesAChangedBackendOrTargetAfterCutover(t *testing.T) {
	t.Setenv(config.NotionTokenEnv, "secret_x")
	d := openDB(t)
	if err := Gate(d, notionCfg()); err != nil {
		t.Fatal(err)
	}
	other := notionCfg()
	other.Notion.ParentPageID = "ffffffffffffffffffffffffffffffff"
	vault := t.TempDir()
	obs := config.Storage{Backend: config.BackendObsidian, Obsidian: config.Obsidian{Vault: vault, Folder: "Foreman"}}
	for name, s := range map[string]config.Storage{"other page": other, "obsidian": obs, "sqlite": {Backend: config.BackendSQLite}} {
		err := Gate(d, s)
		if err == nil || !strings.Contains(err.Error(), "already moved") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestGateRefusesObsidianVaultChangeAfterCutover(t *testing.T) {
	d := openDB(t)
	v1, v2 := t.TempDir(), t.TempDir()
	mk := func(v string) config.Storage {
		return config.Storage{Backend: config.BackendObsidian, Obsidian: config.Obsidian{Vault: v, Folder: "Foreman"}}
	}
	if err := Gate(d, mk(v1)); err != nil {
		t.Fatal(err)
	}
	if err := Gate(d, mk(v1)); err != nil {
		t.Fatal(err)
	}
	if err := Gate(d, mk(v2)); err == nil {
		t.Fatal("a different vault must be refused")
	}
}

func TestGateChecksSetupBeforeMarking(t *testing.T) {
	t.Setenv(config.NotionTokenEnv, "")
	d := openDB(t)
	err := Gate(d, notionCfg())
	if err == nil || !strings.Contains(err.Error(), config.NotionTokenEnv) {
		t.Fatalf("err = %v", err)
	}
	if m, _ := ReadMarker(d); m != nil {
		t.Fatal("a failed setup must not write the marker")
	}
}

func TestGateRefusesWhileAMoveRuns(t *testing.T) {
	d := openDB(t)
	d.Exec(`INSERT INTO locks (name, pid, token, since) VALUES (?, ?, 't', 'x')`, MoveLock, os.Getpid())
	for _, s := range []config.Storage{{Backend: config.BackendSQLite}, notionCfg()} {
		err := Gate(d, s)
		if err == nil || !strings.Contains(err.Error(), "foreman storage move is running") {
			t.Errorf("err = %v", err)
		}
	}
	d.Exec(`UPDATE locks SET pid = 2147483000`)
	if err := Gate(d, config.Storage{Backend: config.BackendSQLite}); err != nil {
		t.Fatalf("a dead mover's lock must not block: %v", err)
	}
}

func TestGateDoesNotMarkAnEmptyDatabaseWhenTheDestinationIsUnreachable(t *testing.T) {
	t.Setenv(config.NotionTokenEnv, "secret_x")
	d := openDB(t)
	bad := func() error { return errors.New("page not shared with the integration") }
	err := GateWith(d, notionCfg(), bad)
	if err == nil || !strings.Contains(err.Error(), "foreman storage check") {
		t.Fatalf("err = %v", err)
	}
	if m, _ := ReadMarker(d); m != nil {
		t.Fatalf("a marker %+v binds the workspace to an unreachable page", m)
	}
	// Correcting the target must still work, because nothing was bound.
	fixed := notionCfg()
	fixed.Notion.ParentPageID = "fedcba9876543210fedcba9876543210"
	if err := GateWith(d, fixed, func() error { return nil }); err != nil {
		t.Fatalf("the corrected target was refused: %v", err)
	}
	if m, _ := ReadMarker(d); m == nil || m.Target != fixed.Notion.ParentPageID {
		t.Fatalf("marker = %+v", m)
	}
}

func TestGateKeepsAMoveThatFinishedWhileTheDestinationWasBeingChecked(t *testing.T) {
	t.Setenv(config.NotionTokenEnv, "secret_x")
	d := openDB(t)
	other := notionCfg()
	other.Notion.ParentPageID = "fedcba9876543210fedcba9876543210"
	during := func() error { // a storage move to another page commits its marker
		return WriteMarker(d, Marker{Backend: other.Backend, Target: Target(other), Tasks: 7})
	}
	if err := GateWith(d, notionCfg(), during); err == nil {
		t.Fatal("the first-run gate must refuse a destination another process bound meanwhile")
	}
	m, _ := ReadMarker(d)
	if m == nil || m.Target != other.Notion.ParentPageID || m.Tasks != 7 {
		t.Fatalf("the completed move's marker was overwritten: %+v", m)
	}
}

func TestGateRefusesToMarkWhenWorkAppearedDuringTheDestinationCheck(t *testing.T) {
	t.Setenv(config.NotionTokenEnv, "secret_x")
	d := openDB(t)
	during := func() error {
		_, err := d.Exec(`INSERT INTO local_issues (id, title, status, created_at, updated_at) VALUES ('L-1','t','open','x','x')`)
		return err
	}
	if err := GateWith(d, notionCfg(), during); err == nil || !strings.Contains(err.Error(), "foreman storage move") {
		t.Fatalf("err = %v", err)
	}
	if m, _ := ReadMarker(d); m != nil {
		t.Fatalf("a marker %+v binds a database that has work", m)
	}
}
