# Decision log

This file records every decision point, in order, whether the user or an agent made it.
Add an entry whenever a choice shapes behavior, architecture, scope, or tooling. Never
rewrite an old entry. If a decision is reversed, mark the old entry
`Superseded by Dn` and add a new one.

Entry format:

```
## Dn. Title
- Date / Stage / Decided by (user | agent)
- Context: why a choice was needed
- Options: what was considered
- Decision: what was chosen
- Consequence: what it changes
```

## D1. Implementation language: Go

- Date: 2026-09-24. Stage 0. Decided by the user, following the agent's recommendation.
- **Context.** The design doc defaulted to TypeScript/Bun unless there was a concrete
  reason not to. The tool will be packaged and distributed as an executable.
- **Options.** A. TypeScript + Bun (`bun build --compile`). B. Go.
- **Decision.** B.
- **Consequence.** The stripped Go binary is 1.8 MB, where the Bun compiled binary was
  59 MB. os/exec gives solid control over the detached runner process, and the YAML v3
  Node API keeps comments and key order in files the user edits. What's lost is
  TypeScript's union types, so outcomes are validated by `agent.ParseOutcome` and the
  strict JSON schema.

## D2. Go toolchain upgraded to 1.27.1 through Homebrew

- Date: 2026-09-24. Stage 0. Decided by the user ("update Go"). The agent chose the
  install method.
- **Context.** Go 1.23.0 was installed at `/usr/local/go`. Replacing that install needs sudo.
- **Options.** A. Homebrew `go`, which comes earlier in PATH. B. The official pkg
  installer, which needs sudo. C. `GOTOOLCHAIN` auto-download.
- **Decision.** A.
- **Consequence.** `/opt/homebrew/bin/go` is 1.27.1 and `go.mod` says `go 1.27.1`. The
  old `/usr/local/go` is still installed but no longer used.

## D3. Binary name: `foreman`

- Date: 2026-09-24. Stage 0. Decided by the user.
- **Context.** The design doc wrote its examples as `dev inbox` and so on.
- **Options.** A. Ship the binary as `dev`. B. Ship it as `foreman`.
- **Decision.** B. The binary is `foreman`, and no `dev` alias is shipped or documented.
- **Consequence.** Usage text and docs use `foreman <command>`.

## D4. Agent execution: headless turns with a structured outcome

- Date: 2026-09-24. Stage 0. Decided by the agent.
- **Context.** The tool has to tell when an agent is blocked on a decision, without
  scraping the terminal.
- **Options.** A. Interactive sessions with terminal or PTY scraping. B. Headless turns
  (`claude -p --json-schema`, `codex exec --output-schema`) whose final message follows
  a schema.
- **Decision.** B. Every turn ends as `pr_opened`, `blocked` (with a question), or `failed`.
- **Consequence.** A blocked agent ends its turn instead of waiting for input.
  `respond` resumes the same session by ID. Verified for both CLIs by
  `scripts/probe-agents.sh`.

## D5. One detached runner process per task, no daemon

- Date: 2026-09-24. Stage 0. Decided by the agent.
- **Context.** Agent turns take minutes to hours. The design doc rules out daemons and
  polling.
- **Options.** A. Run the agent in the foreground of `start`. B. Have `start` and
  `respond` spawn a detached `foreman` runner for that one task, which exits when the
  task needs the human or finishes. C. A daemon.
- **Decision.** B.
- **Consequence.** The runner's pid and log path go in the task file. `inbox` and
  `status` check liveness and CI when the user runs them.

## D6. Who pushes and opens the PR: foreman (superseded by D7)

- Date: 2026-09-24. Stage 0. Decided by the agent.
- **Context.** It hadn't been verified whether Codex's sandbox could reach GitHub
  credentials.
- **Decision.** Foreman would run `git push` and `gh pr create` after the agent returned
  `ready_for_pr`.
- **Status.** Superseded by D7.

## D7. The implementation agent pushes and opens the PR

- Date: 2026-09-24. Stage 0. Decided by the user.
- **Context.** The design doc makes the implementation agent responsible for
  `git push` / `gh pr create`. The user reversed D6.
- **Options.** A. Foreman pushes and opens the PR (D6). B. The agent does both.
- **Decision.** B.
- **Consequence.** The outcome status `ready_for_pr` with a PR title and body is
  replaced by `pr_opened` with `prNumber` and `prUrl`. Foreman checks with `gh` that the
  PR exists before moving to REVIEWING. Codex runs with
  `sandbox_workspace_write.network_access=true`. The probe confirmed that `gh api user`
  and an authenticated `git ls-remote` work inside the Codex sandbox. `Push` and
  `CreatePullRequest` were removed from the ports.

## D8. Agent permissions

- Date: 2026-09-24. Stage 0. Decided by the agent.
- **Context.** Unattended Claude denies shell commands that aren't pre-approved, and the
  `auto` permission mode denied them too. Codex `workspace-write` has no network.
- **Options.** A. `bypassPermissions` / `--dangerously-bypass-*`. B. Claude with
  `acceptEdits` and a configured `--allowedTools` list, and Codex with `workspace-write`
  plus network access.
- **Decision.** B. The dangerous flags are never the default.
- **Consequence.** The default Claude allowlist has to cover git, gh, and the repo's
  test and build commands. Denials appear in `permission_denials` and are treated as
  failures.

## D9. Storage: one Markdown file per repo and number

- Date: 2026-09-24. Stage 0. Decided by the agent, within the design doc's constraints.
- **Context.** The design doc asks for human-editable Markdown with YAML frontmatter and
  no SQLite.
- **Decision.** Files are named `<owner>-<repo>-<number>.md` in a configurable
  directory. Frontmatter holds machine state. The body is round-tripped byte for byte.
  Items that are seen but not delegated are saved as `DISCOVERED`, so the inbox
  remembers what the user has already seen.
- **Consequence.** No lock file is needed. The runner writes only its own task's file.
  Superseded by D21: the runner and inbox can both write one file, so writes are locked.

## D10. Reviewers get disposable sessions built from durable state

- Date: 2026-09-24. Stage 0. Decided by the agent.
- **Decision.** The reviewer runs with `--no-session-persistence` / `--ephemeral`. Its
  input is the issue, the context file, and the diff against the base. It never receives
  the implementer's conversation. It uses the same `codex exec` / `claude -p` path with a
  review schema, not `codex exec review`, so both reviewers work the same way.

## D11. The review loop is capped

- Date: 2026-09-24. Stage 0. Decided by the agent.
- **Decision.** The default is 3 fix and re-review rounds. After that the task becomes
  NEEDS_DECISION with the outstanding findings.
- **Consequence.** The loop can't run forever when the two agents keep disagreeing.

## D12. Branch and worktree naming

- Date: 2026-09-24. Stage 0. Decided by the user (related to the issue, a standard
  slugged format, a word cap). The agent chose the exact rules.
- **Context.** Claude, Codex, and the user all need one predictable name for a task's
  branch and checkout.
- **Decision.** `WorkName = <issue>-<slug>`. The slug is the issue title lowercased, with
  any leading conventional-commit prefix (`feat(x): `) removed, common accents folded to
  ASCII, and non-alphanumeric runs turned into `-`. It keeps at most 5 words and at most
  40 characters, cut on a word boundary. A title that produces nothing becomes `issue`.
  The branch is `agent/<WorkName>` (prefix superseded by D45). The worktree is
  `<worktrees root>/<owner>-<repo>/<WorkName>`.
- **Example.** Issue 825, "Provider retries", becomes branch
  `agent/825-provider-retries` in `…/ridezoomo-armada-backend/825-provider-retries`.
- **Consequence.** The name is computed once at `start` and stored in the task file.
  Editing the title later doesn't rename anything. The code is in `internal/task/naming.go`.

## D13. YAML library and frontmatter merge

- Date: 2026-09-24. Stage 1. Decided by the agent.
- **Context.** Task files must stay editable by hand and in Obsidian. The Go standard
  library has no YAML package.
- **Options.** A. `gopkg.in/yaml.v3`, which is archived. B. `go.yaml.in/yaml/v3`, the
  maintained successor from the YAML org. C. A hand-rolled parser.
- **Decision.** B, and it's the only runtime dependency. On save, foreman decodes the
  existing frontmatter into a `yaml.Node` and replaces only the keys it owns (the tags on
  `store.frontmatter`). Keys the user added (`tags`, `aliases`, and anything else) keep
  their position and comments, and comments on owned keys are kept too. The body is
  written back exactly as it was.
- **Consequence.** A new owned key that the user already uses would collide, so owned
  keys are all snake_case task terms. Saves are atomic (temp file + rename).

## D14. Tasks directory location

- Date: 2026-09-24. Stage 1. Decided by the agent.
- **Context.** The vault location is still unknown, and the design doc says it can be
  configured later.
- **Decision.** In order of precedence: `FOREMAN_TASKS_DIR`, then `tasks_dir` in
  `~/.config/foreman/config.yaml` (with `~` expansion), then the default
  `~/.local/share/foreman/tasks`. Markdown files without task frontmatter are ignored,
  so the directory can live inside a vault.

## D15. Inbox semantics (amended by D64)

- Date: 2026-09-24. Stage 1. Decided by the agent.
- **Context.** The inbox has to answer "what changed since I last looked", and that
  needs a definition.
- **Decision.**
  - The sources are three `gh search` queries, all limited to open items with at most
    100 results each: issues assigned to me, issues mentioning me, and PRs requesting my
    review. Delegated tasks that aren't DONE and that no search returned are also
    re-fetched through `gh api repos/{repo}/issues/{n}`, which covers both issues and PRs
    and detects merges.
  - An item is **new** when it has no task file. It is **changed** when its state
    differs (closed, merged, or reopened), when it has gained a reason (for example it
    is now assigned to me), or, if neither, when its `updatedAt` has moved forward. A
    reason being lost doesn't count as a change.
  - Each item appears in one section only, in priority order: NEEDS DECISION, then
    READY FOR YOU, then CHANGED, then NEW. Unchanged items only add to the hidden count.
  - Running `inbox` marks what it showed as seen. Only new or changed records are
    written, so repeat runs don't touch unchanged files. That means `last_github_sync`
    is the last time foreman recorded a change for that item.
  - A DISCOVERED item that is no longer open becomes DONE. Inbox never changes the state
    of a delegated task. That belongs to Stage 6 and later stages.
- **Consequence.** The first run lists everything as NEW (41 items on the user's
  account), and later runs show only differences.

## D16. Issue references across repos

- Date: 2026-09-24. Stage 1. Decided by the agent.
- **Context.** `show <issue>` takes only a number, but the same number can exist in
  several repos. On the user's account, #49 exists in both billing-bridge and
  sw-automations.
- **Decision.** `<issue>` accepts `825`, `#825`, or `owner/repo#825`. A bare number that
  matches more than one tracked repo is an error that lists the qualified choices.
- **Consequence.** Stage 2's `start` uses the same parser. For `start`, a bare number
  will resolve against the current checkout's repo.

## D17. Ports grow per stage

- Date: 2026-09-24. Stage 1. Decided by the agent.
- **Context.** Go interfaces are satisfied implicitly, so a large port definition forces
  stub methods that do nothing.
- **Decision.** `GitHubClient` currently has only `InboxItems` and `GetItem`. Issue
  fetch arrives in Stage 2, and PR and CI status in Stage 6, each added along with the
  stage that uses it.

## D18. Stage 2 carries the full execution contract and outcome handling

- Date: 2026-09-24. Stage 2. Decided by the agent.
- **Context.** The design doc spreads the execution contract across Stages 2 to 5. But
  the agent owns push and PR creation (D7), and every turn ends in `pr_opened`,
  `blocked`, or `failed` (D4). A Stage 2 runner has to leave the task in a truthful
  state whichever outcome comes back.
