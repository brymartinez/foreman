# Control Layer Progress

## Current stage

Stage 14 (the interactive dashboard) is complete.

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

## Stage instructions

This is a separate staged track from the user's "Foreman Stage Instructions" spec. It
has five stages: resolver, then implement, then review, then pr, then CLI inspection.

### Stage 1 (complete): stage model and resolver

- `task.Stage` (`implement`, `review`, `pr`) and `task.StageInstructions`, which keeps
  the path and text of each layer (D47).
- `internal/stages.Files` resolves `<config dir>/stages/<stage>.md` and then
  `<repo>/.foreman/<stage>.md`, joining them global first and repo second.
  `Config.StagesDir` follows the config file (D48). pr.md will be loaded into the
  implementation turn (D49).
- Nothing is wired into prompts yet.
- Validated by tests for:
  - no files, global only, repo only, both, and global before repo;
  - blank files adding nothing;
  - the right file chosen for each stage, and an unknown stage rejected;
  - the repo layer read from the repo root, not the working directory, and skipped
    when there is no root;
  - permission denied and a directory in place of a file, each reported with its path;
  - `StagesDir` following `FOREMAN_CONFIG`.

### Stage 2 (complete): implement instructions in the implementation prompt

- The runner resolves `implement` for the worktree and adds it as "Developer workflow
  instructions", before the task context. The task context and decisions win on
  conflict. A read error fails the task (D50).
- Validated by tests for:
  - the implementation prompt carrying only `implement` (not `review` or `pr`);
  - the lookup using the worktree root;
  - an unreadable file failing the task before any agent runs;
  - the section coming before the context, and blank text adding no section.
- Validated end to end with the built binary, real stage files, a real git origin, and
  stub agents:
  - The prompt had GLOBAL-IMPLEMENT and then REPO-IMPLEMENT. The repo text came from the
    `.foreman/implement.md` committed on origin/main.
  - The global and repo `review.md` and `pr.md` were absent.
  - An uncommitted `.foreman/implement.md` in the checkout that `start` ran from was
    ignored.
  - A mode-000 global file set the task to FAILED with "stage instructions
    …/implement.md: permission denied".

### Stage 3 (complete): review instructions in the competing review

- Each review round resolves `review` for the worktree and adds it before the task
  context. The built-in review checklist then becomes the fallback, and the JSON format
  still applies. A read error fails the review before the reviewer runs (D51).
- Validated by tests for:
  - the review prompt carrying only `review`;
  - the lookup order (implement, then review) at the worktree root;
  - an unreadable `review.md` failing the task with no review run;
  - the section placement, the fallback line, and no section when there's no text.
- Validated end to end with the built binary, real stage files, a real git origin, and
  a stub Codex reviewer that records its prompt.
  - The review prompt had GLOBAL-REVIEW and then REPO-REVIEW (from the committed
    `.foreman/review.md`), plus the fallback line.
  - The implement and pr text were absent.
  - The implementation prompt had no review text.

### Stage 4 (complete): pr instructions for the PR description

- Fresh implementation prompts also resolve `pr` and add it as a "Pull request
  description" section next to the PR rules. "Closes #N" stays required, and the default
  body rule applies only when there's no `pr` text (D49, D52).
- Validated by tests for:
  - the implementation prompt carrying `implement` and `pr` but not `review`, and the
    review prompt carrying neither;
  - the lookups (implement, pr, review) at the worktree root;
  - an unreadable `pr.md` failing the task before the agent runs;
  - the section placement, "Closes #N" kept, and the default rule when there's no text.
- Validated end to end with the built binary, real global and committed repo `pr.md`
  files, and stub agents:
  - The implementer's prompt had GLOBAL-PR and then REPO-PR, and the PR rule pointed to
    the section.
  - The reviewer's prompt had neither, and only the reviewer had GLOBAL-REVIEW.

### Stage 5 (complete): CLI inspection

- `foreman config stages` shows each stage's sources, and `foreman config stage <stage>`
  shows the combined text as agents get it (D53). Stage 8 replaced both with
  `foreman config --stages` (D55).
