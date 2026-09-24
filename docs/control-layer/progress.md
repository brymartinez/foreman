# Control Layer Progress

## Current stage

Stage 1 (GitHub sync + Markdown state) is complete. Stage 2 has not started.

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

### Stage 1

- `foreman inbox` runs the `gh` searches, re-fetches tracked tasks, compares them with the
  task files, prints NEEDS DECISION, READY FOR YOU, CHANGED, and NEW, and marks what it
  showed as seen.
- `foreman status` lists delegated work, most urgent first. `foreman show <issue>` shows
  one task's durable state and accepts `owner/repo#N`.
- `internal/store` (`Markdown`) keeps user frontmatter keys, comments, and the body.
  `internal/github` wraps `gh`, `internal/config` locates the tasks directory, and
  `internal/render` formats terminal output.
- Validated by unit tests for store round-trips and human edits, the reconcile table,
  `gh` JSON parsing against fixtures, config precedence, and render output. Also run end
  to end against the user's real GitHub account in a throwaway tasks directory: the first
  inbox listed 41 NEW items, the second showed "Nothing new" with 41 hidden, a bare
  `show 49` reported the ambiguity, and a delegated task edited by hand for a closed
  issue came up as CHANGED · closed with the user's comment, tags, and body intact.

## Known limitations

- `start` and `respond` are not implemented yet (Stages 2 and 3).
- Each search returns at most 100 results.
- Mentions only come from issues. Mentions in PR comments are not searched.
- A reason being lost (for example being unassigned) is not reported as a change.
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
- D13. `go.yaml.in/yaml/v3`, with a merge that only touches owned keys.
  D14. Tasks directory resolution.
- D15. Inbox semantics. D16. `owner/repo#N` references. D17. Ports grow per stage.

## Next stage

Stage 2 is dispatch: `foreman start <issue> --agent claude|codex [--context path.md]`.

- Resolve the repo from `owner/repo#N`, or from the current checkout's `gh repo view`,
  and add `GitHubClient.GetIssue`.
- Implement `WorktreeManager.Ensure` over `git worktree`, using the D12 names and a
  configurable worktrees root. It must be idempotent.
- Create or update the task file: state WORKING, the delegation fields, and the context
  file path.
- Add the Claude and Codex adapters for `Start`: argv built per capabilities.md, stdin
  from `/dev/null`, output parsed through `agent.ParseOutcome`.
- Launch a detached runner for that one task (D5), and record its pid and log path.
- Done when `inbox`, then `start 825 --agent claude --context 825.md`, creates the
  worktree, starts Claude with the full task context, sets the task file to WORKING, and
  `status` shows it.
