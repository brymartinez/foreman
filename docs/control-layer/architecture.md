# Architecture

```
cmd/foreman           wiring: build adapters, call app, render output
internal/cli          argv -> Command; no I/O beyond parsing
internal/config       tasks directory resolution (Stage 1)
internal/render       terminal formatting of app results (Stage 1)
internal/tui          the dashboard: pure Model and Frame, one terminal loop (D94)
internal/app          orchestration + ports.go (the interfaces below)
  ├── github          GitHubClient and RepositoryManager over `gh` (Stages 1, 9)
  ├── store           TaskStore over Markdown + YAML frontmatter (Stage 1)
  ├── worktree        WorktreeManager over `git worktree` (Stage 2)
  ├── agent           outcome contract, prompt, claude.go / codex.go adapters (Stage 2)
  ├── runner          spawns and checks the detached `foreman __run` process (Stage 2)
  └── stages          resolves ~/.config/foreman/stages/<stage>.md + <repo>/.foreman/<stage>.md (D47)
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
  if the PR has comments the agent has not answered (D119, resumed from REVIEWED):
    turn = implementer.Resume(session, each comment, where it is, and how to reply)
    head unchanged -> mark them answered, keep CI, READY_FOR_HUMAN (D122)
    head moved     -> mark them answered, review the new head like any change
  if the verified PR head has no review (crash mid-review, or new commits, D63):
    re-verify the PR, record its head, skip to the review below
  turn = implementer.Start(worktree, prompt)          # later: Resume(session, msg)
  until every planning document is on the issue (and no PR exists), the turn plans (D90):
    planned      -> gh: post analysis, PRD, design, and plan as four issue comments
                    (edit the earlier comments on a re-plan), record their ids and URLs,
                    then turn = implementer.Start(new session, posted plan links)
  switch turn.Outcome.Status
    blocked      -> save each of Questions as a decision, state NEEDS_DECISION, exit
    failed       -> state FAILED, exit
    pr_opened    -> (the agent pushed and ran gh pr create) gh: confirm PR exists
                    record PR and its head commit, state REVIEWING
                    review = reviewer.Review(disposable read-only session,
                                             issue + context + decisions + git diff + earlier rounds)
                    clean    -> record review and the head it covered; read the PR's comments,
                and answer anything new before handing over (D123); stay REVIEWING, exit
                    findings -> WORKING; implementer.Resume(session, findings); agent pushes
                                fixes and reports pr_opened again; re-verify; re-review
                    findings after fix_rounds reviewed fixes since the last answer ->
                                NEEDS_DECISION (A: another round, B: accept remaining), exit

foreman resume 825    -> a REVIEWED task goes back to WORKING with its branch,
                         worktree, PR, and session kept; the runner answers the
                         comments (D121)
foreman respond 825   -> answer each waiting question in order; after the last,
                         state WORKING, spawn runner
                         runner: Resume(session, all answers); on ErrNoSession, a fresh
                         Start whose prompt lists all recorded decisions
foreman cancel 825    -> SIGTERM the runner, wait up to 30s for it to exit (D89)
                         runner: context cancelled -> agent process group stopped;
                         the failed step records FAILED, outcome cancelled, keeping
                         session, usage, branch, worktree, and PR
foreman               -> the dashboard (D93), until q or ctrl+c:
                         every 1s : app.Tasks re-reads the task files and, when open,
                                    re-tails the selected task's runner log; no GitHub call
                         every `refresh` seconds (60 by default): app.Inbox, the
                           same as `foreman inbox` (D95, D110)
                         enter on an inbox issue -> agent, context file, app.Start
                         enter on a REVIEWED issue -> app.Resume, no prompts (D121)
                         enter on delegated work -> open that task's log below the lists
                         left/right, h/l -> focus the inbox/delegated work
                         esc -> close the log
                         c on a running task     -> confirm, app.Cancel

foreman inbox|status  -> runner pid dead while WORKING/REVIEWING -> FAILED
                         readiness gate (gate.go), per task with a PR:
                           PR closed  -> stop the runner, PR_CLOSED (D107)
                           PR reopened -> READY_FOR_HUMAN or REVIEWING (D108)
                         and then, per idle task with a PR:
                           PR merged -> DONE
                           REVIEWING + clean review, PR head moved -> WORKING, spawn
                             a runner that reviews the new commits (D63)
                           REVIEWING + clean review: gh pr checks (read only, D46)
                             pending              -> record, wait
                             finished             -> READY_FOR_HUMAN, ✓ CI or ✗ CI: <checks>
                           READY_FOR_HUMAN: refresh the CI result; flag new commits
                         and then, per task the agents are done with (READY_FOR_HUMAN
                         or REVIEWED, PR open, no live runner):
                           read the PR's comments, drop the agent's own replies (D120)
                           any unanswered -> REVIEWED, shown in INBOX (D119)
                           none left      -> READY_FOR_HUMAN
                        inbox only, cleanup.on_done: DONE task -> git worktree remove
                           (kept if dirty); branch -D only if its PR merged
foreman clean [<issue>] -> the same cleanup on demand; <issue> may also be FAILED
                           or PR_CLOSED
```

Nothing runs in the background except the runner working on a task. The readiness and
liveness checks happen whenever the user runs a command. Every task write goes through
`App.update`, which sends a desktop notification when a task enters NEEDS_DECISION,
FAILED, or READY_FOR_HUMAN (D59).

## Why each piece is shaped this way

- **One runner per task.** Each task has its own worktree and task file. The runner,
  `inbox`, and `status` can all write the same task file, so every write goes through
  `TaskStore.Update`, which re-reads the file under a lock file (D21). `inbox` only merges
  in fields that come from GitHub.
- **The runner owns its log.** `Spawn` hands the log file itself to the runner, and the
  runner timestamps its own lines. A writer in the launcher would need a pipe, which
  dies with the launcher (D58). Agents run in their own process group, so stopping a
  runner stops the tools its agent started (D59).
- **INBOX is work to start.** The list offers issues foreman has never delegated plus
  delegated ones in FAILED or REVIEWED. Everything else an agent holds stays in
  DELEGATED WORK, so the same issue is never delegated twice (D124).
- **Structured outcomes.** Both CLIs validate the final message against
  `internal/agent/outcome.schema.json`. `ParseOutcome` is the single place where
  external agent output becomes typed data.
- **Fresh reviewer.** The reviewer session is disposable
  (`--no-session-persistence` / `--ephemeral`). Its prompt is built from durable state:
  the issue, the context file, and `git diff <base>...HEAD`. It never sees the
  implementer's session. Repo instructions load on their own because the reviewer runs
  in the worktree.
- **Stage models.** The chosen implementer uses its plan model until the four plan
  comments are posted, then its implementation model in a new session. Fixes resume
  that implementation session. The competing reviewer uses its review model in a
  disposable session. Codex can set reasoning effort for each of these turns.
- **Naming.** `task.WorkName` gives `<issue>-<slug>`, for example
  `825-provider-retries`. The slug is at most 5 words and 40 characters, and a
  conventional-commit prefix is removed. The branch is `<type>/<workName>`, where the type is `feat`, `fix` and so on,
  picked from `--type`, the title prefix, or the labels (D45), and the
  worktree directory is `<workName>`. The name is computed once at `start` and stored,
  so both agents and the user see the same name for the life of the task.
- **Idempotent steps.** Creating the worktree and the task file each check whether the
  thing already exists, and so does the agent's PR step (`gh pr view` before
  `gh pr create`). That lets a crashed runner be restarted from the
  task file.
