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
func EnsureTasksDir(in io.Reader, out io.Writer, path string) (bool, error) {
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
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		return false, nil
	}
	answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
	if answer != "" && answer != "y" && answer != "yes" {
		return false, nil
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return false, fmt.Errorf("create tasks directory %s: %w", path, err)
	}
	return true, nil
}