- Validated by tests for parsing (both forms, an unknown stage, bad shapes),
  `RepoRoot` (from a subdirectory, and outside a repo), and rendering (found, not
  configured with its path, outside a repo, a read error, and empty content).
- Validated end to end with the built binary:
  - from a repo subfolder, all six sources found;
  - `config stage review` printing global then repo text;
  - outside a repo, "not in a git repository";
  - a mode-000 file showing an error and exit 1;
  - an unknown stage giving usage and exit 64;
  - the user's real config location listing everything as not configured, with the
    `~/.config/foreman/stages/…` paths.

The stage-instructions track is complete.

## Inbox and observability

These stages continue the main track after Stage 6.

### Stage 7 (complete): interactive inbox and storage setup

- `foreman inbox` is interactive by default. It shows repo-qualified identities,
  lets the user choose a current item, selects Claude or Codex, accepts an optional
  context Markdown path, and delegates through the existing start flow.
- `foreman inbox --non-interactive` preserves report output for scripts. Current
  refresh items remain available on later inbox runs even when their GitHub snapshot
  is unchanged.
- The first interactive inbox checks the configured tasks directory and offers to
  create it, explaining that the folder can be an Obsidian vault folder or a normal
  directory. Markdown remains the only storage backend.
- Validated with focused tests for command parsing, repo-qualified rendering,
  repeatable current items, interactive selection, and storage setup outcomes.
- Also validated with `go vet ./...`, `go test ./...`, `go build -o dist/foreman ./cmd/foreman`,
  and `git diff --check`.

### Stage 8 (complete): repository filters and configuration modes

- Added exact `ignore_repositories` config filtering for fresh and tracked inbox data.
- Replaced config subcommands with the mutually exclusive `--stages`, `--show`, and
  `--edit` flags. `--show` prints resolved non-secret values, and `--edit` uses
  `$VISUAL`, `$EDITOR`, or `vi`.
- Changed the unstamped version to `0.1.0-dev`; release stamping remains unchanged.
- Validated with focused config, CLI, app, and rendering tests. The full vet, test,
  build, and end-to-end checks ran in Stage 11, which also fixed `config --edit` for an
  editor with arguments (D61).

### Stage 9 (complete): checkout-independent inbox delegation

- Interactive inbox no longer passes the current working directory into delegation.
- A managed source checkout is cloned or reused under `repositories_dir`; agent
  worktrees continue to use `worktrees_dir`.
- Explicit `foreman start` retains its existing checkout matching behavior.
- Added regression coverage for selecting a repository different from the current
  checkout and for cloning a missing source checkout.
- Stages 7 to 9 were first run end to end in Stage 11, which found and fixed lost
  piped answers after first-run setup and PRs offered for delegation (D61).

### Stage 10 (complete): observability, cost controls, and notifications

This stage was first recorded as a second "Stage 7".


- Added timestamped runner logs and `foreman logs`, with clean, follow, and raw modes.
- Added durable per-task usage accounting. Claude spend is read from
  `total_cost_usd`; Codex input, cached, output, and reasoning tokens are read from
  `turn.completed` events.
- Added configurable Claude per-turn budgets of $3.00 for implementation and $1.00
  for review. The existing three-round cap and early clean-review exit remain active.
- Added best-effort native notifications for NEEDS_DECISION, FAILED, and
  READY_FOR_HUMAN transitions.
- Validated with `go test ./...`, `go vet ./...`, `go build`, and the version command.
  None of that ran a detached runner, and the new log writer killed every runner once
  its launcher exited. Stage 11 found and fixed it (D58).

### Stage 11 (complete): repository review fixes

A review of the repository against this file and the commits, fixed on the user's
request. Each fix is its own commit with a decision.

- The detached runner died as soon as `start`, `respond`, or `inbox` exited, because
  its log went through a pipe owned by the launcher. The runner now stamps its own
  log (D58).
- Every task write notifies on entering NEEDS_DECISION, FAILED, or READY_FOR_HUMAN,
  which adds escalation at the round cap and a reaped runner. A stopped runner also
  stops the tools its agent started (D59).
- `logs --follow` prints whole lines only, and Codex usage lines say USD is
  unavailable (D60).
- Inbox prompts share one stdin reader, the picker offers only issues, and
  `config --edit` accepts an editor with arguments (D61).
