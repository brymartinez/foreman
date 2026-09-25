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

## D15. Inbox semantics

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

## D53. Inspecting stage instructions from the CLI

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
