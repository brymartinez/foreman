# Architecture

```
cmd/foreman           wiring: build adapters, open the dashboard or run a task
internal/cli          argv -> Command (launch flags and __run); no I/O beyond parsing
internal/config       tasks directory resolution
internal/routing      routing.yaml: profiles, rules, default, and their validation
internal/render       terminal formatting of app results
internal/tui          the dashboard: pure Model and Frame, one terminal loop
internal/app          orchestration + ports.go (the interfaces below)
  ├── github          GitHubClient and RepositoryManager over `gh`
  ├── store           TaskStore over Markdown + YAML frontmatter
  ├── worktree        WorktreeManager over `git worktree`
  ├── agent           outcome contract, prompt, claude.go / codex.go adapters, selector
  ├── quota           QuotaChecker over the optional `quota-axi`
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
app.Start, from the dashboard (enter on an inbox issue, or auto mode)
  ├─ gh: repo + default branch of the managed clone, fetch issue
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
  while implementing before a PR exists, a turn may return checkpoint:
    verify the expected branch, a clean worktree, and a new descendant commit;
    save the commit and one short next objective, then Start a fresh session
    from the issue, plan links, decisions, committed code, and that objective
    (no prior transcript); after eight checkpoints, fail instead of looping
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

app.Resume            -> a REVIEWED task goes back to WORKING with its branch,
                         worktree, PR, and session kept; the runner answers the
                         comments
app.Respond           -> answer each waiting question in order; after the last,
                         state WORKING, spawn runner
                         runner: Resume(session, all answers); on ErrNoSession, a fresh
                         Start whose prompt lists all recorded decisions
app.Cancel            -> SIGTERM the runner, wait up to 30s for it to exit
                         runner: context cancelled -> agent process group stopped;
                         the failed step records FAILED, outcome cancelled, keeping
                         session, usage, branch, worktree, and PR
foreman [-a|--auto|--no-auto]
                      -> the dashboard, until q or ctrl+c. A launch flag sets auto
                         mode for this session and saves nothing.
                         every 1s : app.Tasks re-reads the task files and, when open,
                                    reads the bytes the runner added to the selected
                                    task's log; no GitHub call
                         every `refresh` seconds (60 by default), and on r:
                           app.InboxView, the refresh below
                         enter on an inbox issue -> agent, context file, app.Start
                           3 (routing on, never delegated) -> context file,
                             app.Route, show the profile and reason; enter ->
                             app.Start with the profile, esc -> agent choice
                         enter on a REVIEWED issue -> app.Resume, no prompts
                         enter on delegated work -> open that task's log below the lists
                         enter on a NEEDS_DECISION task -> the oldest unanswered
                           question below the lists; enter -> app.Respond with the
                           typed answer, then the next question; after the last,
                           the task's log. esc -> close; recorded answers stay
                         an open log whose task needs a decision -> the question
                         left/right, h/l -> focus the inbox/delegated work
                         esc -> close the log
                         s -> switch the open log and the task's state (render.Show)
                         R -> switch the open log between cleaned and raw lines
                         u, pgup, ctrl-u past the oldest loaded line -> read an
                           older chunk of the log file; g -> read back to its start
                         ? -> the keys and the version; any key closes it
                         c on a running task     -> confirm, app.Cancel
                         a -> toggle auto mode, saved in state.json (D144)
                         v -> toggle the inbox between the configured repositories
                           and all of them, this session only, and refresh
                         auto mode, after each successful refresh, finished start,
                           and 1s tick: app.Start with auto.agent for assigned,
                           never delegated issues, oldest first, while fewer than
                           auto.max_concurrent runs are active (D144). With routing
                           on: app.Route, then app.Start with the profile, no
                           confirmation; a wait holds the issue until the next refresh

app.InboxView         -> runner pid dead while WORKING/REVIEWING -> FAILED
                         readiness gate (gate.go), per task with a PR:
                           PR closed  -> stop the runner, PR_CLOSED
                           PR reopened -> READY_FOR_HUMAN or REVIEWING
                         and then, per stopped task with a PR, including FAILED:
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
                         gh search, then a fetch of tracked items no search returned
                         cleanup.on_done, per task with a worktree and no live runner:
                           DONE, PR_CLOSED, or issue closed -> git worktree remove
                           (kept if dirty, retried next refresh); branch -D only if
                           its PR merged
                         Reconcile, per item: issue closed, no runner -> DONE, including
                           FAILED and PR_CLOSED; a DONE task stays DONE if it reopens

foreman -c            -> open the config in $VISUAL, $EDITOR, or vi, before the config
                         loads, so an invalid file can be fixed; check it after
```

