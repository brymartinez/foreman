# Control Layer Progress

## Current stage

Stage 3 (decision escalation) is complete. Stage 4 has not started.

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
  binaries (14 checks).
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

### Stage 3

- When an agent ends a turn blocked, the task becomes NEEDS_DECISION and the question is
  saved in the `decisions` frontmatter. `inbox` lists it under NEEDS DECISION, and
  `show` prints it in the `BLOCKED: Need decision` layout.
- `foreman respond <issue>` shows the question, reads the answer (Enter picks the
  recommendation, a label picks an option, or type free text), records it in the
  frontmatter plus a readable line under `## Decisions` in the body, and relaunches the
  runner.
- The runner resumes the agent's own session with the answer. If that session is gone,
  it starts a fresh turn whose prompt lists every decision already made.
- Validated by unit tests for answer resolution, the decision note, resume vs. fresh
  start, the no-session fallback, not falling back on other errors, question validation,
  recommendation parsing, the BLOCKED layout, and resume argv for both CLIs.
- Validated end to end against `brymartinez/nest-starter#1` with push disabled. The
  context file told the agent to ask a question first, then report the answer it
  received.
  - Real Claude went to NEEDS_DECISION. Inbox and show displayed the question.
    `echo B | foreman respond 1` recorded "B. Nest 11". The same session ID resumed and
    reported "resumed | B. Nest 11".
  - Real Codex did the same with a free-text answer, on the same thread ID.
  - With a corrupted session ID, the runner logged "session is gone", started a fresh
    session, and it reported the recorded answer.
- `scripts/probe-agents.sh` gained a check that `codex exec resume` can write through
  `-c sandbox_mode`.

## Known limitations

- The PR number from a `pr_opened` outcome is recorded but not yet checked with `gh`
  (Stage 4).
- REVIEWING has no reviewer yet (Stage 5), and READY_FOR_HUMAN is never reached
  (Stage 6).
- Only one question can be pending at a time. Agents ask one per turn.
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
- D26. How `respond` reads input. D27. Decisions go in the frontmatter plus an
  append-only body note. D28. Resume first, rebuild only when the session is gone.
  D29. Questions are validated at the boundary.

## Next stage

Stage 4 is autonomous PR completion. The agent already owns the push and the PR (D7, and
the prompt since Stage 2). Stage 4 has foreman confirm the result and records it.

- Add `GitHubClient.GetPullRequest`. When the outcome is `pr_opened`, check that the PR
  exists, is open, has `headRefName` equal to the task branch, and is in the task's repo.
  If any check fails, the task becomes FAILED with the reason.
- If the agent reports `pr_opened` without a usable number, look the PR up by branch
  (`gh pr list --head <branch>`).
- Show the PR URL in `show`, and the PR number in `status` and `inbox`.
- Done when `start`, then the agent's run, leaves a PR on GitHub whose number is in the
  task file. The end-to-end check needs a repo where a real PR is acceptable. The
  current probes stop before pushing.