- **Options.** A. A reduced Stage 2 prompt with an extra "implemented" status that
  would be removed later. B. The full prompt now, plus a single mapping from outcome to
  state.
- **Decision.** B. `pr_opened` becomes REVIEWING and records the PR number. `blocked`
  becomes NEEDS_DECISION and records the question. `failed`, or any runner error, becomes
  FAILED. The prompt already requires `recommendation` to be an option label.
- **Consequence.** Later stages add only the parts that are still missing: Stage 3 adds
  `respond` and resume, Stage 4 checks the PR with `gh`, and Stage 5 adds the review that
  follows REVIEWING. Until Stage 5, a REVIEWING task has no reviewer running.

## D19. How `start` finds the repo, branch, and base

- Date: 2026-09-24. Stage 2. Decided by the agent.
- **Decision.** `start` must run from a checkout of the issue's repository.
  `gh repo view` in that directory gives the repo and its default branch. An
  `owner/repo#N` argument must match it. The base is `origin/<default>`, fetched before a
  new branch is created. When the task is restarted, the stored branch is reused, so
  editing the title doesn't create a second branch.
- **Consequence.** No map from repos to local paths is needed. Starting from outside a
  checkout gives a clear error.

## D20. Which states `start` accepts

- Date: 2026-09-24. Stage 2. Decided by the agent.
- **Decision.** `start` accepts a task that is new, DISCOVERED, or FAILED, and a WORKING
  or REVIEWING task whose runner has died. It refuses a task whose runner is alive, a
  NEEDS_DECISION task (it points to `respond`), and READY_FOR_HUMAN and DONE tasks. A
  restart can switch to the other agent.

## D21. Writes to one task are serialized

- Date: 2026-09-24. Stage 2. Decided by the agent.
- **Context.** The runner and `inbox` can write the same task file at the same moment,
  and D9's claim of no shared writers stopped being true once the runner existed.
- **Decision.** `TaskStore.Update` takes an exclusive flock on `<tasks_dir>/.foreman.lock`,
  re-reads the file, applies the change, and saves atomically. `inbox` merges in only the
  fields that come from GitHub (title, URL, kind, snapshot, sync time), so it can't undo
  a state the runner has just recorded.

## D22. The runner process

- Date: 2026-09-24. Stage 2. Decided by the agent.
- **Decision.** `start` spawns `foreman __run owner/repo#N` (a hidden command) with
  `Setsid`, so closing the terminal doesn't kill it. Its stdout and stderr are appended to
  `<log_dir>/<owner>-<repo>-<n>.log`. The runner records its own pid and clears it after
  writing the outcome. A runner counts as alive when `kill(pid, 0)` succeeds and `ps`
  shows ` __run ` in its command line, which guards against pid reuse. `status` and
  `inbox` mark a WORKING task whose runner is gone as FAILED and point to the log.

## D23. Agent CLI defaults

- Date: 2026-09-24. Stage 2. Decided by the agent. This refines D8.
- **Decision.** Claude runs with `--permission-mode acceptEdits` and
  `--allowedTools=Bash,WebFetch,WebSearch`. Codex runs with `-s workspace-write` and
  network access enabled. The command, model, and Claude's tool list can all be set in
  the config file.
- **Consequence.** Claude's `Bash` access is unrestricted inside the worktree, which is
  the same trust as running Claude yourself with Bash allowed. It is not a sandbox. Use
  `claude.allowed_tools` to narrow it.

## D24. Handling the context file and session ID

- Date: 2026-09-24. Stage 2. Decided by the agent.
- **Decision.** The context file is stored as an absolute path and read when the runner
  runs. It is not copied into the task file. The session ID is recorded as soon as it's
  known: Claude's is assigned by foreman with `--session-id`, and Codex's comes from the
  `thread.started` event. That way a crashed turn can still be resumed in Stage 3.

## D25. `FOREMAN_CONFIG`

- Date: 2026-09-24. Stage 2. Decided by the agent.
- **Decision.** `FOREMAN_CONFIG` points foreman at another config file. The end-to-end
  checks use it to run with separate directories and stub agents without touching the
  user's config.

## D26. How `respond` reads and records an answer

- Date: 2026-09-24. Stage 3. Decided by the agent.
- **Decision.** `respond <issue>` prints the question in the `BLOCKED: Need decision`
  layout and reads one line from stdin. An empty line picks the recommended option. A
  bare option label (case-insensitive) is expanded to `"<label>. <description>"`. Any
  other text is recorded exactly as typed. If the recommendation doesn't name an option,
  an empty line is rejected.
- **Consequence.** Answers can be piped in (`echo B | foreman respond 825`). On an
  empty or closed stdin, the recommended option is chosen.

## D27. Where a decision is stored

- Date: 2026-09-24. Stage 3. Decided by the agent.
- **Context.** The design doc wants decisions saved in the task Markdown, and its example
  shows them in a `## Decisions` body section. The body belongs to the user (D13).
- **Decision.** The structured record (question, answer, `asked_at`, `answered_at`,
  `delivered_at`) lives in the `decisions` frontmatter key. When an answer is recorded,
  foreman also adds one readable line to the body's `## Decisions` section, creating the
  section if it's missing. That is the only body edit foreman makes. It only adds, and
  never rewrites existing text.

## D28. Resume first, rebuild from the task file only if the session is gone

- Date: 2026-09-24. Stage 3. Decided by the agent.
- **Context.** The design doc says to use native resume where it's reliable and
  otherwise rebuild the context from durable state.
- **Decision.** When a task has an answered decision that hasn't been delivered, the
  runner resumes the recorded session with the answer (`claude --resume`,
  `codex exec resume`). It falls back to a fresh turn only on `agent.ErrNoSession`, which
  means the CLI printed no result or started no thread. That's what both CLIs do for an
  unknown session ID, which was verified. The fresh prompt lists every answered decision
  under "Decisions already made". Any other resume error fails the task, so a second full
  run isn't spent after a real failure. A decision is marked `delivered_at` once a turn
  that received it finishes.
- **Consequence.** Verified live: a corrupted session ID led to a fresh session that
  reported the recorded answer.

## D29. Questions are validated at the boundary

- Date: 2026-09-24. Stage 3. Decided by the agent.
- **Decision.** `ParseOutcome` rejects a blocked outcome whose question has no issue,
  fewer than two options, or empty or duplicate labels. The task then becomes FAILED
  with the reason. `Question.Recommended` accepts "A", "A.", "A: …", and "Option A – …",
  so wordy recommendations still resolve to an option.

## D30. Real push and PR creation are assumed, not exercised

