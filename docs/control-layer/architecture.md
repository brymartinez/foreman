# Architecture

```
cmd/foreman           wiring: build adapters, call app, render output
internal/cli          argv -> Command; no I/O beyond parsing
internal/config       tasks directory resolution
internal/render       terminal formatting of app results
internal/tui          the dashboard: pure Model and Frame, one terminal loop
internal/app          orchestration + ports.go (the interfaces below)
  ├── github          GitHubClient and RepositoryManager over `gh`
  ├── store           TaskStore over Markdown + YAML frontmatter
  ├── worktree        WorktreeManager over `git worktree`
  ├── agent           outcome contract, prompt, claude.go / codex.go adapters
  ├── runner          spawns and checks the detached `foreman __run` process
  └── stages          resolves ~/.config/foreman/stages/<stage>.md + <repo>/.foreman/<stage>.md
internal/task         the durable model: Record, State, Delegation, Decision
```

`app` depends only on the interfaces in `internal/app/ports.go`. Presentation stays in
`cmd/foreman`, and process handling stays in `runner` and the adapters.

## Execution model

Every agent interaction is one headless turn that runs to completion and returns a typed
`agent.Outcome`. There are no interactive sessions and no terminal scraping.

```
foreman start 825 --agent claude          (run from a checkout of the repo)
  ├─ gh: repo + default branch of the checkout, fetch issue
  ├─ git: ensure worktree <root>/<owner>-<repo>/825-<slug> on branch <type>/825-<slug>
  ├─ store: task file, state WORKING, session id
  └─ spawn detached `foreman __run owner/repo#825` (pid + log recorded), exit

runner (one process per task, exits when the task leaves WORKING/REVIEWING):
  if the gate flagged a merge conflict:
    turn = implementer.Resume(session, merge origin/<base>, resolve, validate, push)
    pr_opened, same PR, head moved, new head contains the base head, every
                    reported check passed -> REVIEWING, then the review below
    blocked      -> NEEDS_DECISION with the agent's questions, exit
    anything else -> NEEDS_DECISION, "merge again" or "I fixed it", exit
    (a repair runner that cannot spawn or dies is also NEEDS_DECISION)
  if the PR has comments the agent has not answered (resumed from REVIEWED):
    turn = implementer.Resume(session, each comment, where it is, and how to reply)
    head unchanged -> mark them answered, keep CI, READY_FOR_HUMAN
    head moved     -> mark them answered, review the new head like any change
  if the verified PR head has no review (crash mid-review, or new commits):
    re-verify the PR, record its head, skip to the review below
  turn = implementer.Start(worktree, prompt)          # later: Resume(session, msg)
  until every planning document is on the issue (and no PR exists), the turn plans:
    planned      -> gh: post analysis, PRD, design, and plan as four issue comments
                    (edit the earlier comments on a re-plan), record their ids and URLs,
                    then turn = implementer.Start(new session, posted plan links)
  switch turn.Outcome.Status
    blocked      -> save each of Questions as a decision, state NEEDS_DECISION, exit
    failed       -> state FAILED, exit
    pr_opened    -> (the agent pushed and ran gh pr create) gh: confirm PR exists
                    record PR and its head commit, state REVIEWING
                    before every review: git fetch base and branch, then
                      git merge-tree origin/<branch> origin/<base>
                      clean    -> review; the branch is left as is
                      conflict -> implementer.Resume(session, merge the base, never
                                  rebase or force-push, push); recheck; after two
                                  turns still conflicting -> NEEDS_DECISION, exit
                    review = reviewer.Review(disposable read-only session,
                                             PR description + PR comments + git diff + earlier rounds)
                    clean    -> record review and the head it covered; read the PR's comments,
                and answer anything new before handing over; stay REVIEWING, exit
                    findings -> WORKING; implementer.Resume(session, findings); agent pushes
                                fixes and reports pr_opened again; re-verify; re-review
                    findings after fix_rounds reviewed fixes since the last answer ->
                                NEEDS_DECISION (A: another round, B: accept remaining), exit

foreman resume 825    -> a REVIEWED task goes back to WORKING with its branch,
                         worktree, PR, and session kept; the runner answers the
                         comments
