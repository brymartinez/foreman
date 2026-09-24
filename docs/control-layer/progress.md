# Control Layer Progress

## Current stage

Stage 2 (dispatch) is complete. Stage 3 has not started.

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

### Stage 2

- `foreman start <issue> --agent claude|codex [--context path.md]`, run from a checkout.
  It resolves the repo, fetches the issue, creates or reuses the worktree
  (`agent/<issue>-<slug>` from a freshly fetched `origin/<default>`), writes the task file
  as WORKING, and spawns a detached runner (`foreman __run`).
- The runner builds the implementation prompt (issue, authoritative context file,
  workspace, the full execution contract, and the decision rules), runs one turn through
  the Claude or Codex adapter, records the session as soon as it's known, and maps the
  outcome to REVIEWING, NEEDS_DECISION, or FAILED.
- `status` and `inbox` mark a task FAILED when its runner died. Task writes are
  serialized with a lock file.
- Validated by unit tests for the adapters (argv and output parsing against recorded CLI
  output), the prompt, start guards, branch reuse on restart, the outcome mapping,
  reaping, and worktrees against real git with a bare origin.
- Validated end to end with the real binary against `brymartinez/nest-starter#1` in a
  throwaway clone with push disabled:
  - A stub agent showed the detached runner going from WORKING to REVIEWING. A second
    `start` was refused while it ran.
  - Killing the runner showed the task as FAILED, with the log path.
  - The real Claude (Haiku) and real Codex each confirmed, in their structured summary,
    that they were on `agent/1-upgrade-to-nest-12`, saw the issue title, and saw a
    sentinel from the context file. Neither changed the worktree.

## Known limitations

- `respond` is not implemented (Stage 3). A NEEDS_DECISION task can't be answered yet.
- The PR number from a `pr_opened` outcome is recorded but not yet checked with `gh`
  (Stage 4).
- REVIEWING has no reviewer yet (Stage 5), and READY_FOR_HUMAN is never reached
  (Stage 6).
- Each search returns at most 100 results. Mentions only come from issues. A lost reason
  is not reported.

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
- D18. The full execution contract and the outcome-to-state mapping arrive in Stage 2.
  D19. `start` resolves the repo and base from the current checkout.
- D20. The states `start` accepts. D21. Locked `Update` for task writes. D22. The detached
  runner and liveness checks.
- D23. Agent CLI defaults. D24. The context file is stored as a path, and sessions are
  recorded early. D25. `FOREMAN_CONFIG`.

## Next stage

Stage 3 is decision escalation: `foreman respond <issue>`.

- `show` and `inbox` present the structured question in the `BLOCKED: Need decision`
  layout (issue, context, options, recommendation, impact if wrong).
- `respond` shows the question, reads the answer from stdin (the recommended option is
  the default), writes the answer and time into the decision, sets the task to WORKING,
  and spawns the runner.
- Add `AgentAdapter.Resume`. Claude uses `claude -p --resume <id>` and Codex uses
  `codex exec resume <id>`, both with the outcome schema. If the resume fails, fall back
  to a fresh turn whose prompt is rebuilt from durable state: the issue, the context
  file, and every recorded decision.
- Done when a real ambiguity leads to NEEDS_DECISION, then `inbox` and `status` show it,
  `show` displays the question, `respond` records the answer, and the agent continues.