Nothing runs in the background except the runner working on a task. The readiness and
liveness checks happen on each dashboard refresh. Every task write goes through
`App.update`, which posts an alert to the Discord webhook when a task enters
NEEDS_DECISION, FAILED, READY_FOR_HUMAN, or REVIEWED.

## Design rules

These are the constraints the code is built around, with the reason for each and the
alternatives that were rejected. Change one only on purpose, and update this section in
the same commit. The commit message records why it changed.

### Processes

- **No daemon.** Nothing runs while the dashboard is closed except a task's own runner.
  The gate, liveness checks, and cleanup run on each dashboard refresh. Rejected: a
  daemon, a watcher, or a runner that waits for CI.
- **The dashboard is the only interface.** Bare `foreman` opens it; `-a`/`--auto` and
  `--no-auto` set auto mode for the session, and `-c` edits the config. The only other
  verb is the hidden `__run`. Task subcommands were dropped because they duplicated the
  dashboard, and a piped `foreman` still prints usage.
- **One detached runner per task.** `App.Start`, `App.Respond`, `App.Resume`, and the gate spawn
  `foreman __run owner/repo#N` with `Setsid`, so closing the terminal doesn't stop it.
  A runner is alive when `kill(pid, 0)` succeeds and `ps` shows ` __run `, which guards
  against pid reuse. A dead runner is reaped only after the gate has read its PR, so a
  GitHub outage delays a FAILED verdict instead of risking a wrong one.
- **The runner owns its log.** `Spawn` passes the log file itself as stdout and stderr,
  and the runner timestamps its own lines. A writer in the launcher needs a pipe, which
  dies when the dashboard exits and kills the runner on its next write.
- **Stopping is clean.** Agents run in their own process group, which gets SIGTERM on
  cancel with a 5-second `WaitDelay`. `App.Cancel` waits up to 30 seconds and never
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
  | `stepImplement` | a plan, implement, or fix turn | plan posted or checkpoint verified: implement. Not REVIEWING: done. Else review |
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
- **Writes are locked.** The runner and the dashboard can write one file at once.
  `TaskStore.Update` takes a flock on `<tasks_dir>/.foreman.lock`, re-reads, applies,
  and saves atomically. The inbox refresh merges in only fields that come from GitHub.
- **Every write goes through `App.update`**, which sends the alert when the state
  enters NEEDS_DECISION, FAILED, READY_FOR_HUMAN, or REVIEWED. Call sites never notify
  on their own.
- **States mean one thing each.** FAILED is stopped and restartable, including a
  cancelled run; a separate CANCELLED state was rejected. A PR closed without merging
  is `PR_CLOSED`, not FAILED: it is how the developer calls off work, so it sends no
  alert, keeps the branch, and returns to READY_FOR_HUMAN or REVIEWING if the PR is
  reopened. A closed issue takes precedence and makes the task DONE. The runner can't write to a `PR_CLOSED` task, so the stop that
  follows is never saved as a failure.
- **Names are computed once.** `task.WorkName` is `<issue>-<slug>` (at most 5 words and
  40 characters, conventional prefix removed). The branch is `<type>/<workName>`, with
  the type from the title prefix, the labels, then `feat`. Both are stored at the first
  start, so renaming the issue renames nothing. Old `agent/…` branches still work.

### Agents

- **Claude and Codex are interchangeable.** Every feature works with either as the
  implementer and the other as the reviewer.
- **Headless turns with a structured outcome.** Each turn ends in a final message that
  matches `internal/agent/outcome.schema.json`: `planned`, `checkpoint`, `pr_opened`, `blocked` with
  questions, or `failed`. `agent.ParseOutcome` is the only way an implementer's output
  enters the program, and it rejects questions with fewer than two options or duplicate
  labels. The reviewer and the routing selector have their own schemas and parsers,
  `agent.ParseReview` and `agent.ParseSelection`.
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
- **Implementation checkpoints reset context within the implement phase.** Before a
  PR exists, an implementer may finish one tested part with `checkpoint`, one short
  next objective, and reported passing validation. Foreman accepts it only when the
  worktree is clean on the task branch and HEAD advances from the last accepted
  commit. It saves the commit, objective, and usage together, clears the old session,
  and starts a fresh one. The new prompt includes the title and issue URL, plan links,
  recorded decisions, developer context and stage instructions, commit, and objective;
  it omits the issue body and previous transcript. A retry keeps the objective only
  when the worktree still points at the accepted commit. Checkpoints are limited to
  eight per task. Planning, PR feedback, merge repair, and review fixes do not
  checkpoint. Validation remains agent reported; Foreman verifies Git state.
