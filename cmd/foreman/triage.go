package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/task"
	"github.com/brymartinez/foreman/internal/tui"
)

// triageSession runs the interactive foreman-triage skill in the agent CLI chosen
// for planning, on the developer's terminal. active is set while the session
// owns the terminal, so a Ctrl-C meant for the agent does not stop the dashboard.
func triageSession(cfg config.Config, active *atomic.Bool) func(context.Context, tui.TriageRequest) error {
	return func(ctx context.Context, req tui.TriageRequest) error {
		exe, err := os.Executable()
		if err != nil {
			exe = "foreman"
		}
		target := cfg.Models.Planning
		prompt := triagePrompt(exe, req)
		var cmd *exec.Cmd
		switch target.Agent {
		case task.Codex:
			cmd = exec.CommandContext(ctx, "codex", "--model", target.Model, prompt)
		default:
			cmd = exec.CommandContext(ctx, "claude", "--model", target.Model, prompt)
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if req.Key.Source != task.Local && req.Key.Repo != "" {
			if dir := task.RepositoryPath(cfg.RepositoriesDir, req.Key.Repo); dirExists(dir) {
				cmd.Dir = dir
			}
		}
		active.Store(true)
		defer active.Store(false)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(cmd.Path), err)
		}
		return nil
	}
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// triagePrompt is the first message of a session. The skill carries the interview;
// the prompt names the issue and the exact commands that save its result.
func triagePrompt(exe string, req tui.TriageRequest) string {
	var b strings.Builder
	b.WriteString("Use the foreman-triage skill.\n")
	if req.Key.Source == task.Local {
		fmt.Fprintf(&b, "Triage local Foreman issue %s (%s). Read it with `%s issue show %s`. ", req.Key.ExternalID, req.Title, exe, req.Key.ExternalID)
		fmt.Fprintf(&b, "When I confirm the decisions, save them with `%s issue triage %s --file FILE`.\n", exe, req.Key.ExternalID)
	} else {
		fmt.Fprintf(&b, "Triage GitHub issue %s#%d (%s). Publish the confirmed decisions and apply the triaged label as the skill describes.\n", req.Key.Repo, req.Key.Number, req.Title)
	}
	return b.String()
}
