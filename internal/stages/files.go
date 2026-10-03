// Package stages resolves the developer's workflow-stage instructions from disk:
// <GlobalDir>/<stage>.md, then <repo>/.foreman/<stage>.md. It never reads the
// agents' own instruction files (CLAUDE.md, AGENTS.md); the agent CLIs load those.
package stages

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

// RepoDir is the directory, relative to a repository root, that holds its stage files.
const RepoDir = ".foreman"

type Files struct {
	// GlobalDir holds the developer's stage files, normally ~/.config/foreman/stages.
	GlobalDir string
}

// Resolve loads both layers for stage. repoRoot is the repository (or worktree) root;
// "" skips the repo layer. Missing files are not an error; unreadable ones are.
func (f Files) Resolve(repoRoot string, stage task.Stage) (task.StageInstructions, error) {
	if _, err := task.ParseStage(string(stage)); err != nil {
		return task.StageInstructions{}, err
	}
	out := task.StageInstructions{Stage: stage}
	var err error
	if f.GlobalDir != "" {
		if out.GlobalPath, out.Global, err = read(filepath.Join(f.GlobalDir, string(stage)+".md")); err != nil {
			return task.StageInstructions{}, err
		}
	}
	if repoRoot != "" {
		if out.RepoPath, out.Repo, err = read(filepath.Join(repoRoot, RepoDir, string(stage)+".md")); err != nil {
			return task.StageInstructions{}, err
		}
	}
	return out, nil
}

// RequireGlobal fails unless every stage has a global file. The global files carry the
// agents' working rules, so foreman does not run without them.
func (f Files) RequireGlobal() error {
	var missing []string
	for _, st := range task.Stages {
		path := filepath.Join(f.GlobalDir, string(st)+".md")
		found, _, err := read(path)
		if err != nil {
			return err
		}
		if found == "" {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing global stage instructions: %s; run `make config` from the foreman repository, or copy defaults/stages/*.md into %s",
			strings.Join(missing, ", "), f.GlobalDir)
	}
	return nil
}

// read returns the path and content of an existing file, or empty strings when there
// is no file at path.
func read(path string) (string, string, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("stage instructions %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("stage instructions %s: not a regular file", path)
	}
	b, err := os.ReadFile(path)
	if pe := (*fs.PathError)(nil); errors.As(err, &pe) {
		return "", "", fmt.Errorf("stage instructions %s: %w", path, pe.Err)
	} else if err != nil {
		return "", "", fmt.Errorf("stage instructions %s: %w", path, err)
	}
	return path, string(b), nil
}

// RepoRoot is the top of the git working tree containing dir, or "" when dir is not
// inside one. Tasks use their worktree instead.
func RepoRoot(ctx context.Context, dir string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "not a git repository") {
			return "", nil
		}
		return "", fmt.Errorf("git rev-parse --show-toplevel: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