- Date: 2026-09-24. Stage 4. Decided by the user ("Assume it can push and open a real
  PR using `gh` cli").
- **Context.** Proving that an agent pushes and opens a PR needs a repo where a
  throwaway PR is acceptable.
- **Decision.** No real push or PR is created during validation. The probe already
  showed that `gh` and authenticated git work inside Codex's sandbox (D7). Stage 4 is
  validated by checking what the agent reports against real GitHub PRs, read-only.

## D31. How a PR report is checked

- Date: 2026-09-24. Stage 4. Decided by the agent.
- **Decision.** A `pr_opened` outcome only moves the task to REVIEWING after `gh`
  confirms the PR is open, in the task's repo (not a fork), from the task branch, and
  targeting the task's base branch. If the agent leaves `prNumber` null, foreman finds
  the open PR by branch (`gh pr list --head`). Any failed check sets the task to FAILED
  with the reason. The verified number and URL are what get recorded.
- **Consequence.** A guessed or wrong PR number can't get into the task file, and an
  agent that forgets the number doesn't need a second turn.

## D32. Validation is recorded as the agent reported it

- Date: 2026-09-24. Stage 4. Decided by the agent.
- **Decision.** The `validation` list from `pr_opened` (command and passed) goes in the
  `validation` frontmatter key and is shown by `show`. Foreman doesn't re-run it, because
  the commands vary by repo and only the agent knows them. GitHub CI (Stage 6) is the
  independent check. The prompt now says to open a PR only when validation passes, to
  commit everything, and to take the number and URL from `gh pr view` instead of
  guessing.

## D33. The review contract and reviewer isolation

- Date: 2026-09-24. Stage 5. Decided by the agent.
- **Decision.** The reviewer returns `internal/agent/review.schema.json`: a verdict of
  `clean` or `changes_requested`, a summary, and findings, each with severity
  (`blocking` or `should_fix`), file, line, summary, and detail. `clean` must have no
  findings, and `changes_requested` must have at least one. The reviewer is read-only
  and disposable.
  - Claude runs with `--permission-mode dontAsk --no-session-persistence` and only
    `Read, Grep, Glob, Bash(git diff|log|show|status:*)` allowed.
  - Codex runs with `exec --ephemeral -s read-only`, with no network.
  - The reviewer reads the change through local git (`git diff origin/<base>...HEAD`),
    not through `gh`.
- **Consequence.** The probe checks that neither reviewer can write. The reviewer's
  prompt carries durable state: the issue, the context file, decisions, the reported
  validation, and earlier review rounds. It never includes the implementer's session.

## D34. The runner drives the whole loop

- Date: 2026-09-24. Stage 5. Decided by the agent.
- **Decision.** One runner process goes through implement, check the PR, review, and
  fix, then back to review. Findings resume the implementer's own session with
  `agent.FindingsMessage`. If the session is gone, a fresh turn lists them under
  "Outstanding review findings". The task is WORKING while the implementer runs,
  REVIEWING while the reviewer runs, and stays REVIEWING after a clean review until
  Stage 6's gate. The runner keeps its pid for the whole loop, so a dead runner in
  REVIEWING is caught like one in WORKING.
- **Consequence.** A restart after a crash goes through `start`, which reimplements
  (the agent reuses the branch and the existing PR) and doesn't resume a half-finished
  review.

## D35. The review round cap works as a budget per decision

- Date: 2026-09-24. Stage 5. Decided by the agent. This refines D11.
- **Decision.** Once `review_rounds` rounds (default 3, configurable) have passed since
  the developer last answered a question, the task becomes NEEDS_DECISION with a normal
  question: option A is to address the remaining findings in another round, and option
  B is to accept them as they are. The recommendation is A if any finding is blocking,
  otherwise B. The answer is delivered to the implementer like any decision. The
  reviewer sees it as a settled requirement. Answering resets the budget.
- **Consequence.** Handling a disagreement between agents adds no new states or
  commands.

## D36. How CI is read

- Date: 2026-09-24. Stage 6. Decided by the agent.
- **Context.** `gh pr checks` exits 8 while checks are pending and non-zero when a
  check failed, and it errors with "no checks reported" when a PR has none. The user
  declined a live probe of real PRs, so the exact exit code with `--json` was not
  observed.
- **Options.** (A) Gate on required checks only (`--required`). (B) Gate on every check.
- **Decision.** B, through `gh pr checks <n> --json name,bucket,link`. The exit code is
  ignored whenever gh printed JSON, so both possible exit behaviors parse the same way.
  Failing wins over pending, which wins over passing. `cancel` counts as failing and
  `skipping` as passing. The names and links of failing checks are recorded.
- **Consequence.** A flaky optional check holds a task back. That is safer than letting
  a red PR reach the developer as ready.

## D37. A PR with no checks passes the gate

- Date: 2026-09-24. Stage 6. Decided by the agent.
- **Options.** (A) Treat no checks as passing. (B) Never mark such a task ready.
- **Decision.** A. The state is recorded as `none`, and inbox shows `✓ CI (no checks)`,
  so the developer can see that nothing ran.
- **Consequence.** Repos without CI still reach READY_FOR_HUMAN.

## D38. Failing CI sends the implementer back, twice at most (superseded by D46)

- Date: 2026-09-24. Stage 6. Decided by the agent. This answers the open question in
  the Stage 6 plan.
- **Options.** (A) Only show the failure. (B) Resume the implementer to fix it, with a
  cap.
- **Decision.** B. When the gate sees failing CI, it sets the task to WORKING,
  increments `ci_fixes`, and launches the runner. The runner resumes the implementer
  with the failing checks, or starts a fresh turn that lists them under "Failing CI".
  The fix then goes through PR verification and a new competing review like any other
  round. After `MaxCIFixes` (2), the task becomes FAILED with the failing check names.
  Any implementer turn clears the stored CI result, because it no longer describes the
  branch.
- **Consequence.** `inbox` and `status` can launch a runner. They only do so for a task
  that is idle and was already delegated, which matches "autonomous PR completion".

## D39. When the readiness gate runs and what it changes

- Date: 2026-09-24. Stage 6. Decided by the agent.
- **Decision.** The gate runs at the start of `inbox` and `status`, after dead runners
  are reaped. It only looks at tasks with a PR and no runner: REVIEWING tasks whose
  latest review is clean and belongs to the current PR, and READY_FOR_HUMAN tasks.
  - A merged PR sets the task to DONE.
  - A PR closed without merging sets it to FAILED.
  - For a REVIEWING task with an open PR, green CI sets it to READY_FOR_HUMAN, pending
    CI records the state and changes nothing else, and failing CI follows D38.
  - READY_FOR_HUMAN tasks are checked only for merge or close. CI is not re-checked
    after the task is handed over.
  - Separately, a closed issue ends any delegated task that no runner is driving.
  - Gate changes are reported under CHANGED in `inbox`. Ready tasks appear in READY FOR
    YOU with the draft's checklist: validation, CI, competing review, and decisions.
- **Consequence.** `status` now calls gh once or twice per gated task.

## D40. Clean re-reviews don't use up the review budget

- Date: 2026-09-24. Stage 6. Decided by the agent. This refines D35.
- **Context.** In the end-to-end run, two CI fixes produced three clean reviews. The
  D35 budget counted them all, and the task escalated with "the reviewer still requests
  changes" even though nothing was requested.
- **Decision.** The budget counts only rounds that requested changes since the last
  answer (`ChangeRequestsSinceDecision`). A loop of findings alone escalates at the
  same point as before.
- **Consequence.** A CI fix no longer triggers a false review escalation.

## D41. What cleanup removes

- Date: 2026-09-25. After Stage 6. Requested by the user. Details decided by the agent.
- **Decision.** `foreman clean` removes the worktrees of DONE tasks. `foreman clean
  <issue>` also accepts a FAILED task, and it refuses any other state.
  - It runs plain `git worktree remove`, without `--force`, from the repository's
    shared `.git`. A worktree with uncommitted or untracked changes is kept and
    reported (`ErrWorktreeDirty`).
  - The local branch is deleted only when GitHub reports the task's PR as merged and
    `cleanup.delete_merged_branch` is on. It is deleted with `-D`, because a
    squash-merged branch is not an ancestor of the base branch.
  - Otherwise the branch stays, so a closed-by-hand issue or a FAILED task can be picked
    up again with `start`.
  - Remote branches are left to GitHub's "automatically delete head branches" setting.
  - A path that no longer exists counts as already removed.
  - `worktree_removed_at` is recorded, and `--dry-run` shows the plan without running
    it.
- **Consequence.** Cleanup never loses uncommitted work or unmerged commits.

## D42. Cleanup runs automatically from inbox, and can be turned off

- Date: 2026-09-25. After Stage 6. Decided by the agent.
- **Options.** (A) Only the explicit command. (B) Also clean when `inbox` sees a task
  DONE.
- **Decision.** B by default. The `cleanup.on_done` and
  `cleanup.delete_merged_branch` config keys are both on unless set to false. Results
  show under CHANGED in inbox, for example "worktree and merged branch removed" or
  "worktree kept: it has uncommitted or untracked changes". `status` doesn't clean,
  so it stays a view.
- **Consequence.** Merged work stops using disk space without an extra command. A
  worktree the user is still working in stays because of D41.

## D43. Releases are GitHub releases built by a script

- Date: 2026-09-25. After Stage 6. Requested by the user: GitHub hosting only.
- **Options.** (A) GoReleaser. (B) A shell script run by a tag-triggered GitHub Actions
  workflow.
- **Decision.** B. `scripts/release.sh <version>` builds `CGO_ENABLED=0 -trimpath`
  binaries for darwin and linux on arm64 and amd64. Each archive holds the binary and
  the README, and the script writes `checksums.txt`. Pushing a `v*` tag runs
  `.github/workflows/release.yml`, which runs vet and the tests, then the script, then
  `gh release create --generate-notes`. Windows is not built, because the runner uses
  `setsid`. The binaries are not signed or notarized.
- **Consequence.** No new tool to install or pin. The same script runs locally and in CI.

## D44. Version stamping

- Date: 2026-09-25. After Stage 6. Decided by the agent.
- **Decision.** `main.version` defaults to `dev` and is set with
  `-ldflags "-X main.version=<tag>"`. `foreman version` and `foreman --version` print
  it.

## D45. Branches use a conventional-commit type prefix

- Date: 2026-09-25. After Stage 6. Requested by the user, who asked for `feat/**` or
  `fix/**` instead of `agent/**`. The agent chose how the type is picked. This
  supersedes the `agent/` prefix in D12. The slug rules and the worktree path from D12
  are unchanged.
- **Decision.** The branch is `<type>/<issue>-<slug>`. The type is one of `feat`,
  `fix`, `chore`, `docs`, `refactor`, `perf`, `test`, `build`, or `ci`, chosen in this
  order:
  1. `foreman start --type <type>`.
  2. A conventional prefix in the issue title (`fix(queue): …`).
  3. The first issue label that names a type, with or without a `type:` or `kind/`
     prefix. For example `bug` → `fix`, `enhancement` → `feat`, `documentation` →
     `docs`, `performance` → `perf`, `dependencies` → `chore`.
  4. `feat`.
  
  `gh issue view` now also reads `labels`. The worktree directory keeps the plain
  `<issue>-<slug>` name.
- **Consequence.** A task that already has a branch keeps it, including the old
  `agent/…` branches, so restarts and cleanup still find their worktree
  (`WorkNameOf`). The type is set on the first start and never changes after that.

## D46. CI belongs to the repo owners; foreman only reads it

- Date: 2026-09-25. After Stage 6. Decided by the user, who said CI is the repo owners'
  responsibility and foreman only checks whether the tests pass and reads CI steps
  such as GitHub Actions. This supersedes D38.
- **Decision.** Foreman reads GitHub's check results for the PR with `gh pr checks`
  (D36). It never runs, configures, or fixes CI.
  - A clean review with finished CI makes the task READY_FOR_HUMAN, whether CI passed
    or failed. Failing checks are listed with their links (`✗ CI: test`), and the
    developer decides whether the change or the repo's CI is at fault. Pending CI still
    waits.
  - The CI fix loop is removed: the gate no longer launches runners, and `ci_fixes`,
    the CI resume message, and the "Failing CI" prompt section are gone.
  - READY_FOR_HUMAN tasks now have their CI refreshed on each gate run, so a check the
    repo owners fix later shows as `✓ CI`. This replaces "not re-checked" in D39.
  - Tests are still the agent's reported validation (D32).
- **Consequence.** Foreman never spends agent time on CI it doesn't own. A red PR can
  now reach READY FOR YOU, and the checklist marks it clearly.

## D47. Stage instructions are the developer's workflow layer

- Date: 2026-09-25. Stage-instructions work, Stage 1. Specified by the user. The agent
  chose the shape.
- **Context.** The user wants their own reusable instructions per workflow stage, such
  as a three-pass review or a PR description format. A repo can extend them. The
  agents' own instruction files must stay the agents' concern.
- **Decision.**
  - `task.Stage` has the values `implement`, `review`, and `pr` (`task.Stages`,
    `ParseStage`).
  - `stages.Files{GlobalDir}.Resolve(repoRoot, stage)` reads
    `<GlobalDir>/<stage>.md` and then `<repoRoot>/.foreman/<stage>.md`. It returns a
    `task.StageInstructions` that keeps each layer's path and text.
  - `Content()` joins the non-blank layers, global first and repo second.
  - A missing file is not an error. A stat or read failure is an error that names the
    path, and so is a path that isn't a regular file.
  - Foreman never reads CLAUDE.md or AGENTS.md, never interprets the Markdown, and never
    merges task context into stage text.
- **Consequence.** One small, pure-I/O component holds all resolution logic, so the
  wiring stages only decide which stage's `Content()` goes into which prompt.

## D48. Where the two stage layers are read from

- Date: 2026-09-25. Stage-instructions work, Stage 1. Decided by the agent. The user
  was told and did not object.
- **Decision.**
  - The global layer is `stages/` next to the config file: `~/.config/foreman/stages/`
    by default, or next to `$FOREMAN_CONFIG` when that is set. `Config.StagesDir` is
    derived, not a YAML key, because the user wants convention over configuration.
  - The repo layer is read from the task's worktree root. That is the committed
    `.foreman/` on the task's base branch, not the folder `start` was run from.
- **Consequence.** A stage file that is edited but not committed in the main checkout
  doesn't affect tasks. Moving the config file moves the stage files with it.

## D49. pr.md is loaded in the implementation turn

- Date: 2026-09-25. Stage-instructions work. Decided by the user (option A, the
  recommendation).
- **Context.** The implementation agent opens the PR and writes its description in the
  same turn (D7). There is no separate PR step.
- **Options.** (A) Load pr.md into the implementation prompt, in its own section for
  writing the PR description. (B) Add a separate turn that opens the PR with only
  pr.md.
- **Decision.** A. The implementation prompt carries `implement` and `pr`, each in its
  own section. It never carries `review`. The review prompt carries only `review`.
- **Consequence.** No extra agent turn, and no change to the runner loop or the D7
  contract.

## D50. How implement instructions enter the implementation prompt

- Date: 2026-09-25. Stage-instructions work, Stage 2. Decided by the agent.
- **Decision.** The runner resolves `implement` for the task's worktree each time it
  builds a fresh implementation prompt. The text goes into a "Developer workflow
  instructions" section after the issue and before the task context.
  - A one-line preamble says to follow the section alongside the agent's own repository
    instructions, and that the task context and recorded decisions win on conflict.
    That matches the spec's precedence.
  - Resume messages (answers, findings) don't repeat it, because the resumed session
    already has it.
  - A resolve error, such as an unreadable file, fails the task with the error before
    any agent runs.
  - The app reaches the resolver only through the `StageInstructionResolver` port.
- **Consequence.** An edit to a stage file takes effect on a task's next fresh turn,
  not in the middle of a session.

## D51. How review instructions enter the competing review

- Date: 2026-09-25. Stage-instructions work, Stage 3. Decided by the agent.
- **Decision.** Every review round resolves `review` for the worktree and adds it as
  "Developer workflow instructions" after the issue and before the task context, like
  D50.
  - When the section is present, the built-in "What to look for" list becomes the
    default, so a review process the developer defines, such as three passes, takes
    precedence.
  - The JSON verdict and findings format always applies, because the runner parses it.
  - A resolve error fails the review, and so the task, before the reviewer runs.
- **Consequence.** The developer's review process reaches both reviewers, Claude and
  Codex, the same way. Foreman doesn't parse it.

## D52. How pr instructions enter the implementation turn

- Date: 2026-09-25. Stage-instructions work, Stage 4. Decided by the agent, following
  D49.
- **Decision.** The runner resolves `pr` for the worktree alongside `implement` for
  each fresh implementation prompt. It adds a "Pull request description" section next
  to the PR rules, which says the instructions apply to the PR body, both at
  `gh pr create` and on later `gh pr edit --body`, and not to the code.
  - With the section present, the PR bullet keeps only the fixed rule ("Closes #N",
    which GitHub needs to link and close the issue) and points to the section.
  - Without it, the old default body rule applies: summary, validation, and decisions.
  - The review prompt never gets `pr`, and a resolve error fails the task before the
    agent runs.
- **Consequence.** The developer owns the PR format, and foreman keeps only the issue
  link it depends on.

## D53. Inspecting stage instructions from the CLI (superseded by D55)

- Date: 2026-09-25. Stage-instructions work, Stage 5. Decided by the agent, following
  the shape the spec suggested.
- **Decision.**
  - `foreman config stages` lists each stage's global and repo source. A missing file
    shows as "not configured (<where it would be>)", so the user knows where to create
    it.
  - `foreman config stage <stage>` prints the sources and then the combined text,
    exactly as agents receive it.
  - The repo layer is found from the git root of the current directory
    (`git rev-parse --show-toplevel`). Outside a repository it shows "not in a git
    repository".
  - A footer notes that tasks read the committed `.foreman/` from their worktree (D48),
    because an uncommitted local file shows here but doesn't reach tasks.
  - A read error is shown on its stage and exits 1.
  - `config` has only these two subcommands for now. The command goes straight to the
    resolver and doesn't go through `app`, because it reads no task state.
- **Consequence.** Everything that shapes an agent's stage instructions can be seen
  without running a task.

## D54. Interactive inbox and storage setup

- Date: 2026-09-25. New inbox usability stage. Decided by the user for the
  interactive flow; the implementation shape follows the approved design.
- **Context.** Issue numbers are only unique within a repository. The current
  inbox marks displayed items seen, which makes repeated runs hide work the user
  may still want to act on. Markdown files already work as an Obsidian vault but
  first use gives no setup guidance.
- **Decision.**
  - `foreman inbox` is interactive by default. It presents a repeatable,
    repo-qualified picker, asks for an implementation agent and optional context,
    then delegates through the existing start flow.
  - An explicit non-interactive mode retains report output for scripts.
  - Display identity is always `owner/repo#N`; numeric references remain valid
    only when unambiguous.
  - Markdown remains the storage backend. First use explains that the configured
    task directory can be an Obsidian vault folder or a normal directory and can
    offer to create it. No Obsidian API or second backend is introduced.
  - Inbox refresh persists the GitHub snapshot but does not make current items
    permanently disappear from a later interactive run.
- **Consequence.** The CLI gains prompting and selection seams, while app, store,
  and GitHub lifecycle boundaries remain intact. Automation must opt into the
  non-interactive report mode.

## D55. Repository filtering and direct config modes

- Date: 2026-09-25. New configuration stage. Specified by the user; the exact-match
  shape and version fallback were chosen during implementation.
- **Decision.**
  - `ignore_repositories` is a list of exact `owner/name` values. Invalid values
    fail config loading. The application filters both fresh inbox items and tracked
    refreshes before reconciliation.
  - `foreman config` requires exactly one of `--stages`, `--show`, or `--edit`.
    `--show` prints resolved non-secret settings. `--stages` keeps the existing
    all-stage inspection. `--edit` opens the resolved config path using `$VISUAL`,
    `$EDITOR`, or `vi`.
  - The unstamped local version is `0.1.0-dev`; release builds continue to replace
    it with the release tag through `-ldflags`.
- **Consequence.** Config behavior is discoverable from one flag-based command,
  repository filtering has one exact contract, and local builds no longer report
  the ambiguous bare `dev` value.

## D56. Inbox delegation is checkout-independent

- Date: 2026-09-25. Bug fix after Stage 8. Specified by the user.
- **Context.** Interactive inbox can list repositories other than the repository
  containing the current working directory. Reusing `Start` with that directory
  caused `this checkout is X, not Y` before worktree creation.
- **Decision.**
  - A selected inbox item starts with `Dir` empty and its repo-qualified key.
  - A `RepositoryManager` ensures a source checkout at
    `repositories_dir/<owner>-<repo>`, cloning through `gh repo clone` when absent.
  - `App.Start` uses that managed checkout only when no explicit `Dir` is supplied.
    The explicit `foreman start` command remains checkout-bound and keeps its
    mismatch guard.
- **Consequence.** `foreman inbox` works from any directory and safely maintains
  separate source and agent worktree paths. The first delegation of a repository
  may clone it locally.

## D57. Timestamped logs, bounded spend, and terminal-state notifications

- Date: 2026-09-25. Observability and cost-control stage. Specified by the user and
  approved after design review.
- **Decision.**
  - Add `foreman logs owner/repo#N`, with clean output by default, `--follow` for a
    live detached run, and `--raw` for adapter JSONL.
  - Timestamp lines at the runner logging boundary and persist per-task usage.
    Claude's reported `total_cost_usd` is authoritative. Codex exposes token usage
    but no per-session dollar amount, so Foreman records tokens and does not invent
    a price.
  - Pass Claude implementer and reviewer budgets of $3.00 and $1.00 by default,
    keep the existing three review-round limit, and stop as soon as review is clean.
  - Notify only on `NEEDS_DECISION`, `FAILED`, and `READY_FOR_HUMAN`. Native desktop
    notifications are best-effort; task state and inbox remain durable.
- **Consequence.** A detached run is readable without tailing raw JSON, typical
  Claude work is bounded, Codex usage is measurable but not falsely priced, and
  notifications remain actionable rather than noisy.

## D58. The runner timestamps its own log

- Date: 2026-09-25. Bug fix found in the repository review. Fixed on the user's
  request to fix everything the review found.
- **Context.** D57 wrapped the runner's log file in a timestamping writer inside
  `runner.Processes.Spawn`. When a child's output isn't an `*os.File`, Go copies it
  through a pipe owned by the parent. `foreman start`, `respond`, and `inbox` exit
  right after spawning, so the pipe lost its reader and the runner died on its
  next write. The next `status` reported it as FAILED.
- **Decision.**
  - `Spawn` passes the log file itself as the runner's stdout and stderr.
  - The `__run` process wraps its own stdout in `logs.Writer` and hands that to
    `RunTask`, so agent output and foreman's lines are stamped once, at write time,
    by the process that lives as long as the run. A last line with no newline is
    flushed on exit.
  - `scripts/e2e.sh` runs the built binary against a local origin, a stand-in `gh`,
    and stub agents. It is the check that a detached run survives its launcher.
- **Consequence.** Delegation works again. The regression test in
  `internal/runner` launches a runner from a process that exits at once, which is
  the case unit tests with a live parent can't see.

## D59. Notifications follow state transitions, and a stopped run stops its agent

- Date: 2026-09-25. Bug fixes found in the repository review.
- **Context.** D57 notified from two call sites, the runner's main loop and the
  gate. A task escalated at the review-round cap, or marked FAILED because its
  runner died, changed state without a notification. Driving a stopped runner end
  to end also showed that SIGTERM left the runner waiting for any process the
  agent had started, because Go's `Wait` blocks while a grandchild holds the
  output pipe.
- **Decision.**
  - Every task write in `app` goes through `App.update`, which wraps
    `Store.Update` and notifies when the state changes into NEEDS_DECISION,
    FAILED, or READY_FOR_HUMAN. Call sites no longer decide.
  - `agent.RunCommand` starts the agent in its own process group, sends the group
    SIGTERM on cancel, and sets a 5-second `WaitDelay`.
- **Consequence.** Any new transition is announced without extra code. A stopped
  runner records FAILED within seconds and leaves no agent tools running.

## D60. `foreman logs --follow` prints whole lines only

- Date: 2026-09-25. Gaps against D57 found in the repository review.
- **Context.** Follow mode re-scanned the file every 500 ms with a new scanner, so a
  line the runner was still writing came out as two broken lines. D57 also says
  the log reports Codex USD as unavailable, and it didn't.
- **Decision.** `logs.Show` owns both modes. It holds back a partial line until its
  newline arrives while following, and prints it as-is when not following. The
  Codex usage line ends with `(USD unavailable)`.
- **Consequence.** The follow view matches the clean view line for line. The
  `logs` scenario in `scripts/e2e.sh` follows a live run.

## D61. Inbox prompts share stdin, the picker offers issues only, and the editor runs through the shell

- Date: 2026-09-25. Bugs found running Stages 7 to 9 end to end for the first time,
  on the user's request to fix everything the repository review found.
- **Context.**
  - First-run setup and the picker each wrapped stdin in their own scanner. With
    piped input, setup's scanner read ahead and the picker saw end of input.
  - The picker listed review-requested PRs. `start` only delegates issues, so
    choosing one could only fail.
  - `config --edit` ran `$EDITOR` as a single program name, so `code -w` failed.
- **Decision.**
  - `inbox` makes one `bufio.Reader` over stdin. `EnsureTasksDir` and
    `ChooseInbox` take it and read one line per answer.
  - `ChooseInbox` lists only issues. PRs stay in the non-interactive report.
  - The editor runs as `sh -c '<editor> "$1"' sh <path>`, as git does.
- **Consequence.** The `inbox` scenario in `scripts/e2e.sh` drives first-run setup,
  `ignore_repositories`, the picker, delegation from outside any checkout with a
  fresh clone, the non-interactive report, `config --show`, and `config --edit`.

## D62. A runner stops when its task file is removed

- Date: 2026-09-25. Bug found in the repository review.
- **Context.** Every runner write assumed the task file still existed. Deleting it
  during a run, for example from an Obsidian vault, crashed the runner with a nil
  pointer.
- **Decision.** Runner writes go through `existing`, which returns
  `errTaskRemoved` and saves nothing when the file or its delegation is gone. The
  runner never recreates a task the developer removed.
- **Consequence.** Removing a task file is a way to drop a task. The runner logs
  why it stopped and exits.

## D63. A review covers one PR head

- Date: 2026-09-25. Closes two known limitations on the user's request to fix
  everything the repository review found: a push after a clean review was never
  re-reviewed, and a restart after a crash during a review reimplemented the task.
- **Context.** Foreman didn't record which commit a review covered. A restart of a
  FAILED task also rebuilt its delegation, which dropped the verified PR.
- **Decision.**
  - Foreman reads `headRefOid` with every PR. The delegation keeps it as `pr_head`,
    and each review records the `head` it covered.
  - `Record.ReviewPending` is true when the PR head has no review and no answer
    is waiting to be delivered. A run then starts at the review: it re-verifies
    the PR, records the head, and runs the reviewer. The implementer doesn't run.
  - A restart on the same branch keeps the PR number, URL, and head.
  - The gate compares the PR head with the latest clean review. A REVIEWING task
    whose PR moved goes back to WORKING and gets a runner, which reviews the new
    commits before the task can become READY_FOR_HUMAN.
  - A READY_FOR_HUMAN PR belongs to the developer, so new commits there are
    flagged in the checklist ("! new commits since the review") and not taken back.
  - Records from before this decision have no heads, and keep the old behavior.
- **Consequence.** The reviewer's verdict always matches the code the developer
  receives. A crash during a review costs one review, not a reimplementation.

## D64. Inbox searches are complete or say so (supersedes parts of D15)

- Date: 2026-09-25. Closes three known limitations on the user's request to fix
  everything the repository review found: 100 results per search, mentions from
  issues only, and lost reasons never reported.
- **Context.** D15 capped each search at 100 results, so an item missing from a
  search proved nothing, and it ignored lost reasons for that reason.
- **Decision.**
  - Each search asks for gh's maximum of 1000 results. A search that returns 1000
    is reported as truncated, and the inbox and picker print a warning naming it.
  - A fourth search lists open PRs that mention the developer.
  - When no search was truncated, a reason an open item had and no longer has is
    reported under CHANGED, once: "no longer assigned to you", "no longer mentions
    you", "review no longer requested". A delegated item that only the tracked
    re-fetch found has lost every reason, and its stored reasons are cleared.
  - Closed items are never judged, because the searches only return open ones.
    With a truncated search, D15's rule still holds.
  - `GitHubClient.InboxItems` returns an `InboxSearch` with the items and the
    truncated searches, and `Reconcile` takes whether the searches were complete.
- **Consequence.** Unassigning the developer shows up in the inbox. Items in
  NEEDS DECISION and READY FOR YOU still appear only in their own section (D15).

## D65. A blocked turn can ask several questions

- Date: 2026-09-25. Closes the "one question pending at a time" limitation on the
  user's request to fix everything the repository review found.
- **Context.** The outcome carried one `question`. An agent with two decisions
  stopped, waited, resumed, and stopped again, paying a resume and a wait per
  decision.
- **Decision.**
  - The outcome schema replaces `question` with `questions`, an array. A blocked
    outcome needs at least one, and each is validated as before (D29). The prompt
    asks the agent to raise every decision it already knows about in one turn.
  - Each question becomes its own decision. `Pending` is the oldest unanswered one,
    so questions are answered in the order they were asked.
  - `Respond` answers one question. The task stays NEEDS_DECISION until the last
    answer, and then the runner is launched once. The resume message carries every
    answer the agent hasn't received.
  - `foreman respond` asks each waiting question in turn. If input ends before an
    answer is typed, it stops and says how many are still waiting, instead of
    accepting a recommendation the developer never saw.
  - Inbox shows the first question with "(+N more)", and `show` says how many
    are waiting.
- **Consequence.** `scripts/probe-agents.sh` checks that both CLIs return a
  `questions` array under the strict schema (17 checks, passing on Claude Code
  2.1.281 and Codex 0.156.1).

## D66. What the repository review left as designed

- Date: 2026-09-25. Scope decisions for the user's request to fix everything the
  repository review found. Decided by the agent.
- **Decision.**
  - The gate still runs only on `inbox` and `status`. The design rules out daemons
    and polling (D5), so a waiting runner or a watcher would reverse a user-level
    constraint, not fix a defect.
  - `gh pr checks` is still not probed against a live PR, because the user declined
    that probe (D36).
  - Validation stays self-reported (D32). Re-running agent-reported commands outside
    the agent's sandbox would run untrusted shell. CI is the independent check.
  - The release workflow still can't run until the repo has a GitHub remote.
    `scripts/release.sh` was re-run locally: four archives, valid checksums, and a
    stamped `foreman v0.0.0-test`.
  - `progress.md` keeps the stage numbers the decisions refer to. The second
    "Stage 7" (D57) becomes Stage 10, Stages 7 to 9 move out of the
    stage-instructions track, and the review is Stage 11.
- **Consequence.** The known limitations in `progress.md` are exactly these.

## D67. Picking up at the review has two exceptions (amends D63)

- Date: 2026-09-25. Found reviewing the D63 change before handing it over.
- **Decision.**
  - A run that would pick up at the review, but whose PR is no longer open or no
    longer matches the task branch, logs why and runs the implementer instead of
    failing the task. The implementer opens a new PR as usual.
  - A restart keeps the PR only when the implementer agent is the same. Another
    implementer starts over, because its reviewer is the agent that wrote the PR.

## D68. Keep changes visible on waiting tasks and refresh missing items (amended by D70)

- Date: 2026-09-26. Requested after verifying the remaining inbox gaps.
- **Decision.** Keep each waiting task in its NEEDS DECISION or READY FOR YOU
  section, with its GitHub changes and readiness-gate notes beneath it. Include
  gate notes in the interactive entry too. This amends D15 and D64 without
  duplicating the task in CHANGED.
- **Decision.** Refresh all stored, unfinished items missing from search results,
  including undelegated issues. Continue skipping ignored repositories and DONE
  tasks. Truncated searches still cannot prove that an assignment was lost.
- **Consequence.** A missing item receives a current snapshot, and changes on a
  waiting task are shown before the snapshot advances past them.

## D69. Identify local builds by their embedded Git revision

- Date: 2026-09-26. Requested after verifying that local versions look identical.
- **Decision.** Append the first 12 characters of Go's embedded `vcs.revision` to
  the default development version, plus `-dirty` when `vcs.modified` is true.
  Missing metadata prints `unknown`. Explicit release version stamps stay intact.
- **Consequence.** `foreman version` identifies the source commit used to build the
  running binary, regardless of the current directory or Git installation.

## D70. Bound missing-item refreshes and isolate lookup failures (amended by D71)

- Date: 2026-09-26. Supersedes the missing-item refresh rule in D68.
- **Decision.** Stop refetching undelegated items after a successful refresh and
  complete searches clear their involvement reasons. Truncated searches preserve
  reasons; delegated work remains tracked. Reappearance in search restores tracking.
- **Decision.** Item, PR, and CI lookup failures warn in either inbox mode without
  blocking healthy items. Preserve stored state and retry failures next refresh;
  cancellation, search failures, and storage failures remain fatal. Status retains
  explicit errors rather than silently displaying stale gate results.
- **Decision.** Only search-returned, non-closed issues enter the delegation picker.
  Historic task files remain available through show/status. Store waiting changes
  only when nonempty and use one development-version constant.
- **Decision.** Show waiting-task updates and nonselectable-item change notices
  before the interactive picker, so filtering choices does not discard updates.

## D71. Gone issues stop being checked, and status warns instead of failing

- Date: 2026-09-26. Found reviewing D70 before commit; fixed on the user's request.
- **Context.** Under D70 an issue GitHub no longer has warned on every inbox run
  forever, and `status` printed no tasks when one PR or CI lookup failed.
- **Decision.**
  - The gh adapter maps HTTP 404 (missing, or no access) and 410 (deleted) on an item
    lookup to `app.ErrGone`. For an undelegated item, the inbox reports it once under
    CHANGED, clears its stored reasons so it isn't fetched again, and leaves its state
    alone. Delegated work keeps warning, because the developer owns it.
  - Other lookup failures stay warnings that retry next run (D70).
  - `Status` returns its tasks with the gate's lookup warnings, and `foreman status`
    prints them under the list. This replaces D70's rule that status fails.
- **Consequence.** A deleted repository or issue costs one notice, not a warning per
  run, and `status` is useful while one PR can't be read.

## D72. Default stage instructions ship in the repo, installed by a Makefile

- Date: 2026-09-26. User request.
- **Context.** Stage files (D47) were optional and had no defaults, so a fresh clone
  gave the agents no standing instructions.
- **Decision.**
  - `defaults/stages/{implement,review,pr}.md` hold default instructions. They add to
    foreman's built-in prompts and don't repeat them. The implement file sets the
    writing rules for commits, comments, and summaries.
  - A `Makefile` adds `setup` (install, then config), `build`, `install` (to
    `BIN_DIR`, default `~/.local/bin`), `config`, and `test`. `config` copies the
    defaults into `stages/` next to the config file (D48), honoring `FOREMAN_CONFIG`,
    and skips any file that already exists.
  - Make is a tooling addition only; the Go build needs nothing new.
- **Consequence.** `make setup` takes a clone to a working install with default stage
  instructions. Foreman itself still treats the files as optional.

## D73. Agent working rules live in the stage files, which become required

- Date: 2026-09-26. User request. Amends D47, D49, and D72.
- **Context.** Foreman's prompts held working rules (how to validate, when to ask, what a
  review reports, the default PR body) that the default stage files (D72) then
  partly repeated.