- **Models and effort are set per step:** `plan_model`, `model` (implementation, fixes,
  feedback, merges, PR creation), and `review_model`; Codex also takes reasoning effort
  per step. Each turn logs an `agent:model:step` line. A routed task's stored profile
  replaces the plan and implementation settings for its turns; review settings never
  change.

### Routing

- **`routing.yaml` sits next to `config.yaml`** and holds the selector, named
  profiles, ordered rules (`when`, `profiles`, `why`), and a `default`. A profile sets
  `agent` and may set `plan_model`, `model`, and, for Codex, the two efforts; omitted
  fields inherit `claude.*` or `codex.*`. Budgets, notifications, repository filters,
  cleanup, auto mode, and review settings stay in `config.yaml`. No file means routing
  is off. A file that fails to load is an error on every route, and nothing that needed
  a route starts; the rest of the dashboard still works.
- **Only starts of never-delegated issues are routed:** auto mode, and `3` in the agent
  choice. `1` and `2` override it.
- **Fit comes first.** A selector turn (Claude with `claude.plan_model` unless
  `selector` says otherwise) reads the repository, title, labels, body (6000 runes), up
  to 10 of the newest non-foreman comments (1000 runes each, 5000 in all), and the
  context file (4000 runes), and names a rule or 0. Each input has its own budget, and
  `routing.yaml` rejects rules longer than 2500 runes, so the prompt stays under 20000
  runes without cutting any input short. A failed read of the issue or its comments is
  a route error, so the selector never chooses from part of the issue. It runs with no tools in a scratch
  directory. A selector failure, an unknown rule, or 0 uses `default`, and the reason
  says so.
- **Quota only removes candidates.** Candidates are the chosen rule's profiles in the
  order listed, or `default` alone. `quota-axi` removes those whose agent it confirms
  exhausted: a fresh `all_models` reading at 0%. Another profile runs only when the rule
  lists it. With no candidate left, nothing starts: the dashboard shows why, and auto
  mode keeps the selection and routes again after the next GitHub refresh. When quota
  is unknown (no `quota-axi`, a failure, or no reading) the default runs if it is a
  candidate, else the first, and the reason says `quota unknown`. Ranking by percent
  left was rejected: it would override the cost order the rule sets.
- **The choice is stored resolved** on the delegation (`profile`, `route_reason`), so
  editing either file never changes a running task. The runner writes
  `route:<profile>:<agent>:<reason>` once, before the run's first `agent:model:step`
  line (`route_logged`). A restart with the same agent keeps the profile; another agent
  drops it. The reviewer stays the competing agent with its review settings.
- Rejected: routing inside `app.Start` (a manual start could not confirm first) and in
  the runner (the task is already WORKING, so it could neither wait nor change agents).
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
- **The global files are required.** The dashboard and the runner fail naming the
  missing files; `foreman -c` and a piped `foreman` don't check them. `make config` (`scripts/install-stages.sh`) installs the defaults from
  `defaults/stages/` and never overwrites an installed file. It names each installed
  file that differs from its default and prints the `cp` command that replaces it.
- **Prompts hold protocol, not workflow:** the role, the issue, paths and branch, push
  and `gh pr` commands, `Closes #N`, the question format, the JSON outcome, and
  precedence (task context and decisions beat stage files). Two rules are built in, so
  every repo gets them with or without stage files: the test-first `## Tests` section,
  and the `## Working silently` section. A planning turn gets a short form of the test
  rule instead: the plan describes the behavior to verify, not every test name, and the
  implementer writes those tests first.
- **Agents work silently.** Text between tool calls is billed as output tokens, and
  nobody reads a session live. Neither CLI has a flag to stop it, so the implementation
  and review prompts both carry `silentWorkSection`, which asks for no progress text and
  keeps the final outcome, questions, planning documents, PR text, and findings in full.
  Resumed turns keep it because they continue the same session. The log still gets
  every event: filtering it would save no tokens and would lose diagnostics.
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
  three times before handing over. Enter on a REVIEWED issue resumes with the branch,
  PR, and session kept; a start rebuilds them.
