package db

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCreatesTheVersionedSchema(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "sub", "foreman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var version int
	if err := d.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != SchemaVersion() {
		t.Fatalf("schema version = %d, %v; want %d", version, err, SchemaVersion())
	}
	for _, table := range []string{"tasks", "triage", "start_locks", "local_issues", "learnings", "evaluations", "attempts", "locks", "meta", "workspace", "workspace_entries"} {
		if _, err := d.Exec(`SELECT 1 FROM ` + table + ` LIMIT 1`); err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
	var mode string
	if err := d.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal mode = %q, %v; want wal so the dashboard and runners share the file", mode, err)
	}
}

func TestReopeningAppliesNothingTwice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreman.db")
	for range 3 {
		d, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != len(migrations) {
			t.Fatalf("migrations recorded = %d, %v; want %d", n, err, len(migrations))
		}
		d.Close()
	}
}

func TestADatabaseFromANewerForemanIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreman.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, 'future', 'now')`, SchemaVersion()+1); err != nil {
		t.Fatal(err)
	}
	d.Close()
	if _, err := Open(path); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("Open = %v, want ErrNewerSchema", err)
	}
}

func TestAFailedMigrationLeavesNoPartialSchema(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "foreman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	err = apply(d, migration{version: 99, name: "broken", sql: `CREATE TABLE half (x INTEGER); CREATE TABLE half (x INTEGER);`})
	if err == nil {
		t.Fatal("a broken migration succeeded")
	}
	if _, err := d.Exec(`SELECT 1 FROM half`); err == nil {
		t.Error("the table from a failed migration survived")
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 99`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a failed migration was recorded: %d, %v", n, err)
	}
}

func TestOpenRefusesAnEmptyPathInsteadOfUsingATemporaryDatabase(t *testing.T) {
	for _, p := range []string{"", "  "} {
		if d, err := Open(p); err == nil {
			d.Close()
			t.Errorf("Open(%q) succeeded", p)
		}
	}
}

// Characters that mean something in a URI must stay part of the file name.
func TestOpenKeepsURICharactersInThePath(t *testing.T) {
	for _, name := range []string{"foreman#review.db", "a?b.db", "50%.db", "with space.db", "x#y?z%20.db"} {
		path := filepath.Join(t.TempDir(), name)
		d, err := Open(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := d.Exec(`INSERT INTO meta (key, value) VALUES ('k', 'v')`); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		d.Close()
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s: the database is not at the configured path: %v", name, err)
		}
		again, err := Open(path)
		if err != nil {
			t.Fatalf("%s reopen: %v", name, err)
		}
		var v string
		if err := again.QueryRow(`SELECT value FROM meta WHERE key = 'k'`).Scan(&v); err != nil || v != "v" {
			t.Fatalf("%s: second open lost the row: %q, %v", name, v, err)
		}
		again.Close()
	}
}