- **Decision.**
  - The working rules move out of `internal/agent` into `defaults/stages/*.md` as
    numbered lists. The prompts keep only what carries task values or what foreman
    parses: the issue, the worktree and branch, the push and `gh pr` commands,
    `Closes #N`, the read-only reviewer, the question fields, the JSON outcome, and
    the precedence of the task context and decisions over stage instructions.
  - The default PR body rule is gone; `pr.md` defines the body.
  - All three global stage files are required. Every command except `config` fails
    naming the missing files, so `config --stages` can still diagnose them. The user
    chose failing over an embedded fallback: `make config` always installs them.
  - Release archives carry the defaults in `stages/`.
- **Consequence.** Editing the global files changes all the agents' working rules. A
  deleted global file stops foreman until it is restored.

## D74. The default PR body follows a problem, fix, test, risk outline

- Date: 2026-09-26. User request. Amends D73.
- **Decision.** `defaults/stages/pr.md` orders the body as What was wrong (with the root
  cause folded in), The fix (with decisions), How to test, Risk, then `Closes #N`. Risk
  is always present; a low-risk change says why in one sentence.

## D75. Agent commits carry no agent attribution

- Date: 2026-09-26. User request. Amends D73.
- **Decision.** `defaults/stages/implement.md` rule 19 forbids agent `Co-Authored-By`
  trailers and "Generated with" or "Powered by" lines in commit messages. Commits use
  the repository's configured git identity. `defaults/stages/pr.md` rule 5 applies the
  same to PR titles and bodies.

