package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

// The test binary plays three parts: the test, a launcher that spawns a runner
// and exits at once (as `foreman start` does), and the runner itself.
func TestMain(m *testing.M) {
	switch {
	case len(os.Args) > 1 && os.Args[1] == RunCommand:
		time.Sleep(300 * time.Millisecond)
		fmt.Println("written after the launcher exited")
		fmt.Fprintln(os.Stderr, "stderr after the launcher exited")
		_ = os.WriteFile(os.Getenv("FOREMAN_RUNNER_MARKER"), []byte("survived"), 0o644)
		os.Exit(0)
	case os.Getenv("FOREMAN_RUNNER_TEST") == "launch":
		if _, _, err := (Processes{LogDir: os.Getenv("FOREMAN_RUNNER_LOGS")}).Spawn(task.Key{Repo: "o/r", Number: 7}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestSpawnedRunnerOutlivesItsLauncher(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	launch := exec.Command(os.Args[0])
	launch.Env = append(os.Environ(),
		"FOREMAN_RUNNER_TEST=launch",
		"FOREMAN_RUNNER_LOGS="+dir,
		"FOREMAN_RUNNER_MARKER="+marker,
	)
	if out, err := launch.CombinedOutput(); err != nil {
		t.Fatalf("launcher: %v %s", err, out)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the runner died after its launcher exited")
		}
		time.Sleep(50 * time.Millisecond)
	}
	log, err := os.ReadFile(filepath.Join(dir, "o-r-7.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"written after the launcher exited", "stderr after the launcher exited"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log is missing %q:\n%s", want, log)
		}
	}
}