- **A handed-over PR that conflicts is repaired once per (PR head, base head) pair.**
  Every attempted pair is kept, so none is tried twice. A repair is accepted only if
  the head moved, contains the base head, and every reported check passed; anything
  else asks the developer.
- **Cleanup never loses work.** `git worktree remove` without `--force`, so a dirty
  worktree stays. The local branch is deleted (`-D`, for squash merges) only when its PR
  merged. Unless `cleanup.on_done` is off, each refresh cleans the worktrees of DONE
  and PR_CLOSED tasks once their runner has stopped. A runner launched for a task whose
  worktree was removed recreates it from the kept branch first, so a reopened PR can
  resume. There is no manual cleanup: a FAILED worktree is the developer's to remove.

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
  its repositories. `App.InboxView` drops filtered items and stored records before
  `Reconcile`, so the configured view writes no records for them and reports no waiting
  task from them, and `fetchTracked` skips tracked items outside the view. `v` in the dashboard passes `allRepos`, which
  bypasses both lists for the session; the searches, involvement reasons, and
  `mentions` are the same in either view. In the dashboard, `v` empties INBOX until the
  new view's refresh lands, so no row from the old view stays selectable. Auto mode
  starts nothing in the all view, so a look outside the filter never spends a run there.
  DELEGATED WORK reads the task store, so tasks in any repository stay reachable.
- **Lookup failures warn and retry** next refresh without blocking other items. A 404
  or 410 on an undelegated item is reported once and then no longer checked.
- **Delegation doesn't need a checkout:** foreman keeps a clone under
  `repositories_dir` and creates worktrees from it.

### Dashboard

- **Built on `golang.org/x/term` alone.** `Model.Key` is a pure transition and `Frame` a
  pure function of the model, so nearly all of the UI is unit tested; only `Run` touches
  the terminal. Bubble Tea was rejected as ten modules in a project with one dependency.
- **It never blocks or misdraws:** the terminal check runs before config loads, so a
  piped `foreman` prints usage and exits 0; control characters in dynamic text become
  spaces; widths are counted in terminal columns; GitHub refreshes run off the input
  goroutine; quitting waits for a `Start`, `Cancel`, or `Respond` in flight.
- **Decisions are answered in the lower pane through `app.Respond`,** which resolves an
  option label, free text, or an empty answer (the recommendation). The
  prompt is its own mode: every rune is answer text, so an answer can start with `q`
  or `j`. It reads the question from the task record on each frame, so it always shows
  the oldest unanswered one. It takes half of the body, because a question is read in
  full before it is answered, and the answer line is always its last row. Opening it
  pins the cursor to the task, so a new delegation cannot move it mid-answer.
- **The log pane pages by byte offset.** Opening a log reads its last 128 KiB
  (`logChunk`); each tick reads only the bytes past the loaded end, so lines loaded
  by scrolling back stay. Scrolling past the oldest loaded line reads the chunk before
  it through `logs.Back` and `logs.Lines`, and `g` repeats that to the file start. A
  chunk is cut at a file line before cleaning, because cleaning can drop or expand a
  line. `R` shows raw lines and returns to the tail, since raw and cleaned lines do not
  line up. The scroll offset counts from the tail and grows with new lines, so a reader
  scrolled back keeps the same lines on screen.

### Notifications

- **Alerts go to a Discord webhook** (`notifications.webhook_url`). The URL is a secret:
  foreman never prints it, and errors never contain it. Delivery
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
- **`quota-axi` is optional** (https://github.com/kunchenguid/quota-axi), found on
  `PATH`. Foreman runs `quota-axi --provider claude,codex --json` with a 10 second
  limit before each route and reads `providers[].quotaSemantics.effectiveAvailability`.
  A report printed with exit code 1 (one provider failed) is still read; a missing
  binary, a timeout, or no report makes quota unknown.
- **`FOREMAN_CONFIG`** points at another config file; `scripts/e2e.sh` uses it to run
  the built binary against a local origin, a stand-in `gh`, and stub agents.
  `scripts/tui-drive.py` (python3, standard library) runs the dashboard in a
  pseudo-terminal and plays keys against it, so the e2e scenarios start, answer, cancel,
  and resume work the way the developer does.