## D76. The reviewer knows its place in the review_rounds budget

- Date: 2026-09-26. User request. Amends D73.
- **Context.** `review_rounds` capped the loop (D35, D40), but the reviewer never saw
  the cap, so it couldn't plan its findings around it.
- **Decision.**
  - The review prompt gains a "Review round" section: round
    `ChangeRequestsSinceDecision() + 1` of `review_rounds`, what happens after the last
    round, and "This is the last round." on the last one.
  - `defaults/stages/review.md` rules 6 to 8: report every confirmed finding in round
    1; after that, review what changed and raise new findings on reviewed code only if
    blocking; on the last round, report only blocking findings.
- **Consequence.** A raised `review_rounds` gives the reviewer more rounds before it
  narrows to blocking findings.

## D77. review_rounds counts reviewer passes; fix_rounds caps the fix loop

- Date: 2026-09-26. User correction. Supersedes D76; amends D11 and D35.
- **Context.** D76 read `review_rounds` as the fix-loop budget and told the reviewer
  to narrow its findings as the budget ran out. The user means `review_rounds` as the
  number of passes the reviewer makes over a PR, so it misses nothing.
- **Decision.**
  - `review_rounds` (default 3) is the reviewer's passes per review. The review prompt
    states it and says whether this is a first review or a re-review.
  - New `fix_rounds` (default 3) takes over the cap from D11, D35, and D40: fix and
    re-review cycles that request changes before foreman asks the developer.
  - `defaults/stages/review.md` rules 6 and 7: a first review reads the whole change
    `review_rounds` times before reporting; a re-review checks the fixes first, then
    the PR as a whole.
- **Consequence.** A config that raised `review_rounds` to allow more fix cycles now
  gets more reviewer passes instead, and needs `fix_rounds` for the old effect.

## D78. Every fix is reviewed before the developer is asked

- Date: 2026-09-26. User correction. Amends D35 and D40.
- **Context.** The loop checked the cap before reviewing, so the implementer's last fix
  went unreviewed and the escalation listed findings it may already have fixed.
- **Decision.**
  - The cap is checked after a review. Foreman asks the developer only when a review
    requests changes after `fix_rounds` fixes since the last answer. With the default
    3: implement, review, then up to 3 fix and review pairs.
  - The escalation question lists that review's findings as not yet addressed.
  - The resume after any answer carries the latest review's findings when it
    requested changes, so the implementer sees them with the decision.
- **Consequence.** No fix reaches the developer unreviewed. The default loop runs one
  more review than before.

## D79. The implement defaults drop three rules

- Date: 2026-09-26. User edit. Amends D73.
- **Decision.** `defaults/stages/implement.md` no longer carries "plan, inspect, then
  implement", "make the smallest change", or "open the pull request only when
  validation passes". The remaining rules are renumbered 1 to 16.

