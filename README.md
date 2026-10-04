# foreman

A local CLI that hands GitHub issues to Claude Code or Codex, and gives the PR back to
you only once it is ready for your review.

```
foreman  →  pick issue #825 in the inbox, choose Claude
        ↓
  Claude implements in its own worktree, pushes, and opens the PR
        ↓
  Codex reviews it, Claude fixes, repeat until clean
        ↓
  foreman reads the PR's CI checks on GitHub
        ↓
foreman  →  READY_FOR_HUMAN in delegated work
```

You review and merge. Foreman never merges, and it doesn't run as a background service.
Nothing happens while the dashboard is closed except the per-task runner that works on
an issue you started.

## Requirements

- Go 1.27 or newer, to build.
- [`gh`](https://cli.github.com), authenticated (`gh auth status`).
- [Claude Code](https://docs.claude.com/en/docs/claude-code) (`claude`) and/or
  [Codex](https://github.com/openai/codex) (`codex`), both logged in. Each agent reviews
  the other's work, so install both for the review loop.
- Optional: [`quota-axi`](https://github.com/kunchenguid/quota-axi) on `PATH`, so
  routing can skip an agent that has run out of quota.

## Install

Download the archive for your platform from
[GitHub Releases](https://github.com/brymartinez/foreman/releases), for example:

```bash
gh release download --repo brymartinez/foreman --pattern '*darwin_arm64.tar.gz'
tar -xzf foreman_*_darwin_arm64.tar.gz
mv foreman_*_darwin_arm64/foreman ~/.local/bin/
mkdir -p ~/.config/foreman/stages
cp -n foreman_*_darwin_arm64/stages/*.md ~/.config/foreman/stages/
cp -n foreman_*_darwin_arm64/routing.yaml ~/.config/foreman/   # optional: turns on routing
```

The binaries are not notarized. If you downloaded the archive in a browser, macOS may
block the binary; run `xattr -d com.apple.quarantine ~/.local/bin/foreman` to allow it.

To build from source instead, clone the repository and run:

```bash
make setup
```

`make setup` builds foreman, installs it to `~/.local/bin`, copies the default stage
instructions from `defaults/stages/` into `~/.config/foreman/stages/`, and copies the
starter `defaults/routing.yaml` to `~/.config/foreman/routing.yaml`. It never
overwrites a stage file or a routing file you already have. When a stage file differs
from the current default, it prints `differs <path>` and the `cp` command that replaces
it. `make install` and `make config` run the
steps separately; set `BIN_DIR` or `CONFIG_DIR` to change where they go.

## Usage

Foreman is a dashboard. Everything you do with a task happens there.

```bash
foreman              # open the dashboard: inbox, delegated work, and logs
foreman -a           # open it with auto mode on for this session (also --auto)
foreman --no-auto    # open it with auto mode off for this session
foreman -c           # edit the config in $VISUAL, $EDITOR, or vi
foreman --help
foreman --version
```

`-a` and `--no-auto` override the saved auto mode choice for this session only and
don't change it. `-c` opens the config even when it fails to load, shows the error
first, and checks the file again once the editor exits. A missing or empty config is
filled with the defaults before it opens.

## Dashboard

Run `foreman` with no arguments to open a live view. This example shows the log after
pressing Enter on a delegated task:

```
foreman · 2 delegated · 3 in the inbox · auto on 1/3 · refreshed 10:01:22
▸ INBOX                           │ DELEGATED WORK
> midsoln/foreman#1  Be interactive │ ridezoomo/armada#340   Codex   READY_FOR_HUMAN
    assigned to you                │   Cache fleet lookups · ridezoomo/armada
                                   │   PR #912 · reviewer: Claude · CI ✓ · review ✓
  ridezoomo/armada#351  Retry 5xx  │
    mentions you                   │ > midsoln/foreman#2      Claude  WORKING
────────────────────────────────────────────────────────────────────────────────
LOG · midsoln/foreman#2 · ~/.local/share/foreman/logs/2.log
2026-09-29T10:01:04Z claude Bash go test ./internal/tui
j/k task · u/d log page · ctrl-u/d half page · g/G ends · s show/log · R raw · esc close · ? help
```

The left pane holds issues you can delegate, and the right pane holds delegated work.
Press `←`/`→`, `h`/`l`, or `tab` to switch between them. In DELEGATED WORK, use `j`/`k`
to pick a task and Enter to open its log below the lists. Escape closes the log. With
the log open, `u`/`d` and `g`/`G` scroll it while `j`/`k` still select tasks. The log
opens at its tail. Scrolling past the oldest loaded line reads older lines from the log
file, and `g` reads back to its first line. Press `R` to switch between cleaned lines
and the raw agent JSONL; the log returns to its tail and the heading says `raw`. Press
`s`, from either list, to swap the log for everything recorded for the task: its state,
branch, worktree, PR, planning comments, CI, usage, and last outcome, scrolled with the
same keys. Press `s` again to return to the log. Press `c` from either list to cancel
the highlighted delegated task, after confirmation. Press `?` for the keys and the
foreman version; any key closes it.

Row text is drawn in your terminal's own foreground, so it reads on a light theme and a
dark one alike. Color is kept for the headings, the dividers, the message line, and each
task's state. The selected row is drawn in reverse video with no color inside it.

| Key | What it does |
|---|---|
| `tab`, `←` `→`, `h` `l` | switch between inbox and delegated work |
| `↑` `↓`, `k` `j` | move the focused list cursor |
| `enter` on an issue | delegate it: pick `1` for Claude, `2` for Codex, or `3` to route it, then type an optional context file. A `FAILED` issue starts again; a `REVIEWED` one resumes on its PR comments |
| `enter` on a task | open its log below, or the waiting question on a `NEEDS_DECISION` task |
| `c` | cancel the highlighted delegated task, after a `y`/`n` confirm |
| `a` | turn auto mode on or off; the choice is kept for the next launch |
| `v` | switch the inbox between your configured repositories and all of them, for this session |
| `r` | refresh from GitHub now |
| `u` `d` with a log open | scroll up or down one page |
| `ctrl+u` `ctrl+d` with a log open | scroll up or down half a page |
| `g` `G` with a log open | jump to the log's first line or follow the tail |
| `R` with a log open | switch between cleaned and raw log lines |
| `s` with a log open | switch between the log and everything recorded for the task |
| `pgup` `pgdn`, `end` | optional log scroll keys for keyboards that have them |
| `esc` | close the log or abandon a prompt |
| `?` | show the keys and the version |
| `q`, `ctrl+c` | quit |

Delegated work is re-read from the task files every second, so a state change shows up
on its own. GitHub is refreshed every `refresh` seconds, 60 by default, and when you
press `r`. A refresh runs the searches, the readiness gate, and the DONE cleanup. The
dashboard refreshes only while it is open and starts no background process.

The dashboard needs a terminal at least
60 by 12; below that it says so. When stdin or stdout is not a terminal, `foreman`
prints the usage text and exits 0, so pipes and scripts are unaffected.

Enter on a `NEEDS_DECISION` task shows its oldest unanswered question below the lists:
the issue, context, options, recommendation, and impact. Type an option label or your
own answer and press Enter; an empty Enter takes the recommended option. With several
questions waiting, the
next one follows, and after the last the agent resumes and the pane shows its log.
`↑` `↓` and `pgup` `pgdn` scroll a long question. Every other key is answer text. Esc
closes the prompt; answers already recorded are kept, and Enter asks the next one. A
task whose log is open switches to its question on its own when it needs a decision.

Auto mode is on by default. While the dashboard is open, it starts open issues
assigned to you that foreman has never delegated, oldest first, with `auto.agent` and
no context file. It looks after the first refresh, after each successful refresh, and
when a run leaves WORKING or REVIEWING. Mentions, FAILED tasks, and REVIEWED tasks
never start on their own. At most `auto.max_concurrent` runs are active at once, and
runs you start by hand count toward that limit. Issues in one repository start one
at a time, because they share its checkout. You can still start an issue by hand
when the limit is full. The header shows `auto on 1/3` (active runs and the limit) or
`auto off`.

With [routing](#routing) on, auto mode routes each issue and starts it with the chosen
profile without asking. When no suitable profile can run, the issue's row says
`waiting:` and why, and auto mode tries it again after the next GitHub refresh.

Press `a` to turn auto mode off or on. Turning it off leaves running agents alone. The
choice is saved in `~/.local/share/foreman/state.json` and read at the next launch,
unless you launch with `-a` or `--no-auto`. A
start that fails is shown on the message line and is not retried until you reopen the
dashboard. An issue that lacks detail goes through the usual decision flow.

The inbox searches for open issues assigned to you, open issues and PRs that mention
you, and PRs that request your review. INBOX lists the issues you can delegate, with
what changed since the last refresh beneath each one. Each search returns up to 1000
results.

Set `mentions: false` to leave out the items found only because they mention you, in the
inbox and in the dashboard's INBOX pane. An item that is also assigned to you, or that
requests your review, still appears. The searches keep running either way, so the
changes an item reports stay accurate and the items come back the moment you turn the
setting on. A hidden item stays hidden when it leaves GitHub, so it does not announce
itself on the way out; foreman still stops checking it. It also stays hidden when a
search hits its result limit and foreman has to fetch it by key, because the setting
is applied to the involvement foreman recorded for it.

Missing undelegated items are checked until a successful
refresh with complete searches clears their involvement reasons, then stop being
refetched. Delegated work stays tracked. Missing items are not offered for new
delegation. A failed item lookup warns without aborting the inbox, keeps its stored
state, and retries next time. An issue you never delegated that GitHub no longer has,
or that you can no longer see, is dropped and then not checked again. Failed PR or CI
lookups are counted as warnings in the header.

Local builds show the commit in `foreman --version` and in the `?` help, for example
`foreman 0.1.0-dev (72a70ce12345-dirty)`. The `dirty` suffix means the build included
uncommitted changes. Builds without Git metadata show `unknown`; release builds
show their stamped version.

INBOX shows repo-qualified identities such as `acme/widgets#825`. When you delegate
one, Foreman clones or reuses its source checkout under `repositories_dir`, then
creates the agent worktree under `worktrees_dir`, so you can run `foreman` from any
directory.

The context file you can type after picking an agent is a Markdown file with extra
instructions, for example "keep the 5xx-only retry behavior". The agent treats it as
part of the task.

### What a start does

1. It creates a worktree and branch named after the issue, for example
   `fix/825-provider-retries`. The slug is capped at 5 words, and the worktree directory
   is `825-provider-retries`.
   - The prefix is a conventional-commit type. Foreman takes it from the title's own
     prefix (`fix: …`), otherwise from a label (`bug` → `fix`, `documentation` →
     `docs`, …), otherwise it uses `feat`.
2. It launches a detached runner that logs to `~/.local/share/foreman/logs/`. The
   runner keeps going after you quit the dashboard.
3. The runner has the agent implement the issue, run validation, push, and open a PR
   with `gh pr create`. Foreman then checks with GitHub that the PR exists and targets
   the right branch.
4. The other agent reviews the PR in a read-only, throwaway session. Findings go back
   to the implementer's session, and the loop repeats until the review is clean.
5. On the next GitHub refresh, foreman reads the PR's CI checks on
   GitHub, the same list as at the bottom of the PR page. Once they finish, the task is
   `READY_FOR_HUMAN`.
   - Passing checks show as `✓ CI`. Failing checks are listed as `✗ CI: test`, with
     links.
   - CI belongs to the repo's owners, so foreman never runs or fixes it. You decide
     whether a failure comes from the change or from CI itself.
   - A repo with no checks shows `✓ CI (no checks)`.
   - A merged PR marks the task DONE. A PR closed without merging marks it
     `PR_CLOSED` and stops the run.
6. Every review records the commit it covered. If the PR gets new commits after a clean
   review, the next refresh has the reviewer look at them before the task is
   `READY_FOR_HUMAN`. Once it is, the PR is yours: new commits are flagged but not taken
   back. A runner that crashes during a review picks up at the review when you start
   the `FAILED` task again from the inbox.

## Planning

Before any code, the implementer writes four documents: an analysis, a PRD, a design,
and an implementation plan. `plan.md` sets what each one covers. The runner posts each
document on the issue as its own comment. It then starts a fresh session with the
posted plan links to implement the work. If planning needs a decision, the task goes
to `NEEDS_DECISION`. Otherwise, it continues to the PR without an approval step.
The reviewer checks the PR against the posted PRD.

The `s` view links the four comments. A task that plans again, for example after a
failed run, edits the same comments instead of adding new ones. A task that already
has a PR goes on without a plan.

## Stage instructions

Each stage of the work has an instruction file that holds the agents' working rules:
how to implement, when to ask, what a review counts as a finding, and how a PR
description reads. Foreman's own prompts keep only what it needs to run the task: the
worktree and branch, the push and `gh pr` commands, `Closes #N`, the read-only
reviewer, the question format, and the JSON response. The stage files add to the
agents' own `CLAUDE.md` and `AGENTS.md`, which the agents keep loading as usual.
Foreman never reads or changes those files.

| Stage | Given to | Global file | Repo file (committed) |
|---|---|---|---|
| `plan` | the implementer, for the four planning documents | `~/.config/foreman/stages/plan.md` | `.foreman/plan.md` |
| `implement` | the implementer | `~/.config/foreman/stages/implement.md` | `.foreman/implement.md` |
| `review` | the competing reviewer | `~/.config/foreman/stages/review.md` | `.foreman/review.md` |
| `pr` | the implementer, for the PR description | `~/.config/foreman/stages/pr.md` | `.foreman/pr.md` |

- The four global files are required. The dashboard won't open when one is missing,
  and says which. `make config` installs the defaults from `defaults/stages/`, and the
  release archives carry them in `stages/`. Edit your copies freely.
- Repo files are optional.
- When both files exist, the agent gets both: global first, then the repo's more
  specific rules.
- The task's context file and your recorded decisions take precedence over stage
  instructions.
- Tasks read `.foreman/` from their worktree, so only the version committed on the base
  branch counts.
- A PR body always includes `Closes #N`. A review always ends in foreman's JSON format.
  Otherwise the text is passed through as written.

## Cleaning up worktrees

When a refresh finds a task DONE, it removes the task's worktree. When the PR was
merged, it also deletes the local `feat/...` or `fix/...` branch. There is no manual
cleanup: the worktree of a FAILED or PR_CLOSED task stays until you remove it with
`git worktree remove`.

- A worktree with uncommitted or untracked changes is never removed. A later refresh
  removes it once it is tidy.
- A branch whose PR wasn't merged is kept, so a new start can pick the work up again.
- Remote branches are left to GitHub's "Automatically delete head branches" setting.

Turn either behavior off in the config, under `cleanup`.

## When the agent needs a decision

Agents make routine calls themselves. When something needs your judgment, such as a
product choice or conflicting requirements, the agent stops and returns one or more structured
questions. The task becomes `NEEDS_DECISION`, and the agent process ends. Nothing waits in
a terminal.

Foreman posts to a Discord webhook when a task needs a decision, fails or is cancelled,
becomes ready for review, or gets PR feedback. Each post names the alert, the task
(`owner/repo#N`) and its state, and links the PR, or the issue when there is no PR yet.
Set the webhook URL in `notifications.webhook_url` (see Configuration). The URL is a
secret: keep the config file `chmod 600`. Foreman warns when the URL is missing or the
file is readable by other users, and never prints the URL. Delivery is best effort. A
failed post never changes a task, and Foreman appends the error to `notifications.log`
in the log directory. The dashboard shows the task's state the same either way.

In the dashboard, Enter on the `NEEDS_DECISION` task shows each waiting question in
turn: the issue, context, options, recommendation, and impact if wrong. You can type
an option label, write your own answer, or press Enter to take the recommendation. Esc
leaves the remaining questions waiting. Foreman saves each answer to the task file
and, once all are answered, resumes
the same agent session with them. If that session is gone, it starts a fresh turn that includes every
decision so far. The answer also appears in the PR description.

The implementer and reviewer take turns: every fix is reviewed again. If the review
after the third fix (`fix_rounds`) still has findings, foreman asks the same way: do
another round, or accept the remaining findings. The implementer gets those findings
along with your answer.

## When you close a PR

Closing a delegated PR without merging it ends the task. On the next GitHub refresh,
foreman stops the agent if one is still running, moves the task to `PR_CLOSED`, and
drops it from DELEGATED WORK. Nothing is deleted: the task file, the worktree, and the
branch are kept.
Closing the issue as well leaves the task `PR_CLOSED` rather than DONE, so the worktree
is not cleaned up behind you.
The run did not fail, so there is no failure notification.

Reopen the PR and the task comes back on the following refresh. Foreman reads the PR's
CI and uses the review it already has: a clean review of the PR's current head with
finished CI makes the task `READY_FOR_HUMAN`, and anything else makes it `REVIEWING`.
No agent is started for you either way.

If the agent does not stop at once, the task is `PR_CLOSED` but keeps its runner, and
every refresh tries again.

## States

| State | Meaning | Your move |
|---|---|---|
| `WORKING` | The implementer is running | Wait, or `c` on the task to stop it |
| `NEEDS_DECISION` | The agent asked a question | Enter on the task to answer |
| `REVIEWING` | PR open; under review, or waiting on CI | Wait, or `c` on the task to stop a running review |
| `READY_FOR_HUMAN` | Review clean, CI finished (✓ or ✗ shown) | Review and merge the PR |
| `DONE` | PR merged or issue closed | Nothing |
| `FAILED` | The agent failed, the run was cancelled, or the runner died | `s` on the task to see why, then Enter on it in the inbox to retry |
| `PR_CLOSED` | You closed the PR without merging it | Nothing, or reopen the PR |

## Task files

Each task is one Markdown file with YAML frontmatter, stored in
`~/.local/share/foreman/tasks/<owner>-<repo>-<number>.md`. The frontmatter holds the
state, session ids, PR, validation, review rounds, CI, and decisions. The body is yours,
so you can add notes there.

Foreman rewrites only the frontmatter keys it owns. Keys you add, such as `tags`,
`aliases`, or anything else, stay in place along with their comments.

### Obsidian

The files are ready for Obsidian. The first time you open the dashboard, Foreman
explains the configured task directory and offers to create it. To use a folder inside
your vault, point `tasks_dir` at it first:

```yaml
tasks_dir: ~/Documents/vault/foreman
```

Each task then appears as a note, and the frontmatter shows up as properties.

## Configuration

Foreman reads `~/.config/foreman/config.yaml`, or the file named in `$FOREMAN_CONFIG`.
Every key is optional. These are the defaults:

```yaml
tasks_dir: ~/.local/share/foreman/tasks
worktrees_dir: ~/.local/share/foreman/worktrees
repositories_dir: ~/.local/share/foreman/repositories # source checkouts for delegated work
log_dir: ~/.local/share/foreman/logs
review_rounds: 3                     # reviewer passes over the whole PR per review
fix_rounds: 3                        # fix and re-review cycles before foreman asks you
mentions: true                       # show items found only because they mention you
refresh: 60                          # seconds between the dashboard's GitHub refreshes
auto:                                # the dashboard's auto mode; press a to turn it off
  agent: claude                      # claude or codex
  max_concurrent: 3                  # active runs, manual ones included
budgets:                             # Claude only: Codex reports no cost, so it has no cap
  implementer_usd: 3.00              # Claude maximum per implementation turn
  reviewer_usd: 1.00                 # Claude maximum per review turn
notifications:
  enabled: true                      # decision, failure, readiness, and feedback alerts
  webhook_url: ""                    # Discord webhook URL; a secret, keep this file chmod 600
claude:
  command: claude
  model: claude-opus-5               # implementation and PR; "" uses the CLI default
  plan_model: ""                     # "" uses model
  review_model: ""                   # "" uses model
codex:
  command: codex
  model: gpt-6-sol                   # implementation and PR; "" uses the CLI default
  plan_model: ""                     # "" uses model
  review_model: ""                   # "" uses model
  reasoning_effort: ""               # implementation; "" uses the CLI default
  plan_reasoning_effort: ""          # "" uses reasoning_effort
  review_reasoning_effort: ""        # "" uses reasoning_effort
include_repositories: []             # exact owner/name; when set, the inbox shows only these
exclude_repositories: []             # exact owner/name hidden from the inbox when include is empty
cleanup:
  on_done: true                   # each refresh removes DONE tasks' worktrees
  delete_merged_branch: true      # and the local branch, once the PR is merged
```

`FOREMAN_TASKS_DIR` overrides `tasks_dir`.

Planning, implementation, and review can use separate models. PR creation is part of
implementation. The agent chosen for the task plans and implements; the competing
agent reviews. Run `foreman -c` to set the values. For example:

```yaml
claude:
  model: opus
  plan_model: sonnet
  review_model: opus
codex:
  model: gpt-6-sol
  plan_model: gpt-6-luna
  review_model: gpt-6-sol
  reasoning_effort: medium
  plan_reasoning_effort: xhigh
  review_reasoning_effort: medium
```

`refresh` must be at least 5 seconds; foreman refuses to start with a smaller value.
Task files and the open log are re-read every second regardless, and `r` refreshes
GitHub whenever you ask.

`include_repositories` and `exclude_repositories` accept exact repository names such as
`acme/widgets`. A nonempty include list limits the inbox to those repositories and the
exclude list is then ignored. With an empty include list, the exclude list hides its
repositories. The filter applies to fresh inbox results and tracked-task refreshes.
Press `v` in the dashboard to see every repository for the rest of the session;
`mentions` still applies. Auto mode pauses while the dashboard shows every repository,
so it only starts work from your configured repositories. DELEGATED WORK lists every
task whatever its repository.

## Routing

`~/.config/foreman/routing.yaml`, next to `config.yaml`, lets foreman choose the agent
and models for each new issue you start from the dashboard. Without the file, routing
is off and the dashboard works as before. `make setup` installs this starter file:

```yaml
selector:
  agent: claude                     # model "" uses claude.plan_model, else claude.model
profiles:
  claude:
    agent: claude
    plan_model: sonnet
    model: opus
  codex:
    agent: codex
    plan_model: gpt-6-luna
    plan_reasoning_effort: xhigh
    model: gpt-6-sol
    reasoning_effort: medium
rules:
  - when: Broad or ambiguous work, design or architecture changes, or refactors across several packages.
    profiles: [claude]
    why: broad or ambiguous design work
  - when: Narrow, well-specified change with clear steps or acceptance criteria, such as a small bug fix, a typo, or a single function.
    profiles: [codex]
    why: narrow, well-specified change
  - when: Any other work.
    profiles: [claude, codex]
    why: general work
default: claude
```

The rules' `when` texts may take up to 2500 characters in all, so the issue always
fits in the selector's input. A profile sets `agent`, and may set `plan_model`, `model`, and for Codex
`plan_reasoning_effort` and `reasoning_effort`. A field you leave out uses the
`claude.*` or `codex.*` value in `config.yaml`. Budgets, notifications, repository
filters, cleanup, auto mode, and review settings stay in `config.yaml`.

Before a new task starts, the selector agent reads the issue's title, labels, body,
recent comments, and your context file, and picks the first rule whose `when` fits.
Foreman then tries that rule's profiles in order and skips one whose agent `quota-axi`
reports as out of quota. A profile the rule does not list is never tried. If the
selector is unsure or fails, foreman uses `default`. If no listed profile can run,
nothing starts and the dashboard says why. If `quota-axi` is missing or cannot tell,
foreman uses `default` when the rule lists it and says the quota is unknown.

In the dashboard, press `3` after `enter` on a new issue to route it. The footer shows
the chosen profile, its models, and the reason: `enter` starts it, and `esc` goes back
to `1` and `2`, which still pick an agent yourself.

The task keeps its profile: a retry with the same agent uses it, and starting with the
other agent drops it. The competing agent still reviews with its review settings.
The `s` view lists the profile and the reason, and the task's log starts with a
`route:<profile>:<agent>:<reason>` line. An error in `routing.yaml` shows when you
route an issue, and nothing routed starts until you fix it.

## Permissions

- **Claude as implementer.** Runs with `--permission-mode acceptEdits`, with only
  `Bash`, `WebFetch`, and `WebSearch` pre-approved.
- **Codex as implementer.** Runs in the `workspace-write` sandbox with network access,
  so it can push and use `gh`.
- **Reviewers.** Read-only sessions that can't change the worktree.
- The CLIs' dangerous bypass flags are never used.

## Development

```bash
go vet ./... && go test ./...
go build -o dist/foreman ./cmd/foreman
scripts/e2e.sh            # the built binary against a local origin, stand-in gh, stub agents
scripts/probe-agents.sh   # re-checks the real agent CLIs (a few cents of model calls)
scripts/release.sh v0.1.0 # build the release archives locally into dist/release
```

To publish a release, push a tag. The release workflow runs vet and the tests, builds
the archives, and creates the GitHub release:

```bash
git tag v0.1.0 && git push origin v0.1.0
```

Design and status are in [`docs/control-layer/`](docs/control-layer/):

- [progress.md](docs/control-layer/progress.md) covers the current stage, open work,
  and the known limitations.
- [architecture.md](docs/control-layer/architecture.md) covers the package layout, the
  execution model, and the design rules with their reasons.
- [capabilities.md](docs/control-layer/capabilities.md) lists the verified CLI
  behavior.