- A runner whose task file is removed stops instead of crashing (D62).
- Reviews record the PR head they covered. New commits after a clean review are
  reviewed before handover, and a restart after a crash mid-review goes straight to
  the review, unless that PR is no longer open or the implementer changed (D63, D67).
- Searches ask for 1000 results and warn when one hits the limit. PR mentions are
  listed, and lost reasons are reported when every search is complete (D64).
- A blocked turn can ask several questions, answered in one `respond` (D65).
- `scripts/e2e.sh` runs the built binary against a local origin, a stand-in `gh`,
  and stub agents. Its six scenarios cover the runner, notifications, logs, the
  inbox with setup and filters, re-review, and multi-question respond.
- Validated with `go vet ./...`, `go test ./...`, `go build`, `scripts/e2e.sh`, and
  `scripts/probe-agents.sh` (17 checks on Claude Code 2.1.281 and Codex 0.156.1).
  Each new test was also checked against the code before its fix, where it failed.

### Stage 12 (complete): inbox change visibility and build identity

- Waiting tasks retain their own section and show GitHub changes and CI transition
  notes beneath it. The interactive entry includes CI transition notes too (D68).
- Missing undelegated items are refreshed until their involvement reasons clear;
  delegated work remains tracked. Item/PR/CI lookup failures warn and retry without
  blocking the inbox. Missing items stay out of the delegation picker. DONE tasks
  and ignored repositories remain excluded (D70).
- An undelegated issue GitHub no longer has (404 or 410) is reported once and not
  checked again. `status` lists its tasks with lookup warnings underneath instead of
  failing (D71).
- Local version output includes the embedded commit and dirty flag. Release
  version stamps are preserved; absent Git metadata is explicit (D69).
- Validated with lifecycle regressions for failed lookups, recovery, reassignment,
  truncated searches, delegated gates, and both inbox views; the full Go race
  tests, vet, every end-to-end scenario, and development/release binary versions.
  Process-lifecycle end-to-end checks require permission to inspect processes.

### Stage 13 (complete): planning documents on the issue

- The first implementer turn writes an analysis, a PRD, a design, and an
  implementation plan, following the new required `plan` stage file. It changes no
  code. The outcome contract gains a `planned` status carrying the documents (D90).
- The runner posts each document on the issue as its own comment, records the comment
  ids and URLs in the task file, and resumes the planning session to implement. A
  re-plan edits the same comments. There is no approval gate: a planning question goes
  to NEEDS_DECISION, and otherwise the run continues to the PR.
- The reviewer's prompt carries the posted PRD. `foreman show` links the comments.
- Validated by unit tests for the contract, prompts, posting and re-posting, partial
  post failures, planning questions, the gh comment calls, and rendering; every
  end-to-end scenario with a planning stub; and `scripts/probe-agents.sh`, where
  Claude and Codex both return a planned outcome under the new schema.

### Stage 14 (complete): the interactive dashboard

- A bare `foreman` opens a three-pane dashboard: delegable inbox issues on the left,
  delegated work on the right, and the selected task's runner log along the bottom.
  `foreman tui` is the explicit form, and a pipe still gets the usage text (D93).
- `internal/tui` splits into a pure `Model` whose `Key` returns an `Action`, a pure
  `Frame` that returns exactly the lines on screen, and one `Run` that owns raw mode,
  the tickers, SIGWINCH, and the redraw. `golang.org/x/term` is the only new
  dependency (D94).
- Tab moves focus, arrows and j/k move the cursor, enter delegates an issue after
  asking for the agent and an optional context file, c cancels a running task after a
  y/n confirm, r refreshes, PgUp/PgDn scroll the log, End follows it again, and q or
  ctrl+c quits. Enter on a NEEDS_DECISION task names `foreman respond`.
- Task files are re-read every second and GitHub every 60 seconds, only while the
  dashboard is open (D95). `App.Tasks` is the new gate-free read; `App.Status` keeps
  the gate and both share `activeTasks`.
- The panes reuse `render.TaskRows` (shared with `status`), `app.Delegable` (shared
  with the inbox picker), and `logs.Tail` (the cleaning `foreman logs` does).