## D80. The validation gate on opening a PR moves to pr.md

- Date: 2026-09-26. User request. Amends D79.
- **Decision.** `defaults/stages/pr.md` rule 1: open the pull request only when
  validation passes, else report "failed". D79 had dropped it from `implement.md`.

## D81. Default models: Claude Opus 5 and GPT-6-Sol

- Date: 2026-09-26. User request. Amends D23.
- **Decision.** `claude.model` defaults to `claude-opus-5` and `codex.model` to
  `gpt-6-sol`. Both names were checked against the installed CLIs: a one-line
  `claude -p` call answered on `claude-opus-5`, and `gpt-6-sol` is in Codex's model
  cache. An explicit `""` still means the CLI's own default.
- **Fix.** `make install` removes the old binary before copying. Copying over it in
  place left a binary macOS killed on launch (exit 137).

## D82. Claude's tool list is fixed, not configured

- Date: 2026-09-26. User correction. Amends D23.
- **Context.** `claude.allowed_tools` was the only config key specific to one agent
  CLI's permission model. Removing `Bash` from it broke every task.
- **Decision.** The key is gone. The implementer always runs with
  `--allowedTools=Bash,WebFetch,WebSearch` (`agent.ImplementTools`), next to the fixed
  reviewer list (`agent.ReviewTools`). An old config that still sets the key loads
  without error; the value is ignored.

## D83. Round settings load with their defaults

- Date: 2026-09-27. Found when the user asked why their config said `review_rounds: 0`.
- **Decision.** `config.Load` starts `review_rounds` and `fix_rounds` at 3, so the file
  `config --edit` writes on first use shows the real values instead of 0. An explicit
  0 still means the default.

## D84. config --stages reports a missing global file as required

- Date: 2026-09-27. Found after D73; fixed on the user's request.
- **Decision.** A missing global stage file shows as `MISSING, required (<path>)`, the
  report ends with how to install the defaults, and `config --stages` exits 1. D73 made
  the files required but left this report calling them optional.

## D85. Each agent can review with its own model

- Date: 2026-09-27. User request. Amends D81.
- **Decision.** `claude.review_model` and `codex.review_model` set the model for
  reviews. Empty, the default, reviews with `model`. Implementation turns always use
  `model`.

## D86. The dollar budgets are labeled Claude-only

- Date: 2026-09-27. User decision.
- **Context.** Codex reports no cost, and its token counts arrive only when a turn
  ends, so foreman can't stop a Codex turn at a dollar or token limit.
- **Decision.** `budgets.implementer_usd` and `budgets.reviewer_usd` stay as they are.
  The README and `config --show` say they cap Claude turns only and that Codex has no
  cap. The user chose this over a per-turn time limit for both agents.

## D87. Claude's event stream goes to the runner log

- Date: 2026-09-27. User request, after a $10 Claude run on ridezoomo/armada#351
  left one summary line in its runner log. Amends D57.
- **Context.** Claude ran with `--output-format json`, which prints only the final
  result, so the runner log couldn't show where the time or money went. Codex's JSONL
  events already reach the log.
- **Decision.**
  - Every Claude turn, implementation and review, runs with `--output-format
    stream-json --verbose`. The adapter copies each event line to the runner log and
    reads the outcome from the `result` event. It reads to the end of the stream,
    because events can follow `result`.
  - The summary line adds `turns=<num_turns>`.
  - A stream with no `result` event is `ErrNoSession`, like the empty stdout before.
    Stream mode reports a resume of a missing session as a `result` with
    `error_during_execution` and `num_turns` 0, so that's `ErrNoSession` too and the
    fresh-turn fallback (D28) still applies.
  - `foreman logs` summarizes Claude events as `claude session started`, `claude
    <tool> <description>`, and `claude message`. It hides tool results, thinking, and
    the raw `result` event. `--raw` keeps everything.
- Verified by `scripts/probe-agents.sh` against claude 2.1.283.

## D88. foreman points to Claude's own transcript

- Date: 2026-09-27. User request, alongside D87.
- **Context.** Claude Code already keeps each session's full transcript, including
  per-turn token usage, at `~/.claude/projects/<cwd with non-alphanumerics as
  "-">/<session>.jsonl` (`$CLAUDE_CONFIG_DIR` replaces `~/.claude`). Nothing in
  foreman led there.
- **Decision.** `agent.ClaudeTranscript` computes the path. Each implementation turn
  logs `claude: transcript <path>`, and `foreman show` prints a Transcript row for
  Claude tasks. Reviews have no transcript (`--no-session-persistence`), so the
  stream in the runner log is their only record. foreman only computes the path; it
  doesn't copy or read the transcript.

## D89. foreman cancel stops a running task

- Date: 2026-09-27. User request.
- **Context.** The only way to stop a runaway run, such as the $10 #351 run, was to
  find and kill the runner by hand. D59 already made a SIGTERM'd runner stop its
  agent's process group.
- **Decision.**
  - `foreman cancel <issue>` works on a WORKING or REVIEWING task that has a runner.
    It writes `foreman: cancel requested; stopping runner <pid>` to the task log,
    sends the runner SIGTERM, and waits up to 30 seconds for it to exit
    (`Runners.Stop`). If the runner is still alive after that, cancel exits 1 and
    changes nothing; foreman never SIGKILLs the runner, because that would leave the
    agent running.
  - When a step fails after the runner's context was cancelled, the runner records
    FAILED with outcome status `cancelled` and the summary `run cancelled during
    implementation|review (<cause>)`. The turn's session and usage are still
    recorded. A step that finished before the stop keeps its result.
  - A runner that exits without recording anything is marked cancelled by `cancel`
    itself.
  - The branch, worktree, and PR are kept. `foreman start` resumes the task the same
    way it does any FAILED task, picking up at the review if the PR head has none.
  - A cancelled FAILED task notifies "Run cancelled" instead of "Run failed". Any
    SIGTERM, SIGINT, or SIGHUP to the runner counts as a cancel. The e2e check for a
    SIGTERM'd runner now expects that.
- **Rejected.** A separate CANCELLED state. FAILED already means stopped and
  restartable, and `clean <issue>` and `start` handle it; a new state would touch
  every state table for no new behavior.

## D90. The implementer plans first, and the runner posts the plan on the issue

- Date: 2026-09-27. User request: follow the superpowers workflow, with an analysis,
  a PRD, a design, and an implementation plan, all posted on the issue. Amends D73.
- **Context.** Superpowers writes a spec (analysis, requirements, and design in one)
  and a plan of small TDD tasks, and gates each on the user's approval. The user asked
  for four separate documents and no approval gate: the documents are a record, and
  foreman keeps running.
- **Decision.**
  - A new required stage file, `plan.md`, sets what each document covers. It takes
    superpowers' rules: two or three approaches with a recommendation, testable
    numbered requirements, `### Task N` with exact files and test-first steps, no
    placeholders, and a self-review.
  - Until all four documents are posted, and while the task has no PR, the
    implementer's turn is a planning turn. Its prompt is the full implementation
    prompt plus a "Plan first" section, so the session already holds the
    implementation instructions when it is resumed.
  - The outcome schema gains status `planned` and a nullable `documents` object.
    `ParseOutcome` requires all four documents to be non-empty for `planned`. The
    adapters don't change.
  - The runner, not the agent, posts the documents through `gh api`: one comment each,
    headed Analysis, PRD, Design, and Implementation plan, with a
    `<!-- foreman:<doc> -->` marker. It records each comment's id and URL as soon as
    it posts it. This is foreman's first write to GitHub.
  - A re-plan edits the recorded comments. A comment the developer deleted is posted
    anew. A text over GitHub's 65,536-character limit is cut with a note.
  - After posting, the runner resumes the planning session with the comment links to
    implement. If that session is gone, a fresh turn gets the links under "The plan".
  - There is no approval gate. A planning question sends the task to NEEDS_DECISION.
    The answer resumes the planning turn, which still ends in `planned`.
  - The reviewer can't reach GitHub, so foreman reads the PRD comment and puts it in
    the review prompt. The implementation plan stays out of it to keep reviews cheap.
  - Plan steps name the functions, types, and tests they add, not full code listings.
    Superpowers wants complete code in each step, but the same session implements
    right after, so the listings would pay for the code twice.
  - A failed post fails the task. Restarting it plans again and edits what was posted.
  - A task that already has a PR, from before this stage, goes on without a plan.
- **Rejected.** The agent posting its own comments (the user chose the runner), one
  combined comment (the user chose one per document), and a competing-agent review of
  the documents (the user chose self-review).

## D91. config --edit opens a config that fails to load

- Date: 2026-09-28. Bug the user hit: a stray quote in `ignore_repositories` made
  every command fail, including `config --edit`, the one that fixes it.
- **Decision.** `config --edit` resolves the file path without loading the config.
  If the file fails to load, it prints the error, opens the file as it is, and
  doesn't write defaults over it. Once the editor exits, it loads the file again
  and exits 1 with the error if it's still invalid. Every other command still
  refuses an invalid config.

## D92. The prompts' remaining workflow rules move to the stage files

- Date: 2026-09-28. User request. Amends D73 and D90.
- **Context.** D73 moved the agents' working rules into the stage files, but a few
  rules stayed in the Go prompts, where the developer couldn't edit them.
- **Decision.**
  - Moved to `implement.md`: read the issue comments and the branch's earlier work
    before changing code; follow the posted plan's tasks in order and say why in the
    PR when deviating; commit work in progress before reporting "blocked". The
    writing rules now name the planning documents too.
  - Moved to `plan.md`: read the issue and every comment first.
  - Moved to `review.md`: check the change against the PRD's requirements and
    acceptance criteria; a clean verdict when no substantive problem remains.
  - Added to `pr.md`: list deviations from the posted plan.
  - The Go prompts keep protocol and facts only: the role, unattended or read-only,
    the issue, paths and branch, the push and `gh pr` commands, `Closes #N`, the plan
    links and the PRD text, the planning turn's no-changes rule, the question format,
    the JSON outcome, and precedence between the context file, decisions, and stage
    files.
- **Consequence.** An installed stage file doesn't get the moved rules until the
  developer copies them in, because `make config` never overwrites one.

## D93. A bare `foreman` opens the dashboard

- Date: 2026-09-29. Issue midsoln/foreman#1. Decided by the agent; the user asked for
  "a mode where we issue `foreman`" and left the presentation to the agent's judgment.
- **Context.** Daily use meant `foreman inbox`, then `foreman status`, then
  `foreman logs <issue> --follow`. Each is one shot, so the developer retyped them to
  see whether anything moved.
- **Options.** A. A new `foreman dashboard` verb, leaving bare `foreman` as usage.
  B. Bare `foreman` opens the dashboard. C. Make `foreman inbox` interactive-plus.
- **Decision.** B, with `foreman tui` as the explicit form for scripts and docs.
  `foreman help`, `-h`, and `--help` still print usage. When stdin or stdout is not a
  terminal, bare `foreman` and `foreman tui` print usage and exit 0, exactly as bare
  `foreman` always has, so a pipe or a script never blocks on a UI it cannot draw.
- **Consequence.** The single most common invocation is now the live view. No existing
  command changed. `foreman inbox` stays the scriptable, one-shot picker.

## D94. The dashboard is a pure model and frame over golang.org/x/term

- Date: 2026-09-29. Issue midsoln/foreman#1. Decided by the agent.
- **Context.** Raw terminal mode and the terminal's size both need a `termios` and a
  `TIOCGWINSZ` ioctl. The standard library exposes neither, so the dashboard cannot be
  built under the standard-library-first rule without a decision.
- **Options.** A. Bubble Tea and Lip Gloss. B. `golang.org/x/term` plus a hand-written
  model, frame, and event loop. C. Reprint `inbox` and `status` on a timer with no raw
  mode.
