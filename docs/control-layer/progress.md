# Control Layer Progress

## Current stage

Stage 6 (readiness gate) is complete. All stages in the design are built.

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

### Stage 4

- The implementation agent still owns implementing, validating, self-review, the push,
  and the PR (D7). The prompt now requires passing validation, a clean committed tree,
  and a PR number and URL read back from `gh pr view`.
- Foreman checks every `pr_opened` claim with `gh` (D31): open, same repo, task branch,
  and task base. If the number is missing, it looks the PR up by branch. The verified PR
  number and URL, plus the agent's validation list, are saved in the task file. `show`
  prints the PR link and the ✓/✗ validation lines.
- Validated by unit tests for all eight verification outcomes, the `gh pr view` /
  `gh pr list --head` parsing, the optional PR number, and rendering.
- Validated end to end with read-only calls to real GitHub. A stub agent reported
  `pr_opened` with no number for a task on `feat/event-idempotency-via-valkey`, and
  foreman found and recorded the user's open `ridezoomo/sw-automations#90`. A made-up
  #99999 set the task to FAILED with gh's error. Real pushes and PR creation were not
  exercised (D30).

### Stage 5

- After a PR is checked, the runner has the competing agent review it: Codex reviews
  Claude's work and Claude reviews Codex's. The reviewer runs in a disposable, read-only
  session built only from durable state (D33).
- Findings resume the implementer's session. It fixes, revalidates, and pushes, then
  reports `pr_opened`. Foreman re-checks the PR, and the reviewer re-reviews with the
  earlier rounds in its prompt. The loop ends when the review is clean. After
  `review_rounds` rounds (default 3), the task escalates as a normal decision (D35).
- Every round is saved in the `reviews` frontmatter. `status` shows `review ✓` or
  `review round N: K findings`, and `show` prints the latest review's findings.
- Validated by unit tests for a clean first round, fix then re-review, the round cap to
  answer to budget reset, blocking during a fix, review failure, the reviewer mapping
  (Codex implements, Claude reviews), the fresh-turn fallback carrying findings, the
  review parser and schema, the reviewer argv (read-only, disposable), and rendering.
- Validated live with real reviewers on a local fixture: a one-line diff that retries
  4xx although the issue says 5xx only, with a stub implementer and a stand-in `gh` on
  PATH for issue and PR metadata.
  - Real Codex reviewed Claude's stub work and reported "[blocking] retry.go:6 4xx
    client errors are retried". Foreman resumed the implementer with the finding,
    re-checked the PR, and escalated at a 1-round cap.
  - Real Claude (Haiku) reviewed Codex's stub work and found the same bug for $0.14.
  - After the bug was fixed with tests, Codex returned clean, leaving the task
    REVIEWING with `review ✓`.
  - Neither reviewer changed the worktree.
- `scripts/probe-agents.sh` now has 16 checks, including that both reviewer modes
  can't write. It passes on Codex 0.156.1, which updated itself during the session.

### Stage 6

- `inbox` and `status` run the readiness gate on every idle task that has a PR (D39).
  - A clean review with passing CI, or no checks at all (D37), makes the task
    READY_FOR_HUMAN.
  - A merged PR makes it DONE, and a closed one makes it FAILED.
  - Pending CI is recorded and shown as `CI pending`.
- Failing CI resumed the implementer, at most twice (D38). D46 later removed this
  loop; see "After Stage 6".
- CI is read with `gh pr checks --json name,bucket,link`. Every check counts, and exit
  codes are ignored whenever gh printed JSON (D36).
- Views:
  - `inbox` READY FOR YOU prints the draft's checklist: agents, PR link, each
    validation command, CI, competing review, and decisions.
  - `status` shows `CI ✓`, `CI pending`, or `CI ✗ <checks>`.
  - `show` has a CI row and lists failing checks.
- Fixed while validating: clean re-reviews no longer count toward the review budget
  (D40).
- Validated by unit tests for gh check parsing (pass, skipped, pending with exit 8,
  failing plus cancelled with exit 1, empty, and "no checks reported"), and gate tests.
  - Green or none becomes ready. Pending waits. A task with a live runner is skipped.
  - Two CI fixes that resume with the failing checks, then FAILED. This runs with a
    1-round review budget as a regression test for D40.
  - A fresh-turn prompt carries the failing checks.
  - Merged becomes DONE and closed becomes FAILED, reported under CHANGED.
  - A closed issue ends idle tasks.
  - Also covered: store round-trip and rendering.
