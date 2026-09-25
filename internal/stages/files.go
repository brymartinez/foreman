// Package stages resolves the developer's workflow-stage instructions from disk:
// <GlobalDir>/<stage>.md, then <repo>/.foreman/<stage>.md. It never reads the
// agents' own instruction files (CLAUDE.md, AGENTS.md); the agent CLIs load those.
package stages

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

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
	if err != nil {
		return "", "", fmt.Errorf("stage instructions %s: %w", path, err)
	}
	return path, string(b), nil
}