foreman respond 825   -> answer each waiting question in order; after the last,
                         state WORKING, spawn runner
                         runner: Resume(session, all answers); on ErrNoSession, a fresh
                         Start whose prompt lists all recorded decisions
foreman cancel 825    -> SIGTERM the runner, wait up to 30s for it to exit
                         runner: context cancelled -> agent process group stopped;
                         the failed step records FAILED, outcome cancelled, keeping
                         session, usage, branch, worktree, and PR
foreman               -> the dashboard, until q or ctrl+c:
                         every 1s : app.Tasks re-reads the task files and, when open,
                                    re-tails the selected task's runner log; no GitHub call
                         every `refresh` seconds (60 by default): app.InboxView, the
                           same as `foreman inbox`
                         enter on an inbox issue -> agent, context file, app.Start
                         enter on a REVIEWED issue -> app.Resume, no prompts
                         enter on delegated work -> open that task's log below the lists
                         left/right, h/l -> focus the inbox/delegated work
                         esc -> close the log
                         c on a running task     -> confirm, app.Cancel
                         a -> toggle auto mode, saved in state.json (D144)
                         v -> toggle the inbox between the configured repositories
                           and all of them, this session only, and refresh
                         auto mode, after each successful refresh, finished start,
                           and 1s tick: app.Start with auto.agent for assigned,
                           never delegated issues, oldest first, while fewer than
                           auto.max_concurrent runs are active (D144)

foreman inbox|status  -> runner pid dead while WORKING/REVIEWING -> FAILED
                         readiness gate (gate.go), per task with a PR:
                           PR closed  -> stop the runner, PR_CLOSED
                           PR reopened -> READY_FOR_HUMAN or REVIEWING
                         and then, per idle task with a PR:
                           PR merged -> DONE
                           REVIEWING + clean review, PR head moved -> WORKING, spawn
                             a runner that reviews the new commits
                           REVIEWING + clean review: gh pr checks (read only)
                             pending              -> record, wait
                             finished             -> READY_FOR_HUMAN, ✓ CI or ✗ CI: <checks>
                           READY_FOR_HUMAN: refresh the CI result; flag new commits
                         and then, per task the agents are done with (READY_FOR_HUMAN
                         or REVIEWED, PR open, no live runner):
                           read the PR's comments, drop the agent's own replies
                           any unanswered -> REVIEWED, shown in INBOX
                           none left      -> READY_FOR_HUMAN
                         and then, per READY_FOR_HUMAN task with an open PR:
                           mergeable CONFLICTING, (PR head, base head) never tried ->
                             record the pair, WORKING, spawn a runner
                           UNKNOWN or MERGEABLE -> nothing
                        inbox only, cleanup.on_done: DONE task -> git worktree remove
                           (kept if dirty); branch -D only if its PR merged
foreman clean [<issue>] -> the same cleanup on demand; <issue> may also be FAILED
                           or PR_CLOSED
```

Nothing runs in the background except the runner working on a task. The readiness and
liveness checks happen whenever the user runs a command. Every task write goes through
`App.update`, which posts an alert to the Discord webhook when a task enters
NEEDS_DECISION, FAILED, READY_FOR_HUMAN, or REVIEWED.

## Design rules

These are the constraints the code is built around, with the reason for each and the
alternatives that were rejected. Change one only on purpose, and update this section in
the same commit. The commit message records why it changed.

### Processes

- **No daemon.** Nothing runs between commands except a task's own runner. The gate,
  liveness checks, and cleanup run when `inbox` or `status` runs, or while the
  dashboard is open, which is the same as running `inbox` on a timer. Rejected: a
  daemon, a watcher, or a runner that waits for CI.
- **One detached runner per task.** `start`, `respond`, `resume`, and the gate spawn
  `foreman __run owner/repo#N` with `Setsid`, so closing the terminal doesn't stop it.
  A runner is alive when `kill(pid, 0)` succeeds and `ps` shows ` __run `, which guards
  against pid reuse. A dead runner is reaped only after the gate has read its PR, so a
  GitHub outage delays a FAILED verdict instead of risking a wrong one.
- **The runner owns its log.** `Spawn` passes the log file itself as stdout and stderr,
  and the runner timestamps its own lines. A writer in the launcher needs a pipe, which
  dies when `start` exits and kills the runner on its next write.