- Validated by unit tests for key decoding, every key binding and both prompts,
  selection surviving a reordering refresh, the frame at three sizes, truncation, the
  too-small message, each mode's footer, the log tail, the gate-free task list, and
  the shared status rows; by an e2e scenario for the pipe behavior; and by hand on a
  PTY, where the dashboard drew, quit on q, and restored the terminal.

## Known limitations

- The gate runs only when `inbox` or `status` runs, because the design rules out
  daemons and polling (D5). Notifications come from the runner and from those
  commands.
- `gh pr checks` handling is verified against gh's documented behavior and fixtures,
  not a live PR, because the user declined the live probe (D36).
- Validation results are self-reported by the agent (D32). CI is the independent
  check, and foreman gates on it.
- A search that returns 1000 results is flagged, and lost reasons aren't judged
  while one is truncated (D64).
- The release workflow has not run: the repo has no GitHub remote yet.

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
- D47. Stage instructions and their resolver. D48. Global layer next to the config
  file; repo layer from the worktree. D49. pr.md goes in the implementation turn.
  D50. Implement instructions are resolved per fresh turn, before the task context.
  D51. Review instructions go to every review round, and the built-in checklist becomes
  the fallback. D52. pr instructions sit with the PR rules, and "Closes #N" stays required.
  D53. `foreman config stages` and `foreman config stage <stage>`, later replaced by
  flags (D55).
- D46 (supersedes D38). CI belongs to the repo owners: foreman reads GitHub's checks and
  hands failures to the developer.
- D54. Interactive inbox and storage setup. D55 (supersedes D53). Repository filters
  and `config --stages|--show|--edit`. D56. Inbox delegation is checkout-independent.
- D57. Timestamped logs, bounded Claude spend, and notifications.
- D58. The runner timestamps its own log. D59. Notifications follow state transitions,
  and a stopped run stops its agent. D60. Follow logs by whole lines. D61. Shared
  stdin, issues-only picker, editor through the shell. D62. A removed task file stops
  the runner.
- D63. A review covers one PR head. D64 (amends D15). Complete inbox searches, PR
  mentions, and lost reasons. D65. A blocked turn can ask several questions.
- D66. What the repository review left as designed. D67 (amends D63). When picking up
  at the review falls back to implementing.
- D68 (amends D15 and D64). Waiting-task changes and missing undelegated items.
  D69. Local versions identify their embedded Git revision. D70 (amends D68).
  Bounded refreshes and isolated lookup failures. D71 (amends D70). Gone issues stop
  being checked, and status warns instead of failing.
- D72. Default stage instructions ship in the repo, installed by a Makefile. D73
  (amends D47, D49, D72). Agent working rules live in the required stage files.
  D74 (amends D73). The default PR body outline. D75 (amends D73). No agent
  attribution in commits or PRs. D76 (amends D73), superseded by
  D77 (amends D11, D35): `review_rounds` counts reviewer passes, `fix_rounds` caps
  the fix loop. D78 (amends D35, D40). Every fix is reviewed before the developer
  is asked. D79 (amends D73). Three implement rules dropped. D80
  (amends D79). The PR validation gate moves to pr.md. D81
  (amends D23). Default models are claude-opus-5 and gpt-6-sol.
  D82 (amends D23). Claude's tool list is fixed, not configured.
  D83. Round settings load with their defaults.
- D84. config --stages reports missing global files as required. D85 (amends
  D81). Per-agent review models. D86. The dollar budgets are labeled Claude-only.
- D87 (amends D57). Claude's stream-json events go to the runner log. D88.
  `show` and the runner log point to Claude's own session transcript.
- D89. `foreman cancel` stops a running task.
- D90 (amends D73). The implementer plans first and the runner posts the plan on the
  issue.
- D91. `config --edit` opens a config that fails to load. D92 (amends D73, D90). The
  prompts' remaining workflow rules moved to the stage files.
- D93. A bare `foreman` opens the dashboard. D94. The dashboard is a pure model and
  frame over `golang.org/x/term`. D95 (amends D5). The dashboard polls in the
  foreground, at two rates.

## Next stage

None. Stages 0 to 14 are complete. Further work needs a new request.
