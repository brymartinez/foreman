package main

import (
	"os"
	"testing"

	"github.com/brymartinez/foreman/internal/cli"
	"github.com/brymartinez/foreman/internal/config"
)

func TestAutoModeLaunchFlagOverridesTheSavedChoiceWithoutWritingIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := config.StatePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveAutoEnabled(path, false); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)

	auto := autoMode(config.Auto{}, cli.AutoOn)
	if !auto.Enabled || auto.LoadErr != nil {
		t.Fatalf("autoMode(--auto) = enabled %v, err %v; want on with no error", auto.Enabled, auto.LoadErr)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatalf("launch override wrote the state file: %q became %q", before, after)
	}
	if err := config.SaveAutoEnabled(path, true); err != nil {
		t.Fatal(err)
	}
	if auto := autoMode(config.Auto{}, cli.AutoOff); auto.Enabled {
		t.Fatal("autoMode(--no-auto) is on; want off over a saved on")
	}
}

func TestAutoModeWithoutAFlagUsesTheSavedChoice(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, _ := config.StatePath()
	if err := config.SaveAutoEnabled(path, false); err != nil {
		t.Fatal(err)
	}
	if auto := autoMode(config.Auto{}, cli.AutoSaved); auto.Enabled {
		t.Fatal("autoMode with no flag is on; want the saved off")
	}
}

func TestAutoModeToggleStillSavesAfterALaunchOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	auto := autoMode(config.Auto{}, cli.AutoOn)
	if auto.Save == nil {
		t.Fatal("Save is nil; the a key would not persist")
	}
	if err := auto.Save(false); err != nil {
		t.Fatal(err)
	}
	path, _ := config.StatePath()
	if on, err := config.LoadAutoEnabled(path); err != nil || on {
		t.Fatalf("saved choice = %v, %v; want off", on, err)
	}
}

func TestAutoModeOverrideHidesAnUnreadableSavedChoice(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, _ := config.StatePath()
	if err := os.MkdirAll(path, 0o755); err != nil { // a directory cannot be read as the file
		t.Fatal(err)
	}
	if auto := autoMode(config.Auto{}, cli.AutoSaved); auto.LoadErr == nil {
		t.Fatal("no flag: LoadErr is nil; want the read error shown")
	}
	if auto := autoMode(config.Auto{}, cli.AutoOff); auto.LoadErr != nil || auto.Enabled {
		t.Fatalf("--no-auto: enabled %v, err %v; want off with no error", auto.Enabled, auto.LoadErr)
	}
}
