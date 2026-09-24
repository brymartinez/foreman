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
  The branch is `agent/<WorkName>`. The worktree is
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