- Validated end to end with the built binary, a stand-in `gh` on PATH, and stub agents
  in a scratch directory (`/tmp`), so no real repo was touched. The sequence:
  - Pending: `CI pending`.
  - Failing: CHANGED "CI failed (test); sent back to claude, fix 1 of 2". A real
    detached runner resumed the session with the CI message, re-verified the PR, and
    had it re-reviewed. The second fix went the same way.
  - No checks: READY FOR YOU in the draft's format.
  - Merged: CHANGED "PR #912 merged", DONE, and gone from `status`.
  - User frontmatter keys and body notes survived.
- Not exercised against a real PR's checks, because the live probe was declined (D36).

## After Stage 6

- Worktree cleanup, requested by the user (D41, D42).
  - `foreman clean [<issue>] [--dry-run]` and automatic cleanup in `inbox`, configured
    by `cleanup.on_done` and `cleanup.delete_merged_branch`.
  - Validated by unit tests for dry run, merged-only branch deletion, the setting
    turned off, a closed PR, active tasks refused, dirty worktrees kept, not cleaning
    twice, and inbox notes. Also tested against real git: dirty refusal, idempotency,
    and branch deletion.
  - Validated end to end with real git and a stand-in `gh`. The sequence ran
    `start` twice, then READY, then both PRs merged. Inbox removed #825's worktree and
    branch and kept #826 because of an untracked file. `clean --dry-run` showed the plan,
    and `clean` then removed #826. `show` marks the worktree as removed.
- CI belongs to the repo owners, per the user (D46).
  - Foreman only reads GitHub's check results. Failing checks go to READY FOR YOU,
    listed with their links, instead of being sent back to the agent. Ready tasks have
    their CI refreshed.
  - The fix loop, `ci_fixes`, and the CI prompts are removed.
  - Validated by tests for the hand-off (no runner and no resume) and for the refresh
    on ready tasks, plus rendering.
  - End to end with the built binary and a stand-in `gh`, the sequence was pending,
    then failing (READY with `✗ CI: test` and its link, no runner started), then passing
    (refreshed to `✓ CI`), then merged (DONE).
- Conventional branch prefixes, requested by the user (D45).
  - Branches are `feat/…`, `fix/…` and so on, picked from `--type`, then the title
    prefix, then the labels, then `feat`. Stored `agent/…` branches keep working.
  - Validated by table tests for the type inference, the `--type` parsing and override,
    and labels parsed from `gh issue view`.
  - End to end, an unlabeled issue got `feat/825-provider-retries` and a `bug`-labeled
    issue got `fix/826-pagination`, and cleanup removed both.
- GitHub releases (D43, D44).
  - `scripts/release.sh` and `.github/workflows/release.yml`.
  - A local build produced four archives with valid checksums, and the darwin/arm64
    binary reported `foreman v0.0.0-test`.
  - The workflow has not run: the repo has no GitHub remote yet.

## Known limitations

- The gate runs only when `inbox` or `status` runs. A push to the branch after a clean
  review is not re-reviewed.
- `gh pr checks` handling is verified against gh's documented behavior and fixtures,
  not a live PR (D36).
- Validation results are self-reported by the agent (D32).
- A restart after a crash reimplements rather than resuming a half-finished review.
- Only one question can be pending at a time.
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
- D12. The branch is `<type>/<issue>-<slug>` (type prefix per D45) and the worktree directory is
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
- D30. Real push and PR are assumed (user decision). D31. PR claims are checked with gh.
  D32. Self-reported validation is recorded.
- D33. The review contract and read-only, disposable reviewers. D34. The runner drives
  the whole loop. D35. The review round cap is a budget per decision.
- D36. All checks gate, read from `gh pr checks` JSON. D37. No checks passes.
  D38. Failing CI is sent back to the implementer, at most twice. D39. When the gate
  runs and what it changes. D40. Only rounds that request changes use the review budget.
- D41. What cleanup removes. D42. Automatic cleanup from inbox, configurable.
  D43. GitHub releases through a script and a tag workflow. D44. Version stamping.
- D45. Branches use a conventional-commit type prefix instead of `agent/`.
- D46 (supersedes D38). CI belongs to the repo owners: foreman reads GitHub's checks and
  hands failures to the developer.

## Next stage

None. Stages 0 to 6 of the design are complete. Any further work, such as re-checking
CI on ready tasks, re-reviewing after a manual push, or required-checks-only mode, needs
a new request.
