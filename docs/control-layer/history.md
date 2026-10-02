# Control Layer History

The record of every completed stage, moved out of progress.md (D144). Add a stage's
entry here when it completes. Nothing reads this file by default.

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
  ids and URLs in the task file, and starts a fresh session to implement (D117). A
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
- Tab cycles through the inbox, task list, and log. Arrows and j/k move the focused
  list cursor or scroll the log by line. Enter delegates an issue after asking for the
  agent and an optional context file; c cancels a running task after a y/n confirm;
  r refreshes; q or ctrl+c quits. Enter on a NEEDS_DECISION task names
  `foreman respond`. Log paging keys are described in D105.
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
- Review round 1 found seven defects, all fixed (D96): the terminal check now runs
  before the config is read, a split escape sequence waits for its rest, quitting waits
  for a Start or Cancel in flight, a typed context file is made absolute, the log
  follows the cursor at once, the task pane wraps its head at 60 columns, and the
  NEEDS_DECISION message leads with the command.
- A follow-up review found three more dashboard defects, fixed in PR #2 (D97 to D99):
  cancel confirmation keeps its original task across refreshes, dynamic text cannot
  issue terminal controls, and the selected PR with its badges fits the 60 by 12
  minimum layout. Focused regressions cover each case. Validated with `go vet ./...`,
  `go test ./...`, race tests for `internal/tui`, `internal/app`, and `internal/render`,
  a binary build, and the full `scripts/e2e.sh` run.
- Review round 2 closed the two remaining findings and a gap the round's own PR wrap
  opened (D100 to D102): a log paged back with PgUp holds its lines as the agent
  writes, `truncate`, `pad`, and `wrap` count terminal columns so a CJK title stays
  inside its pane, and the wrapped PR row is bounded by `taskRowLines` so a waiting
  question still fits the minimum layout.
- Review round 3 found one defect, fixed (D103): the one PR line a waiting task has at
  60 by 12 was truncated past the PR number, hiding the CI and review badges R3 asks
  for. The row now drops the reviewer and then the failing checks' names, and it grows
  back to the row `status` prints where the pane is tall enough.
- The PR author's follow-up asked for color and dividers, and for the log to follow the
  most recently delegated issue with cancellation from that view (D104). `Start` now
  records the delegation time, the default cursor follows the newest start until a task
  is selected manually, and `c` targets the task shown in the log from either pane.
  The frame colors status rows and messages and draws a vertical divider.
- The follow-up on log accessibility is complete (D105). Tab reaches the log as a
  third pane. In that pane, j/k and arrows scroll by line, u/d by page, ctrl-u/d by
  half page, and g/G jump to the oldest available line or the live tail. The footer
  names the keys for the focused pane.

### Stage 15 (complete): dashboard views for issue #3

- The user's clarification replaces the always-visible log and third Tab stop (D106).
  The dashboard starts with INBOX and DELEGATED WORK. `h`/`l` switch between them,
  and Enter on a delegated task opens its log in a smaller area below. Escape closes
  it; moving to another task closes it until that task is opened. The selected log is
  read only while open. `u`/`d`, Ctrl+U/Ctrl+D, and `g`/`G` scroll the open log while
  `j`/`k` keep navigating delegated work.
- The task list displays its branch when there is room, and colored dividers separate
  the lists and the optional log. Status color still marks delegated task rows.
- Verified with `go vet ./...`, `go test ./...` (including the runner test with normal
  process access), `go build -o dist/foreman ./cmd/foreman`, `scripts/e2e.sh`, and
  `git diff --check`. Focused TUI tests cover hidden and opened logs, Escape, task
  changes, navigation, narrow layout, branch display, and color dividers.

### Stage 16 (complete): navigation, visibility, and settings for issue #5

- Left and Right select the two list panes beside `tab`, `h`, and `l` (D109).
- Row text keeps the terminal's foreground, and only a task's state is colored. The
  selected row is reverse video alone, which reads on a light theme and a dark one
  (D109).
