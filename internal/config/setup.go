package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// EnsureTasksDir verifies the configured task directory, offering to create it
// when it is missing. It returns false when the user declines or reaches EOF.
// It reads one line, so in can go on to the next prompt.
func EnsureTasksDir(in *bufio.Reader, out io.Writer, path string) (bool, error) {
	info, err := os.Stat(path)
	switch {
	case err == nil:
		if !info.IsDir() {
			return false, fmt.Errorf("tasks path %s is not a directory", path)
		}
		return true, nil
	case !os.IsNotExist(err):
		return false, fmt.Errorf("tasks directory %s: %w", path, err)
	}

	fmt.Fprintf(out, "Foreman stores task Markdown files in %s.\n", path)
	fmt.Fprintln(out, "This can be an Obsidian vault folder or any normal directory.")
	fmt.Fprint(out, "Create it now? [Y/n]: ")
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}
	answer := strings.TrimSpace(strings.ToLower(line))
	if answer != "" && answer != "y" && answer != "yes" {
		return false, nil
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return false, fmt.Errorf("create tasks directory %s: %w", path, err)
	}
	return true, nil
}