- **Stopping is clean.** Agents run in their own process group, which gets SIGTERM on
  cancel with a 5-second `WaitDelay`. `foreman cancel` waits up to 30 seconds and never
  SIGKILLs the runner, because that would leave the agent running. Any SIGTERM, SIGINT,
  or SIGHUP counts as a cancel and records FAILED with outcome `cancelled`.
- **A removed task file stops its runner.** Runner writes go through `existing`, which
  saves nothing once the file or its delegation is gone. Deleting the file is a way to
  drop a task.

### Runner

- **The runner is a step loop.** `RunTask` claims the task, runs any pending conflict
  repair, reads waiting comments, and hands a `taskRun` to its loop.
  `firstStep` picks the first step from the record alone. Each step is one method that
  returns the next:

  | Step | Does | Next |
  |---|---|---|
  | `stepAnswer` | a turn on the waiting comments | stopped or blocked: done. Head unchanged: handover. Else review |
  | `stepPickUpReview` | re-verify the PR, record its head | PR not open: implement. Else review |
  | `stepImplement` | a plan, implement, or fix turn | plan posted: implement. Not REVIEWING: done. Else review |
  | `stepReview` | conflict check, review, escalate | findings: implement. Clean: handover. Else done |
  | `stepHandover` | read comments that arrived during the run | comments: answer. Else done |

  Every implementer turn goes through `runTurn` (turn, then the PR check) and
  `finishTurn` (one write through the uncancelled context, the cancel outcome, and
  usage). Each caller passes its own mapping: `applyTurn`, `applyFeedbackTurn`, or the
  conflict repair's. The code is in `internal/app/run.go` and `internal/app/turn.go`.
- **Idempotent steps.** Creating the worktree and the task file each check whether the
  thing already exists, and so does the agent's PR step (`gh pr view` before
  `gh pr create`). That lets a crashed runner be restarted from the
  task file.

### Task files

- **One Markdown file per `repo + number`**, named `<owner>-<repo>-<number>.md`, so the
  tasks directory can be an Obsidian vault. Frontmatter holds machine state. Saves
  replace only the keys foreman owns (snake_case) through a `yaml.Node`, so user keys
  and comments keep their place. The body is the user's: foreman's only edit is
  appending a line to `## Decisions` when an answer is recorded.
- **Writes are locked.** The runner, `inbox`, and `status` can write one file at once.
  `TaskStore.Update` takes a flock on `<tasks_dir>/.foreman.lock`, re-reads, applies,
  and saves atomically. `inbox` merges in only fields that come from GitHub.
- **Every write goes through `App.update`**, which sends the alert when the state
  enters NEEDS_DECISION, FAILED, READY_FOR_HUMAN, or REVIEWED. Call sites never notify
  on their own.
- **States mean one thing each.** FAILED is stopped and restartable, including a
  cancelled run; a separate CANCELLED state was rejected. A PR closed without merging
  is `PR_CLOSED`, not FAILED: it is how the developer calls off work, so it sends no
  alert, keeps the branch and worktree, and returns to READY_FOR_HUMAN or REVIEWING if
  the PR is reopened. The runner can't write to a `PR_CLOSED` task, so the stop that
  follows is never saved as a failure.
- **Names are computed once.** `task.WorkName` is `<issue>-<slug>` (at most 5 words and
  40 characters, conventional prefix removed). The branch is `<type>/<workName>`, with
  the type from `--type`, the title prefix, the labels, then `feat`. Both are stored at
  `start`, so renaming the issue renames nothing. Old `agent/…` branches still work.
- **Issue references** accept `825`, `#825`, or `owner/repo#825`. A bare number that
  matches more than one tracked repo is an error that lists the choices.

### Agents

- **Claude and Codex are interchangeable.** Every feature works with either as the
  implementer and the other as the reviewer.
- **Headless turns with a structured outcome.** Each turn ends in a final message that
  matches `internal/agent/outcome.schema.json`: `planned`, `pr_opened`, `blocked` with
  questions, or `failed`. `agent.ParseOutcome` is the only way agent output enters the
  program, and it rejects questions with fewer than two options or duplicate labels.
  Rejected: interactive sessions with terminal scraping.