- **Decision.** B. `golang.org/x/term` is the Go team's own package and brings only
  `golang.org/x/sys`, against Bubble Tea's ten or so modules in a project that had one
  dependency. C cannot place panes side by side, read a keystroke without Enter, or
  select anything.
- **Consequence.** `internal/tui` splits three ways, matching how `render` and `app`
  are already split. `Model.Key` is a pure state transition returning an `Action`, and
  `Frame` is a pure function from the model to exactly the lines on screen. Only `Run`
  touches the terminal, so almost all of the UI is unit tested. Scrolling, layout, and
  key decoding are written by hand, about 600 lines.
- **Reuse.** The panes show no text of their own invention: `render.TaskRows` is shared
  with `foreman status`, `app.Delegable` with the `foreman inbox` picker, and
  `logs.Tail` cleans lines the way `foreman logs` does.

## D95. The dashboard polls in the foreground, at two rates

- Date: 2026-09-29. Issue midsoln/foreman#1. Decided by the agent. Amends D5.
- **Context.** D5 rules out a daemon and background polling: the readiness gate runs
  only when a command runs. A view that updates on its own is polling by definition.
- **Decision.** The dashboard refreshes only while it is open, and stops when the
  developer quits. It starts no background process, which keeps D5's substance: nothing
  runs between commands except a task's own runner. Watching the dashboard is the same
  as running `foreman inbox` by hand, on a timer.
  - Local tick, every second: `App.Tasks` re-reads the task files, reaps dead runners,
    and the selected task's log is re-tailed. No GitHub call, so this is cheap enough
    to make the pane move on its own.
  - GitHub tick, every 60 seconds, and on `r`: `App.Inbox` runs the searches, the
    readiness gate, and the configured DONE cleanup, one at a time. It runs in a
    goroutine, so a slow `gh` never blocks a keystroke.
- **Consequence.** `App.Status` keeps the gate; `App.Tasks` is the new gate-free read,
  and both share `activeTasks`. Leaving the dashboard open runs the gate and the
  worktree cleanup once a minute, which is what makes a merged PR turn up as DONE
  without the developer asking. The rates are constants in `cmd/foreman`, not config,
  until someone needs to change them.

## D96. The dashboard's review corrections

- Date: 2026-09-29. Issue midsoln/foreman#1, PR #2 review round 1 by Codex. The
  reviewer found seven defects against the PRD; all seven were accepted.
- **Decisions.**
  - The terminal check moved ahead of `config.Load` and `RequireGlobal`
    (`cmd/foreman/main.go`). A broken config or a missing stage file used to fail a
    piped `foreman`, which D93 promised would print usage and exit 0.
  - A lone ESC, and a rune split across reads, now wait `escapeWait` (40ms) for the
    rest before they are decoded. A terminal read ends anywhere, so `DecodeKey` gained
    a `final` argument: without it a split arrow key arrived as an escape plus two
    letters. 40ms is longer than a terminal takes to write three bytes and shorter
    than a person takes to press a second key.
  - Quitting waits for a `Start` or `Cancel` in flight. `App.Start` records WORKING
    before it spawns the runner, and `listReaped` does not reap a task with no pid, so
    exiting in that window left work that nothing would ever clear. A refresh is still
    abandoned, because it writes nothing.
  - The dashboard makes a typed context file absolute before `App.Start`, as `inbox`
    and `start` already do. The runner a later `foreman respond` spawns reads the
    stored path from its own working directory.
  - Moving the task cursor re-tails the log at once instead of at the next tick, so
    the pane never shows one task's lines under another's heading.
  - `render.TaskHead` exposes the three fields `TaskRows` pads into its first row. At
    60 columns the padded row does not fit the task pane and truncation dropped the
    state, so the pane wraps the agent and state onto their own line. `status` keeps
    its padding, and the text still comes from one place.
  - The NEEDS_DECISION message leads with `foreman respond <owner>/<repo>#<n>`, so a
    narrow terminal truncates the explanation rather than the command.
- **Consequence.** `DecodeKey` and `readKeys` take an extra argument each, and
  `readKeys` now decodes on its own goroutine with a timer beside the byte reader.

## D97. A cancel confirmation names one task for its whole lifetime

- Date: 2026-09-29. Issue midsoln/foreman#1, PR #2 follow-up review. Decided by the
  agent after the user's request to fix the findings.
- **Context.** The local tick can remove a task from the list while its cancel prompt
  is open. The cursor then moves to another task, and confirming used to cancel that
  new selection.
- **Decision.** Store the original task key when the prompt opens. The footer keeps
  naming that key. Confirmation looks up its current record and cancels only if it
  is still running with a runner; otherwise it reports that nothing was cancelled.
- **Consequence.** A refresh cannot redirect a confirmed cancellation to another
  task.

## D98. The dashboard removes terminal controls from dynamic text

- Date: 2026-09-29. Issue midsoln/foreman#1, PR #2 follow-up review. Decided by the
  agent after the user's request to fix the findings.
- **Context.** Issue titles, task fields, runner logs, and error messages reach the
  frame. A control sequence in any of them could move the cursor, clear the screen,
  or leave the alternate screen.
- **Decision.** Replace control characters with spaces before truncating or wrapping
  dynamic text. Keep the frame's own styling sequences outside that text.
- **Consequence.** Dynamic content cannot issue terminal commands through the frame.

## D99. The minimum dashboard size keeps the selected PR visible

- Date: 2026-09-29. Issue midsoln/foreman#1, PR #2 follow-up review. Decided by the
  agent after the user's request to fix the findings.
- **Context.** At 60 by 12, the task pane had three body rows. A wrapped task needed
  four even before its PR text exceeded the pane width, so the PR and badges were
  inaccessible.
- **Decision.** Give the top panes at least five body rows and wrap the shared PR
  status row inside the task pane. The log keeps a heading and one body row at the
  minimum height.
- **Consequence.** The selected task's PR, reviewer, CI, and review status are visible
  at the stated minimum terminal size without changing `foreman status` output.

## D100. A scrolled-back log holds its lines as the log grows

- Date: 2026-09-29. Issue midsoln/foreman#1, PR #2 follow-up review. Decided by the
  agent after the user's request to fix the findings.
- **Context.** `LogOffset` counts lines back from the tail, and the local tick re-tails
  the file every second. A running agent's new lines moved the tail, so the view slid
  forward under a developer who had paged back to read older output.
- **Decision.** `readLog` moves `LogOffset` by the log's growth while the reader is
  scrolled back, and `SetTasks` drops the offset when the cursor lands on another
  task, as moving the cursor already did. End still follows the tail.
- **Consequence.** Paging back holds still. Past `logLines` (500) the oldest lines are
  dropped and the view moves with them, because `logs.Tail` reads only the tail.

## D101. The frame counts terminal columns, not runes

- Date: 2026-09-29. Issue midsoln/foreman#1, PR #2 follow-up review. Decided by the
  agent after the user's request to fix the findings.
- **Context.** `truncate`, `pad`, and `wrap` measured text in runes. A CJK or emoji
  title takes two columns per rune, so at 60 columns the inbox pane's text ran over
  the task pane beside it and the layout's column arithmetic was wrong.
- **Decision.** `internal/tui/width.go` reports a rune's columns: none for a combining
  mark, two in the East Asian wide and fullwidth blocks, one otherwise. The table is
  written out rather than taken from a dependency, and the ambiguous-width runes
  foreman draws itself, such as · ✓ ▸ and …, stay one column, which is what a terminal
  outside a CJK locale draws.
- **Consequence.** A wide rune that would straddle a pane's last column is dropped and
  the line is padded instead, so no line exceeds its pane.

## D102 (amends D99). The wrapped PR row is bounded by the pane's floor

- Date: 2026-09-29. Issue midsoln/foreman#1, PR #2 follow-up review. Decided by the
  agent after the user's request to fix the findings.
- **Context.** D99 wraps the PR status row inside the task pane, and the pane's body
  floor is five rows. A failing CI list wrapped that row onto two lines, which pushed a
  NEEDS_DECISION task's question off the pane at 60 by 12. The pane scrolls by whole
  rows, so the question could not be reached, which is the defect D99 fixed for the PR
  row itself.
- **Decision.** `taskRowLines` (5) names the most lines a task may draw, `layoutFor`
  sizes the pane from it, and the PR row wraps only into what is left once the
  question is counted. Its last line is truncated instead of wrapping further.
- **Consequence.** A task with a waiting question shows one line of PR status and its
  question; a task without one shows two lines of PR status, which fits the reviewer,
  CI, and the review badge at 60 columns.

## D103 (amends D99, D102). The PR row drops detail instead of being truncated

- Date: 2026-09-29. Issue midsoln/foreman#1, PR #2 review round 3 by Codex. The
  reviewer found one defect; it was accepted.
- **Context.** At 60 by 12 the task pane is 32 columns wide and a task with a waiting
  question has one line left for its PR, because the identity, the agent and state, the
  title, and the question take the other four. D102 left the rest of that row to
  truncation, so `PR #912 · reviewer: Codex · CI ✗ test, lint · review round 2: 3
  findings` showed the PR number and cut both badges, which R3 asks for. No key
  revealed them.
- **Decision.** `render.TaskPR` returns the row's facts, each worded as `status` prints
  it, beside a short form of the two that grow without a bound: `CI ✗` without the
  failing checks' names and `review ✗ 3` without the round. The pane takes the first
  that fits: the whole row, then the row without the reviewer, then the states alone.
  `Frame` passes the pane's body height to `taskPane`, so the row grows to `prRowLines`
  (3) where there is room and `status` keeps printing the whole row unchanged.
- **Consequence.** At 60 by 12 a waiting task shows `PR #912 · CI ✗ · review ✗ 3`, and
  at 60 by 24 the same task shows the row `status` prints. The reviewer and the check
  names are what a narrow pane loses, not whether CI or the review passed.

## D104. The log follows the latest delegation by default

- Date: 2026-09-29. PR #2 author comment. Requested by the user.
- **Context.** The task list is ordered by attention, so its first row can be older
  than the most recently delegated issue. The log followed that first row at startup.
  Cancellation also required focus on the task list.
- **Decision.** Store the time of each `Start` in the task file. Until the user moves
  the task cursor, select the newest start on each local refresh. A new delegation
  resumes this default. `c` confirms cancellation of the task shown in the log from
  either list pane. Color the status rows and messages, and draw a vertical divider
  between the lists. Existing task files without a start time remain readable.
- **Consequence.** The newest delegated issue's log appears by default, while a manual
  selection stays on its task through refreshes. A restart of a failed task becomes the
  latest start. Older task files with no start time retain their attention order among
  themselves until they are started again.

## D105. The log is a focusable pane with keyboard scrolling

- Date: 2026-09-30. PR #2 follow-up. Requested by the user.
- **Context.** Tab cycled only between inbox and tasks. Vim movement keys moved list
  cursors, while the log required Page Up and Page Down, which the user does not have.
- **Decision.** Make the log the third Tab stop. With log focus, j/k and arrows move by
  line, u/d move by page, ctrl-u/d move by half page, and g/G jump to the oldest
  available line or follow the tail. Keep Page Up, Page Down, and End as optional keys.
  Show the log keys in the footer while that pane has focus.
- **Consequence.** The log can be read with standard letter keys, and list navigation
  works as before when a list pane has focus.

## D106 (amends D104 and D105). A delegated task opens its log on demand

- Date: 2026-09-30. Issue midsoln/foreman#3. The user clarified that the log view
  should be removed and shown only after Enter on a DELEGATED WORK entry.