- A PR closed without merging stops its runner, moves the task to the new `PR_CLOSED`
  state, and drops it from DELEGATED WORK without a failure or a notification (D107).
  Reopening the PR brings the task back, as READY_FOR_HUMAN or REVIEWING depending on
  the review it has and the PR's current CI (D108). Local rereads defer reaping a dead
  runner with a PR until the GitHub gate checks it. Reopening waits for a retained
  runner to stop or clears its stale pid (D111). A dead runner is reaped only once the
  gate has read its PR, so a failed lookup delays the verdict instead of guessing it
  (D113).
- `mentions` hides items found only because they mention the developer; `refresh` sets
  the dashboard's GitHub interval, 60 seconds by default and 5 at the least (D110).
  The note for an item that left GitHub follows the setting too, judged by the reasons
  the task stored before they were cleared (D114). An item a truncated search missed
  arrives as a tracked fetch, and is judged by the involvement the task stored before
  the refresh rewrote it (D115).
- Verified with `go vet ./...`, `go test ./...`, `go build -o dist/foreman
  ./cmd/foreman`, `scripts/e2e.sh`, and `git diff --check`. New tests cover the arrow
  keys, the row and selection colors, the closed and reopened PR paths including a
  running and an unstoppable runner, the mention filter, and the two settings.
- The D111 and D113 to D115 review fixes passed `go vet ./...`, `go test ./...`, `go test
  -race` over `internal/app`, `internal/render`, `internal/tui`, and `internal/config`,
  the build, `scripts/e2e.sh`, and `git diff --check`. The closed and reopened PR paths
  have no end-to-end check; `gate_test.go` covers them, including a dead runner, a stop
  that will not take, and a PR lookup that fails. `inbox_gone_test.go` covers the gone
  note against the mentions setting, and `inbox_test.go` covers a tracked fetch under a
  truncated search. The D113, D114, and D115 tests were confirmed to fail without their
  fixes.

### Fix (complete): Codex commits in a linked worktree

- midsoln/foreman#5 failed because Codex couldn't write the clone's `.git` from its
  workspace-write sandbox, so it couldn't commit (D112). The Codex adapter now adds
  that directory as a writable root on `exec` and `exec resume`.
- Verified with `go vet ./...`, `go test ./...` (a new test uses a real linked worktree),
  `scripts/e2e.sh`, and `scripts/probe-agents.sh` (24 checks on Codex 0.156.1). The
  probe shows the commit failing without the root and succeeding with it.

### Merge resolution (complete): PR #6 with main

- Preserved the issue #5 changes and the Codex worktree fix. Both branches had used
  D112, so D112 remains the worktree decision and the later issue #5 review decisions
  are D113 to D115 (D116).
- Verified the combined tree with `go vet ./...`, `go test ./...`, race tests for
  `internal/app`, `internal/tui`, `internal/config`, and `internal/agent`, a build, and
  `scripts/e2e.sh`.

### Stage 17 (complete): lower implementer token use

- The plan-to-implementation handoff starts a fresh session with the posted plan
  links and the current task context. Planning decisions still resume the planning
  session, while later findings resume the new implementation session (D117).
- The plan and implement stage defaults use cohesive milestones and focused checks
  during editing. The local config uses Sonnet and a $25 implementer cap for a trial.
- `go vet ./...`, `go test ./...`, the binary build, and every `scripts/e2e.sh`
  scenario passed. `foreman config --show` confirmed the live model and budget.

### Stage 18 (complete): models per agent turn

- Planning, implementation, and review each select a model. Codex also selects
  reasoning effort for each turn. Planning answers keep the planning setting; fixes
  keep the implementation setting. PR creation stays in implementation (D118).
- The local config uses Claude `sonnet` and Codex `gpt-6-luna` at `xhigh` for
  planning, then Claude `opus` and Codex `gpt-6-sol` at `medium` for implementation
  and review. The global PR instructions ask for Basic English.
- `config --edit` materialized the new fields in a new file and loaded the live
  settings. `config --show` displayed all nine stage values. `go vet ./...`,
  `go test ./...`, the binary build, and every `scripts/e2e.sh` scenario passed.

### Stage 19 (complete): PR feedback, the INBOX filter, and Codex logs for issue #7

- A new or text-edited comment on an open foreman PR moves a READY_FOR_HUMAN task to
  the new `REVIEWED` state (D119). The gate reads the conversation, the inline comments
  and replies, and the reviews on every refresh (D121). Replies that start with
  `<!-- foreman:reply -->` are dropped, so the agent's answers start no new round (D120).
