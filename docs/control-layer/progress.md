# Control Layer Progress

## Current stage

Stage 0 (capability research and architecture) is complete. Stage 1 has not started.

## Completed

### Stage 0

- `capabilities.md` covers the verified behavior of Claude Code, Codex, gh, and git
  worktree, and lists the design assumptions the tooling can't support as written.
- `architecture.md` covers the package layout, the execution model, and the lifecycle.
- The Go skeleton includes the task model (`internal/task`), the agent outcome contract
  and strict JSON schema (`internal/agent`), the ports (`internal/app/ports.go`), and
  the argv parser (`internal/cli`). Every command exits with "not implemented (Stage N)".
- `internal/task/naming.go` gives the shared `<issue>-<slug>` name for branches and
  worktrees.
- `scripts/probe-agents.sh` re-checks the agent CLI capabilities against the real
  binaries (13 checks).
- Validated with `go vet ./...`, `go test ./...`, `go build ./cmd/foreman`, and
  `scripts/probe-agents.sh`.

## Known limitations

- No command is implemented yet.
- `recommendation` in a blocked outcome is free text, not an option label (see
  capabilities.md, item 5).

## Architectural decisions

Every decision point is logged in [decisions.md](decisions.md), including who made it and
what it replaced. Current summary:

- D1. Go. D2. Go 1.27.1 through Homebrew. D3. The binary is `foreman`.
- D4. Headless agent turns ending in a structured outcome: `pr_opened`, `blocked`, or
  `failed`.
- D5. One detached runner process per task, with no daemon.
- D7 (supersedes D6). The implementation agent pushes and opens the PR, and foreman
  confirms with `gh` that it exists.
- D8. Claude uses `acceptEdits` and `--allowedTools`. Codex uses `workspace-write` with
  network access. The dangerous flags are never the default.
- D9. One Markdown file per `repo + number`. D10. Disposable reviewer sessions.
  D11. At most 3 review rounds.
- D12. The branch is `agent/<issue>-<slug>` and the worktree directory is
  `<issue>-<slug>`, with at most 5 words and 40 characters.

## Next stage

Stage 1 implements `inbox`, `status`, and `show`.

- `internal/store` holds `MarkdownTaskStore`, using `go.yaml.in/yaml/v3`. The tasks
  directory comes from `FOREMAN_TASKS_DIR` or `~/.config/foreman/config.yaml`.
- `internal/github` wraps `gh search issues|prs` and `gh issue|pr view`.
- `internal/app/inbox.go` holds a pure diff function that compares GitHub items with
  stored snapshots and groups them as NEW, CHANGED, NEEDS DECISION, READY FOR YOU, and
  hidden.
- Tests cover the diff table, store round-trips (including human edits to the body),
  and the `gh` JSON parsing using recorded fixtures.
