package notifications

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNativeCallsThePlatformNotifier(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	for _, name := range []string{"osascript", "notify-send"} {
		script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >>" + calls + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := (Native{}).Notify(`Foreman o/r#1`, `Needs "your" decision`); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(calls)
	want := map[string]string{
		"darwin": `-e display notification "Needs \"your\" decision" with title "Foreman o/r#1"`,
		"linux":  `Foreman o/r#1 Needs "your" decision`,
	}[runtime.GOOS]
	if strings.TrimSpace(string(got)) != want {
		t.Errorf("notifier called with %q, want %q", got, want)
	}
}