- `foreman resume <issue>`, the picker, and enter on the dashboard resume the task's own
  session in its own worktree with each unanswered comment and where to reply (D121).
  A turn that leaves the PR head unchanged returns to READY_FOR_HUMAN with no new review;
  a turn that pushes goes through review and CI again. Comments count as answered only
  after a `pr_opened` turn (D122).
- The runner reads the conversation again before it hands over and answers what arrived
  during the run, at most three times per run (D123).
- INBOX lists never delegated issues plus delegated FAILED and REVIEWED ones (D124).
- The cleaned log renders Codex messages, commands and their results, file changes,
  searches, MCP calls, errors, and turn totals. `--raw` still prints the JSON (D125).
- `show` lists each PR comment foreman tracks and whether it is answered.
- Review round 1 fixes (D126): the `mentions` filter keeps delegated FAILED and
  REVIEWED tasks selectable, and a restart with another agent keeps the PR while
  comments wait, so the new agent answers them.
- Review round 2 fix (D127): a resume that finds no comment to answer, or cannot read
  them, ends without an agent turn when the clean review covers the PR head.
- Verified with `go vet ./...`, `go test ./...`, `go build -o dist/foreman
  ./cmd/foreman`, `scripts/e2e.sh` (a new `feedback` scenario covers comment, REVIEWED,
  resume, reply, and no second round), and `git diff --check`.

### Merge resolution (complete): PR #10 with main

- Kept main's fresh implementation session and per-stage model settings alongside
  issue #7's PR feedback flow. Issue #7's decision IDs are D119 to D127, after
  main's D117 and D118 (D128).
- Verified the combined tree with `go test ./...`, `go vet ./...`, a binary build,
  and all `scripts/e2e.sh` scenarios with normal process access.

### Stage 20 (complete): concise planning comments and PR descriptions for issue #13

- `defaults/stages/plan.md` asks each planning document to open with a short summary
  and to cut repeated background. `pr.md` caps the body at about 25 lines and links the
  planning comments. `implement.md` rule 20 asks writing to lead with the result (D129).
- Installed stage files, existing comments, and existing PRs do not change.
- Verified with `go vet ./...`, `go test ./...`, a binary build, and `scripts/e2e.sh`.

### Stage 21 (complete): merge conflict repair on handed-over PRs for issue #17

- The gate reads `mergeable` for each open PR. A READY_FOR_HUMAN PR that GitHub reports
  as CONFLICTING goes back to its implementer once per PR head and base head (D130).
- The runner resumes the implementer to merge the base, checks with git that the new
  head includes the base head and that every reported check passed, and reviews it. A
  failed repair is NEEDS_DECISION, including a runner that cannot start or dies
  (D132). Every attempted pair stays spent (D131).
- Verified with `go vet ./...`, `go test ./...`, a binary build, and `scripts/e2e.sh`,
  which has a new `conflict` scenario.

### Stage 22 (complete): merge conflicts before each review for issue #12

- Before every review pass the runner checks the PR against its latest base with
  `git merge-tree` (D133). A clean merge, even after the base moved, changes nothing.
- A conflict gets one implementer turn that merges the base into the task branch and
  pushes. The new head is reviewed. The turn uses no fix round.
- A blocked merge turn, or two turns that still leave conflicts, ends in
  NEEDS_DECISION with the files. `foreman respond` resumes the implementer, which
  finishes the merge, and the run checks again before its review.
- Review round 1 fixes (D134): a retargeted PR is checked and reviewed against its
  new base, and a blocked merge question always names the conflicting files.
- Review round 2 fix (D135): the conflict list is always appended, since a substring
  search can mistake `dir/a.go` for `a.go`.
- Verified with `go vet ./...`, `go test ./...`, `go build -o dist/foreman
  ./cmd/foreman`, and `scripts/e2e.sh` (a new `conflicts` scenario covers a
  conflicting base and a clean base update).
- Merged main's conflict repair for issue #17 (D136). Issue #12's decisions are
  D133 to D135. Verified with `go vet ./...`, `go test ./...`, a binary build, and
  all `scripts/e2e.sh` scenarios, including both `conflicts` and `conflict`.