- **Decision.** The dashboard starts with two lists and no log. `h` and `l` focus the
  inbox and delegated work; Tab cycles between those lists. Enter on a delegated task
  opens a small log area below them. Escape closes it, and moving to another task
  closes it until Enter selects that task. The log is not a Tab stop. While delegated
  work is focused and its log is open, `u`/`d`, Ctrl+U/Ctrl+D, and `g`/`G` scroll it;
  `j`/`k` continue to select tasks. The task list shows the branch when it has room,
  and colored dividers separate the areas.
- **Consequence.** A log is never displayed merely because a task is highlighted or
  because a local refresh chose the newest delegation. The 60 by 12 layout still
  shows the selected task's PR and state when the log is open.

## D107. A closed PR ends its task in PR_CLOSED, not FAILED

- Date: 2026-10-01. Issue midsoln/foreman#5. Requested by the user.
- **Context.** The gate mapped a closed PR to FAILED, which is the state a crashed
  agent gets: it notified "Run failed", colored the row red, and kept the task in
  DELEGATED WORK forever. The gate also skipped any task a runner was driving, so a PR
  closed mid-run was never seen and the agent kept working on a PR that was gone.
- **Decision.** Add the state `PR_CLOSED`. The gate watches every task with a PR, not
  only the ones waiting on GitHub, and fetches the PR for each; CI is still fetched
  only for a gated task or one coming back from `PR_CLOSED`. A PR closed without
  merging records the outcome `pr_closed` with the summary "PR #N closed without
  merging; the branch and worktree are kept", and only then is the runner stopped:
  `existing` refuses every runner write to a `PR_CLOSED` task, so the stop that
  follows is never saved as a failed or cancelled run and the developer is never told
  one failed. The pid comes from that locked write, not from the list that preceded
  it, and only the pid foreman stopped is cleared, so a run started since keeps its
  runner. A runner that will not stop keeps its pid and warns; the stop is driven by
  the record's pid, so every refresh tries again. `PR_CLOSED` is not in `attention`, so
  the task leaves DELEGATED WORK in the dashboard and in `foreman status`. Nothing
  notifies the developer, who closed the PR. `foreman start` and `foreman clean
  <issue>` accept the state, as they accept FAILED, and a start from `PR_CLOSED` drops
  the closed PR's number, which would otherwise have the gate end the new run.
  A task whose runner is still alive is startable and cleanable by neither: the
  retained pid means that runner still owns the worktree and the task file, and a
  start would both share them with a second runner and lift the `PR_CLOSED` state
  that holds the first one off.
  `PR_CLOSED` also survives the issue reconciliation that follows in the same refresh:
  a closed issue turns a delegated task DONE, and DONE is cleaned automatically, so
  without that the worktree and branch this decision promises would be removed and the
  gate would stop watching for the PR to reopen.
- **Consequence.** Closing a PR is how a developer calls off delegated work, and it
  reads as that rather than as a failure. The cost is one `gh pr view` per active task
  with a PR, per refresh. The task file, worktree, and branch survive, so the work can
  be resumed or cleaned on the developer's terms.

## D108. A reopened PR returns the task to delegated work

- Date: 2026-10-01. Issue midsoln/foreman#5. Requested by the user.
- **Context.** D107 leaves a task in `PR_CLOSED`. A developer who reopens the PR means
  to carry on, and foreman had no path back.
- **Decision.** The gate reads the CI of a reopened PR and derives the state from the
  review it already has: READY_FOR_HUMAN when the latest review is clean, covers that
  PR, names the head GitHub reports now, and CI has finished; REVIEWING otherwise. The
  head must match explicitly, not merely fail to differ: a review from before heads
  were recorded (D63) has none, and cannot be shown to cover the reopened PR. No agent
  is started. REVIEWING with a clean review is what the existing gate promotes once CI
  finishes, so the two paths converge instead of duplicating the rule.
- **Consequence.** Reopening is enough to get the task back in the list. Work that was
  not finished when the PR closed waits in REVIEWING for `foreman start`, rather than
  foreman spending an agent the developer did not ask for.

## D109 (amends D104). Dashboard rows keep the terminal's foreground

- Date: 2026-10-01. Issue midsoln/foreman#5. Requested by the user.
- **Context.** D104 colored whole task rows by state. On a light terminal the greens and
  yellows washed out, and under the selected row's reverse video a colored row became a
  colored background with background-colored text, which could not be read at all. The
  colored span also closed with SGR 0, which ended the reverse video mid-line.
- **Decision.** Row text carries no color. A row names the one word to accent, its
  state, and `highlight` wraps that word after the line is truncated and padded, so the
  column arithmetic (D101) is untouched and a truncated state is simply left plain. A
  selected row is drawn from the plain line in reverse video alone. `colored` ends with
  SGR 39 and `dimmed` with SGR 22, so neither clears another attribute. Left and Right
  join `h` and `l` as pane keys.
- **Consequence.** Color now means heading, divider, message, or task state. The
  selection reads on a light theme and a dark one, because reverse video over the
  default foreground is readable by construction.

## D110. mentions and refresh are settings

- Date: 2026-10-01. Issue midsoln/foreman#5. Requested by the user.
- **Context.** The inbox always showed items found only because they mention the
  developer, which is the noisiest of the four searches, and the dashboard's GitHub
  refresh was a 60 second constant.
- **Decision.** `mentions` (default true) and `refresh` (default 60 seconds, minimum 5)
  join the config. `mentions: false` filters the finished report, dropping entries whose
  only reason is `mentioned`, rather than dropping the searches: the snapshots keep
  every reason, so "no longer mentions you" still works and the items return as soon as
  the setting does. A `refresh` below 5 fails `config.Load` with the file and the
  minimum. Local task and log rereads stay at one second, and `r` still refreshes.
- **Consequence.** The quiet inbox costs nothing in accuracy, and the refresh rate is
  the developer's call within a floor that keeps one refresh from overlapping the next.

## D111 (amends D107 and D108). Check PR state before reaping its runner

- Date: 2026-10-01. Issue midsoln/foreman#5. Review findings.
- **Context.** A runner can exit before the next GitHub refresh. Reaping it during a
  local task reread would record FAILED before the gate could see that its PR closed.
  A runner that resisted a stop could also keep writing after its PR reopened.
- **Decision.** Local rereads defer reaping tasks with a PR. `status` and `inbox`
  identify dead runners, check PR state, then reap only tasks the gate left running.
  Before a `PR_CLOSED` task returns to active work, the gate stops its retained runner
  or clears its stale pid. A failed stop leaves the task closed for another refresh.
- **Consequence.** A dead runner with a PR can remain WORKING until the next GitHub
  refresh. A closed PR records `PR_CLOSED` without a failure notification, and a
  reopened PR cannot release an old live runner into the active task.

## D112. Codex can write the worktree's git directory

- Date: 2026-10-01. Requested by the user after midsoln/foreman#5 failed when Codex
  couldn't commit its fixes. D107 to D111 are taken on that issue's branch.
- **Context.** Foreman's worktrees are linked to a clone under `repositories/`. Their
  git data lives in that clone's `.git`, outside the worktree, and Codex's
  workspace-write sandbox only writes inside the worktree (plus the temp dirs). Codex
  could edit and test but not commit, so the task failed. The probe missed this
  because its clone was under `$TMPDIR`. Claude's Bash tool runs outside a sandbox, so
  it was not affected.
- **Decision.** The Codex adapter reads the worktree's `.git` file and `commondir` to
  find the clone's `.git` and adds it with
  `-c sandbox_workspace_write.writable_roots=[...]` on `exec` and `exec resume`. A
  plain checkout adds nothing. The reviewer stays read-only. The probe checks the
  commit with the temp dirs excluded: it must fail without the root and succeed with
  it, on `exec` and `exec resume`.
- **Consequence.** Codex can commit and push from foreman's worktrees. It can write to
  the whole clone's `.git`, not just this worktree's entry, which is the same access a
  plain checkout gives. Network access for `gh` and `git push` is unchanged (D7).

## D113 (amends D111). Reap a dead runner only once its PR has been read

- Date: 2026-10-01. Issue midsoln/foreman#5. Review finding.
- **Context.** D111 reaped after the gate so a closed PR would be seen first, but it
  reaped every dead runner the pass had found. A failed `GetPullRequest` only warns and
  leaves the task WORKING, so the reap still recorded FAILED and notified. A later
  refresh could correct the state, never the notification.
- **Decision.** `gate` returns the keys whose PR it read. `reapable` keeps a dead runner
  only when its task had no PR, which takes no GitHub call to judge, or when the gate
  read that PR this pass. An unread task keeps its state for the next refresh.
- **Consequence.** GitHub being down delays a FAILED verdict instead of risking a wrong
  one. The condition is positive, so a future early return in `gate` leaves a task
  unread, and unread means unreaped.

## D114 (amends D110). Judge the gone note by the reasons the task stored

- Date: 2026-10-01. Issue midsoln/foreman#5. Review finding.
- **Context.** An item that leaves GitHub gets a CHANGED note built from the task
  record, not from a search result, so the entry carries no reasons. `hideMentionOnly`
  keeps an entry with no `mentioned` reason, so with `mentions: false` a mention-only
  item still announced itself on the way out.
- **Decision.** `Inbox` reads the stored reasons before `stopTracking` clears them and
  drops the note when the setting is off and those reasons were a mention alone. The
  check sits where the entry is built, not in `hideMentionOnly`, because the entry's
  empty reasons are what tell `render.InboxActivity` to always show a gone note.
  Filling them in would hide the note from the picker even with mentions on.
- **Consequence.** The setting covers the item's whole life, including its last note.
  The record still stops being checked either way: the setting hides the note, not the
  bookkeeping that ends the refreshes.

## D115 (amends D110 and D114). Resolve a tracked fetch's reasons before filtering

- Date: 2026-10-01. Issue midsoln/foreman#5. Review finding.
- **Context.** A search that hits its 1000 result limit can leave a tracked item out.
  `fetchTracked` then retrieves it with the synthetic `tracked` reason alone, and new
  activity puts it in CHANGED. `mentionOnly` saw no `mentioned` reason, so with
  `mentions: false` the item still printed.
- **Decision.** `storedReasons` snapshots each record's involvement before `Reconcile`,
  which calls `observe` and rewrites those records in place. `involvement` resolves an
  entry carrying `tracked` alone to that snapshot, and `hideMentionOnly` filters on the
  result. This covers NEW, CHANGED, and the dashboard's `Current`.
- **Consequence.** The setting holds when the searches are incomplete, which is when a
  mention-only item is most likely to arrive as a tracked fetch. An item foreman has
  never stored has no snapshot, so it keeps the reasons the search gave it.

## D116. Keep both worktree and issue decisions after the merge

- Date: 2026-10-01. PR midsoln/foreman#6 merge resolution.
- **Context.** The issue branch and main independently assigned D112 to different
  decisions. Both changes are needed and their references must stay unambiguous.
- **Decision.** Keep main's D112 for the Codex worktree fix and renumber the issue
  branch's later review decisions D113 to D115. Preserve both sets of changes.
- **Consequence.** Code comments and progress references identify the intended
  decision after PR #6 merges.

## D117 (amends D90). Start implementation with a fresh agent session

- Date: 2026-10-01. Foreman token usage investigation.
- **Context.** Planning and implementation shared one Claude session. On issue #7,
  the implementation inherited about 214,000 tokens of planning context and the
  session grew to about 386,000 tokens. Each later turn read that history again.
- **Decision.** After the runner posts the four plan comments, it starts a new
  implementer session from the task record and comment links. Planning questions
  still resume the planning session; later decisions and review findings resume
  the implementation session. The plan and implement stage files ask for a few
  cohesive milestones and focused checks during editing, with full validation
  after the code is complete.
- **Consequence.** Implementation no longer carries planning tool output in its
  context. The task file records the new session ID for later resumes. The local
  configuration trials Sonnet for Claude work and lowers the implementer budget
  from $100 to $25 per invocation.