- **Permissions.** The dangerous bypass flags are never used. Claude runs with
  `acceptEdits` and the fixed `Bash,WebFetch,WebSearch` tool list; the list is not
  configurable, because removing `Bash` broke every task. Codex runs `workspace-write`
  with network access, plus the clone's `.git` as a writable root, because foreman's
  worktrees keep their git data outside the worktree.
- **The agent pushes and opens the PR.** Foreman then checks with `gh` that the PR is
  open, in the task's repo, from the task branch, against the task's base, and records
  the verified number and head. A PR the developer retargets keeps its new base.
- **Resume first, rebuild only when the session is gone.** Answers, findings, and
  comments resume the recorded session. Only `agent.ErrNoSession` falls back to a fresh
  turn whose prompt carries the durable state; any other resume error fails the task.
- **Planning is its own session.** Until the four planning documents are posted, and
  while there is no PR, the turn plans. The runner, not the agent, posts them as issue
  comments with a `<!-- foreman:<doc> -->` marker and edits them on a re-plan. There is
  no approval gate. Implementation then starts a fresh session, so it doesn't carry the
  planning context (about 200k tokens on one task).
- **Models and effort are set per step:** `plan_model`, `model` (implementation, fixes,
  feedback, merges, PR creation), and `review_model`; Codex also takes reasoning effort
  per step. Each turn logs an `agent:model:step` line.
- **Dollar budgets cap Claude only.** Codex reports no cost and its tokens arrive only
  when a turn ends, so foreman records Codex tokens and never invents a price.
- **Validation is self-reported.** Foreman records what the agent ran and doesn't re-run
  it, because that would run untrusted commands outside the sandbox. CI is the
  independent check.

### Stage instructions

- **The developer's workflow lives in stage files**, not in Go. `plan`, `implement`,
  `review`, and `pr` each resolve `stages/<stage>.md` next to the config file, then the
  committed `.foreman/<stage>.md` in the task's worktree. Foreman never parses them and
  never reads CLAUDE.md or AGENTS.md.
- **The global files are required.** Every command except `config` fails naming the
  missing files. `make config` installs the defaults from `defaults/stages/` and never
  overwrites an installed file, so a changed default reaches existing users only by hand.
- **Prompts hold protocol, not workflow:** the role, the issue, paths and branch, push
  and `gh pr` commands, `Closes #N`, the question format, the JSON outcome, and
  precedence (task context and decisions beat stage files). The one built-in rule is
  the test-first `## Tests` section, so every repo gets it with or without stage files.
- **pr.md goes into the implementation turn,** because the agent opens the PR there.
  The review prompt never gets it.

### Review

- **The reviewer is the competing agent,** in a disposable session (`--no-session-persistence`
  / `--ephemeral`) that never sees the implementer's session. It is strictly read-only:
  Claude gets only `Read, Grep, Glob` and read-only git, and Codex the read-only
  sandbox. Letting it run tests was rejected, because Claude's Bash has no sandbox.
- **The PR description is the only requirements.** The prompt carries the description,
  the PR comments, `git diff origin/<base>...HEAD`, and earlier rounds, but not the
  issue, PRD, decisions, or validation claims, so the review stays independent. A blank
  description stops the review.
- **A review covers one PR head.** Each review records the head it read. A REVIEWING
  task whose head moved is reviewed again; new commits on a READY_FOR_HUMAN PR are only
  flagged, because that PR is the developer's now. A crash mid-review restarts at the
  review, not the implementation.
- **Conflicts are checked before every review** with `git merge-tree` on
  `origin/<branch>` and `origin/<base>`, which writes nothing (needs git 2.38+).
  GitHub's `mergeable` was rejected for this: it reads UNKNOWN after a push and names no
  files. The implementer merges the base, never rebases or force-pushes. After two
  turns that still conflict, the developer is asked.
- **Two round settings.** `review_rounds` (3) is how many passes the reviewer makes over
  a PR. `fix_rounds` (3) is how many reviewed fixes run before the developer is asked to
  accept the rest or go again. The cap is checked after a review, so no fix reaches the
  developer unreviewed, and answering resets it.

### CI and handover