### Stage 23 (complete): the dashboard show view for issue #11

- With the lower pane open, `s` switches between the log and the `foreman show` text
  for the selected task, with either list focused (D137). The heading reads
  `SHOW · repo#N · state`, plus the line range when the text does not fit.
- Show lines wrap at the terminal width. The log scroll keys scroll the show view, which
  opens at its top. Returning to the log follows its tail.
- Verified with `go vet ./...`, `go test ./...`, `go build -o dist/foreman
  ./cmd/foreman`, and `scripts/e2e.sh`.

### Stage 24 (complete): test-first, readable behavior tests for issue #19

- Every implementation prompt has a built-in `## Tests` section: test first, see it
  fail, implement, rerun, commit only passing tests, and name tests by behavior. The
  planning prompt asks the plan to name each test's behavior. Conflict repair and
  merge-before-review turns get neither (D138).
- Verified with `go vet ./...`, `go test ./...`, a binary build, and `scripts/e2e.sh`.

### Stage 25 (complete): a shorter default PR description for issue #21

- `defaults/stages/pr.md` asks for `## Problem`, `## Fix`, and `## Risk`, then
  `Closes #N`. `## Review notes` appears only for a decision, plan deviation, or
  limitation a reviewer needs (D139).
- The body drops the test report, reviewer test steps, and planning comment links. The
  validation gate before a PR opens stays.
- Installed stage files and existing PRs do not change.
- Verified with `go vet ./...`, `go test ./...`, and a binary build.

### Stage 26 (complete): adversarial reviews for issue #22

- The reviewer gets the PR description as its only requirements, plus the PR comments
  as non-authoritative history. It no longer sees the issue, PRD, context, decisions,
  or the implementer's validation. A blank or unreadable description stops the review.
  The prompt asks for counterexamples and verified defects, and a re-review lists the
  earlier findings in full (D140).
- Verified with `go vet ./...`, `go test ./...`, a binary build, and `scripts/e2e.sh`.

### Stage 27 (complete): the model and step for each agent turn, issue #25

- Each plan, implement, review, feedback, and merge turn logs `agent:model:step`, for
  example `codex:gpt-6-sol:review`. The model is the one the adapter passes to the CLI,
  or `default` when none is set. Resumed and repeated turns each log their own line
  (D141).
- Verified with `go vet ./...`, `go test ./...`, a binary build, and `scripts/e2e.sh`.

### Stage 28 (complete): the task runner as named steps for issue #26

- `RunTask` runs a loop of named steps (`stepImplement`, `stepAnswer`,
  `stepPickUpReview`, `stepReview`, `stepHandover`) on a `taskRun`. `firstStep` picks
  the first step from the record and the waiting comments.
- `runTurn` and `finishTurn` in `internal/app/turn.go` replace four copies of the
  turn, verify, and record sequence. `implement` takes a `turnRequest`. `failTask`
  replaces six hand-written FAILED transitions.
- No change to CLI output, exit codes, the task file, prompts, or log text (D142).
- New tests cover a merge turn whose PR can't be verified, a cancelled feedback turn,
  a deleted plan comment, a failed plan post, `failTask`, and `firstStep`.
- Verified with `gofmt -l .`, `go vet ./...`, `go test ./...`, a binary build, and
  `scripts/e2e.sh`.

### Stage 29 (complete): Discord webhook alerts for issue #32

- `notifications.Discord` posts each alert to `notifications.webhook_url`, replacing
  `osascript` and `notify-send` (D143). The triggers are unchanged.
- `Notifier.Notify` takes an `app.Alert` with the state, task key, and PR or issue link.
- Foreman warns on stderr when notifications are enabled with no URL, and when the
  config file holding the URL is readable by other users. `config --show` prints only
  whether the URL is set.
- Delivery errors go to `<log_dir>/notifications.log` without the URL and never change
  a task.
- New tests cover the post body, the link choice, mentions, non-2xx and network
  failures, the config warnings, and `config --show`. The e2e notify scenario uses a
  stand-in webhook for delivery, a 500 response, the permission warning, and a missing
  URL.

