# Architecture

```
cmd/foreman           wiring: build adapters, call app, render output
internal/cli          argv -> Command; no I/O beyond parsing
internal/config       tasks directory resolution (Stage 1)
internal/render       terminal formatting of app results (Stage 1)
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
  if the verified PR head has no review (crash mid-review, or new commits, D63):
    re-verify the PR, record its head, skip to the review below
  turn = implementer.Start(worktree, prompt)          # later: Resume(session, msg)
  switch turn.Outcome.Status
    blocked      -> save each of Questions as a decision, state NEEDS_DECISION, exit
    failed       -> state FAILED, exit
    pr_opened    -> (the agent pushed and ran gh pr create) gh: confirm PR exists
                    record PR and its head commit, state REVIEWING
                    review = reviewer.Review(disposable read-only session,
                                             issue + context + decisions + git diff + earlier rounds)
                    clean    -> record review and the head it covered, stay REVIEWING, exit
                    findings -> WORKING; implementer.Resume(session, findings); agent pushes
                                fixes and reports pr_opened again; re-verify; re-review
                    findings after fix_rounds reviewed fixes since the last answer ->
                                NEEDS_DECISION (A: another round, B: accept remaining), exit

foreman respond 825   -> answer each waiting question in order; after the last,
                         state WORKING, spawn runner
                         runner: Resume(session, all answers); on ErrNoSession, a fresh
                         Start whose prompt lists all recorded decisions
foreman inbox|status  -> runner pid dead while WORKING/REVIEWING -> FAILED
                         readiness gate (gate.go), per idle task with a PR:
                           PR merged -> DONE; PR closed -> FAILED
                           REVIEWING + clean review, PR head moved -> WORKING, spawn
                             a runner that reviews the new commits (D63)
                           REVIEWING + clean review: gh pr checks (read only, D46)
                             pending              -> record, wait
                             finished             -> READY_FOR_HUMAN, ✓ CI or ✗ CI: <checks>
                           READY_FOR_HUMAN: refresh the CI result; flag new commits
                        inbox only, cleanup.on_done: DONE task -> git worktree remove
                           (kept if dirty); branch -D only if its PR merged
foreman clean [<issue>] -> the same cleanup on demand; <issue> may also be FAILED
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
- **Structured outcomes.** Both CLIs validate the final message against
  `internal/agent/outcome.schema.json`. `ParseOutcome` is the single place where
  external agent output becomes typed data.
- **Fresh reviewer.** The reviewer session is disposable
  (`--no-session-persistence` / `--ephemeral`). Its prompt is built from durable state:
  the issue, the context file, and `git diff <base>...HEAD`. It never sees the
  implementer's session. Repo instructions load on their own because the reviewer runs
  in the worktree.
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