- **CI belongs to the repo owners.** Foreman reads `gh pr checks --json` and never runs,
  configures, or fixes CI. Every check gates, not just required ones; failing beats
  pending beats passing, and no checks passes as `✓ CI (no checks)`. A clean review
  with finished CI hands over even when CI failed, with the failing checks listed.
- **PR feedback.** The gate reads the three comment threads of each handed-over PR on
  every refresh. A comment is identified by kind, id, and a digest of its text, so an
  edit counts again; the text itself is never stored. The agent prefixes its replies
  with `<!-- foreman:reply -->` and foreman drops those, because replies post under the
  developer's own login. A turn that leaves the head unchanged only replied and goes
  straight back; one that moved it is reviewed. A run answers new comments at most
  three times before handing over. `foreman resume` keeps the branch, PR, and session;
  `start` rebuilds them.
- **A handed-over PR that conflicts is repaired once per (PR head, base head) pair.**
  Every attempted pair is kept, so none is tried twice. A repair is accepted only if
  the head moved, contains the base head, and every reported check passed; anything
  else asks the developer.
- **Cleanup never loses work.** `git worktree remove` without `--force`, so a dirty
  worktree stays. The local branch is deleted (`-D`, for squash merges) only when its PR
  merged. `inbox` cleans DONE tasks automatically unless `cleanup.on_done` is off.

### Inbox

- **Four searches,** open items only: assigned issues, mentioning issues, mentioning
  PRs, and review requests, each up to gh's maximum of 1000. A search that hits 1000 is
  reported as truncated, and lost reasons ("no longer assigned to you") are judged only
  when every search was complete.
- **INBOX is work to start:** issues never delegated, plus delegated ones in FAILED or
  REVIEWED. Work an agent holds stays in DELEGATED WORK, so an issue is never delegated
  twice. `mentions: false` hides mention-only items but never a delegated task that
  needs the developer.
- **Repository filter.** A nonempty `include_repositories` shows only those
  repositories and overrides `exclude_repositories`; otherwise the exclude list hides
  its repositories. `App.InboxView` drops filtered items before `Reconcile`, so the
  configured view writes no records for them, and `fetchTracked` skips tracked items
  outside the view. `v` in the dashboard and the picker passes `allRepos`, which
  bypasses both lists for the session; the searches, involvement reasons, and
  `mentions` are the same in either view. `status` and DELEGATED WORK read the task
  store, so tasks in any repository stay reachable.
- **Lookup failures warn and retry** next refresh without blocking other items. A 404
  or 410 on an undelegated item is reported once and then no longer checked.
- **Delegation from the inbox doesn't need a checkout:** foreman keeps a clone under
  `repositories_dir` and creates worktrees from it. `foreman start` stays bound to the
  current checkout.

### Dashboard

- **Built on `golang.org/x/term` alone.** `Model.Key` is a pure transition and `Frame` a
  pure function of the model, so nearly all of the UI is unit tested; only `Run` touches
  the terminal. Bubble Tea was rejected as ten modules in a project with one dependency.
- **It never blocks or misdraws:** the terminal check runs before config loads, so a
  piped `foreman` prints usage and exits 0; control characters in dynamic text become
  spaces; widths are counted in terminal columns; GitHub refreshes run off the input
  goroutine; quitting waits for a `Start` or `Cancel` in flight.

### Notifications

- **Alerts go to a Discord webhook** (`notifications.webhook_url`). The URL is a secret:
  `config --show` prints only whether it is set, and errors never contain it. Delivery
  errors go to `<log_dir>/notifications.log` and never change a task or an exit code.
  Native desktop notifications were dropped because they never reach a phone.

### Tooling

- **Go, standard library first.** The dependencies are `go.yaml.in/yaml/v3` (task
  files keep comments and key order) and `golang.org/x/term` (raw mode and terminal
  size). Add another only with the user's approval, and list it here.
- **Releases** are `scripts/release.sh` run by a tag workflow: static binaries for
  darwin and linux on arm64 and amd64, no Windows (the runner needs `setsid`), not
  signed. Local builds report `0.1.0-dev (<revision>)`, with `-dirty`
  when the tree had changes.
- **`FOREMAN_CONFIG`** points at another config file; `scripts/e2e.sh` uses it to run
  the built binary against a local origin, a stand-in `gh`, and stub agents.
