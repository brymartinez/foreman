# foreman

A local CLI that hands GitHub issues to Claude Code or Codex, and gives the PR back to
you only once it is ready for your review.

```
foreman start 825 --agent claude
        ↓
  Claude implements in its own worktree, pushes, and opens the PR
        ↓
  Codex reviews it, Claude fixes, repeat until clean
        ↓
  foreman reads the PR's CI checks on GitHub
        ↓
foreman inbox  →  READY FOR YOU
```

You review and merge. Foreman never merges, and it doesn't run as a background service.
Nothing happens between commands except the per-task runner that works on an issue you
started.

## Requirements

- Go 1.27 or newer, to build.
- [`gh`](https://cli.github.com), authenticated (`gh auth status`).
- [Claude Code](https://docs.claude.com/en/docs/claude-code) (`claude`) and/or
  [Codex](https://github.com/openai/codex) (`codex`), both logged in. Each agent reviews
  the other's work, so install both for the review loop.

## Install

Download the archive for your platform from
[GitHub Releases](https://github.com/brymartinez/foreman/releases), for example:

```bash
gh release download --repo brymartinez/foreman --pattern '*darwin_arm64.tar.gz'
tar -xzf foreman_*_darwin_arm64.tar.gz
mv foreman_*_darwin_arm64/foreman ~/.local/bin/
mkdir -p ~/.config/foreman/stages
cp -n foreman_*_darwin_arm64/stages/*.md ~/.config/foreman/stages/
```

The binaries are not notarized. If you downloaded the archive in a browser, macOS may
block the binary; run `xattr -d com.apple.quarantine ~/.local/bin/foreman` to allow it.

To build from source instead, clone the repository and run:

```bash
make setup
```

`make setup` builds foreman, installs it to `~/.local/bin`, and copies the default stage
instructions from `defaults/stages/` into `~/.config/foreman/stages/`. It never
overwrites a stage file you already have. `make install` and `make config` run the
steps separately; set `BIN_DIR` or `CONFIG_DIR` to change where they go.

## Usage

Run `start` from any checkout of the repository. Foreman reads the repo and its default
branch from that checkout.

```bash
foreman                                        # dashboard: inbox, delegated work, and logs
foreman inbox                                  # refresh, pick an issue, and delegate it
foreman inbox --non-interactive                 # print the report without prompting
foreman start 825 --agent claude               # delegate issue #825
foreman start 825 --agent codex --context ./context/825.md
foreman start 825 --agent claude --type fix    # force the branch prefix
foreman status                                 # delegated work, most urgent first
foreman show 825                               # everything recorded for one task
foreman logs 825                               # clean, timestamped runner log
foreman logs 825 --follow                       # follow a live runner
foreman logs 825 --raw                          # show raw agent JSONL (Claude and Codex)
foreman respond 825                            # answer a question from the agent
foreman cancel 825                             # stop a running agent; foreman start resumes
foreman clean --dry-run                        # which worktrees would be removed
foreman clean                                  # remove worktrees of DONE tasks
foreman config --stages                         # which stage instruction files apply here
foreman config --show                           # show the resolved configuration
foreman config --edit                           # edit the config in $VISUAL/$EDITOR
foreman version
```

`<issue>` can be `825`, `#825`, or `owner/repo#825`. Use the last form when the same
number is tracked in more than one repo.

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
j/k task · u/d log page · ctrl-u/d half page · g/G ends · s show/log · esc close · h/l pane
```

The left pane holds issues you can delegate, and the right pane holds delegated work.
Press `←`/`→`, `h`/`l`, or `tab` to switch between them. In DELEGATED WORK, use `j`/`k`
to pick a task and Enter to open its log below the lists. Escape closes the log. With
the log open, `u`/`d` and `g`/`G` scroll it while `j`/`k` still select tasks. Press `s`,
from either list, to swap the log for the task's full `foreman show` output, wrapped to
the terminal width and scrolled with the same keys. Press `s` again to return to the
log. Press `c` from either list to cancel the highlighted delegated task, after
confirmation.

Row text is drawn in your terminal's own foreground, so it reads on a light theme and a
dark one alike. Color is kept for the headings, the dividers, the message line, and each
task's state. The selected row is drawn in reverse video with no color inside it.

| Key | What it does |
|---|---|
| `tab`, `←` `→`, `h` `l` | switch between inbox and delegated work |
| `↑` `↓`, `k` `j` | move the focused list cursor |
| `enter` on an issue | delegate it: pick `1` for Claude or `2` for Codex, then type an optional context file |
| `enter` on a task | open its log below; show how to answer a waiting question |
| `c` | cancel the highlighted delegated task, after a `y`/`n` confirm |
| `a` | turn auto mode on or off; the choice is kept for the next launch |
| `v` | switch the inbox between your configured repositories and all of them, for this session |
| `r` | refresh from GitHub now |
| `u` `d` with a log open | scroll up or down one page |
| `ctrl+u` `ctrl+d` with a log open | scroll up or down half a page |
| `g` `G` with a log open | jump to the oldest available line or follow the tail |
| `s` with a log open | switch between the log and the task's `foreman show` output |
| `pgup` `pgdn`, `end` | optional log scroll keys for keyboards that have them |
| `esc` | close the log or abandon a prompt |
| `q`, `ctrl+c` | quit |

Delegated work is re-read from the task files every second, so a state change shows up
on its own. GitHub is refreshed every `refresh` seconds, 60 by default, and when you
press `r`: that runs the same work as `foreman inbox`, including the readiness gate and
the DONE cleanup. The dashboard refreshes only while it is open and starts no background
process.

`foreman tui` is the same command spelled out. The dashboard needs a terminal at least
60 by 12; below that it says so. When stdin or stdout is not a terminal, `foreman`
prints this usage text and exits 0, so pipes and scripts are unaffected.

Answering a question still belongs to `foreman respond <issue>`, which the dashboard
points you at.

Auto mode is on by default. While the dashboard is open, it starts open issues
assigned to you that foreman has never delegated, oldest first, with `auto.agent` and
no context file. It looks after the first refresh, after each successful refresh, and
when a run leaves WORKING or REVIEWING. Mentions, FAILED tasks, and REVIEWED tasks
never start on their own. At most `auto.max_concurrent` runs are active at once, and
runs you start by hand count toward that limit. Issues in one repository start one
at a time, because they share its checkout. You can still start an issue by hand
when the limit is full. The header shows `auto on 1/3` (active runs and the limit) or
`auto off`.

Press `a` to turn auto mode off or on. Turning it off leaves running agents alone. The
choice is saved in `~/.local/share/foreman/state.json` and read at the next launch. A
start that fails is shown on the message line and is not retried until you reopen the
dashboard. An issue that lacks detail goes through the usual decision flow.

`foreman inbox` lists open issues assigned to you, open issues and PRs that mention
you, and PRs that request your review. It also reports what changed since the last run,
including a reason an item lost, such as "no longer assigned to you". Each search
returns up to 1000 results, and the inbox warns when one hits that limit.

Set `mentions: false` to leave out the items found only because they mention you, in the
inbox and in the dashboard's INBOX pane. An item that is also assigned to you, or that
requests your review, still appears. The searches keep running either way, so the
changes an item reports stay accurate and the items come back the moment you turn the
setting on. A hidden item stays hidden when it leaves GitHub, so it does not announce
itself on the way out; foreman still stops checking it. It also stays hidden when a
search hits its result limit and foreman has to fetch it by key, because the setting
is applied to the involvement foreman recorded for it.

Waiting tasks show changes beneath their NEEDS DECISION or READY FOR YOU entry,
including CI changes. Missing undelegated items are checked until a successful
refresh with complete searches clears their involvement reasons, then stop being
refetched. Delegated work stays tracked. Missing items are not offered for new
delegation. A failed item lookup warns without aborting the inbox, keeps its stored
state, and retries next time. An issue you never delegated that GitHub no longer has,
or that you can no longer see, is reported once and then not checked again. DONE tasks
and repositories outside the configured filter are excluded. `foreman status` lists every task it can,
with any failed PR or CI lookups as warnings underneath.

Local builds show the commit in `foreman version`, for example
`foreman 0.1.0-dev (72a70ce12345-dirty)`. The `dirty` suffix means the build included
uncommitted changes. Builds without Git metadata show `unknown`; release builds
show their stamped version.

The inbox is interactive by default. It shows repo-qualified identities such as
`acme/widgets#825`, offers the issues for delegation, asks which configured agent should
implement the chosen one, and accepts an optional context Markdown file. Press `v` to switch between your configured
repositories and all of them, and `q` to leave without starting work. Use
`--non-interactive` for scripts and the report-style output. The interactive picker
shows unchanged current items again on later runs and can be run from any directory.
When you delegate an item, Foreman clones or reuses its source checkout under
`repositories_dir`, then creates the agent worktree under `worktrees_dir`.

`--context` is a Markdown file with extra instructions, for example "keep the 5xx-only
retry behavior". The agent treats it as part of the task.

### What `start` does

1. It creates a worktree and branch named after the issue, for example
   `fix/825-provider-retries`. The slug is capped at 5 words, and the worktree directory
   is `825-provider-retries`.
   - The prefix is a conventional-commit type. Foreman takes it from the title's own
     prefix (`fix: …`), otherwise from a label (`bug` → `fix`, `documentation` →
     `docs`, …), otherwise it uses `feat`.
   - `--type chore` overrides it.
2. It launches a detached runner, logs to `~/.local/share/foreman/logs/`, and returns
   immediately.
3. The runner has the agent implement the issue, run validation, push, and open a PR
   with `gh pr create`. Foreman then checks with GitHub that the PR exists and targets
   the right branch.
4. The other agent reviews the PR in a read-only, throwaway session. Findings go back
   to the implementer's session, and the loop repeats until the review is clean.
5. The next time you run `inbox` or `status`, foreman reads the PR's CI checks on
   GitHub, the same list as at the bottom of the PR page. Once they finish, the task is
   READY FOR YOU.
   - Passing checks show as `✓ CI`. Failing checks are listed as `✗ CI: test`, with
     links.
   - CI belongs to the repo's owners, so foreman never runs or fixes it. You decide
     whether a failure comes from the change or from CI itself.
   - A repo with no checks shows `✓ CI (no checks)`.
   - A merged PR marks the task DONE. A PR closed without merging marks it
     `PR_CLOSED` and stops the run.
6. Every review records the commit it covered. If the PR gets new commits after a clean
   review, the next `inbox` or `status` has the reviewer look at them before the task
   is READY FOR YOU. Once a task is READY FOR YOU, the PR is yours: new commits are
   flagged in its checklist but not taken back. A runner that crashes during a review
   picks up at the review when you run `foreman start` again.

## Planning

Before any code, the implementer writes four documents: an analysis, a PRD, a design,
and an implementation plan. `plan.md` sets what each one covers. The runner posts each
document on the issue as its own comment. It then starts a fresh session with the
posted plan links to implement the work. If planning needs a decision, the task goes
to `NEEDS_DECISION`. Otherwise, it continues to the PR without an approval step.
The reviewer checks the PR against the posted PRD.

`foreman show` links the four comments. A task that plans again, for example after a
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

- The four global files are required. Every command except `config` fails when one
  is missing. `make config` installs the defaults from `defaults/stages/`, and the
  release archives carry them in `stages/`. Edit your copies freely.
- Repo files are optional.
- When both files exist, the agent gets both: global first, then the repo's more
  specific rules.
- The task's `--context` file and your recorded decisions take precedence over stage
  instructions.
- Tasks read `.foreman/` from their worktree, so only the version committed on the base
  branch counts.
- A PR body always includes `Closes #N`. A review always ends in foreman's JSON format.
  Otherwise the text is passed through as written.

```bash
foreman config --stages         # where each stage's instructions come from
```

## Cleaning up worktrees

When `inbox` finds a task DONE, it removes the task's worktree. When the PR was merged,
it also deletes the local `feat/...` or `fix/...` branch. `foreman clean` does the same on demand,
and `foreman clean <issue>` also works for a FAILED or PR_CLOSED task.

- A worktree with uncommitted or untracked changes is never removed. Inbox reports it,
  and you rerun `foreman clean <issue>` once it is tidy.
- A branch whose PR wasn't merged is kept, so `foreman start` can pick the work up
  again.
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
file is readable by other users, and `config --show` prints only whether it is set.
Delivery is best effort. A failed post never changes a task, and Foreman appends the
error to `notifications.log` in the log directory. The inbox and `status` show the same
thing the next time you run them.

```
NEEDS DECISION
  acme/widgets#825  Provider retries
        acme/widgets · Should retries also apply to 4xx responses?
```

`foreman respond 825` shows each waiting question in turn and reads your answers:

```
BLOCKED: Need decision

Issue:
Should retries also apply to 4xx responses?

Context:
The retry queue only retries 5xx today. The issue says "retry failed requests".

Options:
A. Keep retries limited to 5xx
B. Retry 4xx as well

Recommendation:
A

Impact if wrong:
Retrying 4xx repeats requests that will never succeed.

Decision (option label, your own answer, or Enter for A):
> A
```

You can type an option label, write your own answer, or press Enter to take the
recommendation. If input ends before you answer, the remaining questions keep
waiting. Foreman saves each answer to the task file and, once all are answered, resumes
the same agent session with them. If that session is gone, it starts a fresh turn that includes every
decision so far. The answer also appears in the READY FOR YOU summary and in the PR
description.

The implementer and reviewer take turns: every fix is reviewed again. If the review
after the third fix (`fix_rounds`) still has findings, foreman asks the same way: do
another round, or accept the remaining findings. The implementer gets those findings
along with your answer.

## When you close a PR

Closing a delegated PR without merging it ends the task. On the next GitHub refresh,
foreman stops the agent if one is still running, moves the task to `PR_CLOSED`, and
drops it from DELEGATED WORK. Nothing is deleted: the task file, the worktree, and the
branch are kept, and `foreman clean <issue>` removes the worktree when you want it gone.
Closing the issue as well leaves the task `PR_CLOSED` rather than DONE, so the worktree
is not cleaned up behind you.
The run did not fail, so there is no failure notification; the change is reported as an
inbox note the next time you look.

Reopen the PR and the task comes back on the following refresh. Foreman reads the PR's
CI and uses the review it already has: a clean review of the PR's current head with
finished CI makes the task `READY_FOR_HUMAN`, and anything else makes it `REVIEWING`,
which `foreman start` picks up. No agent is started for you either way.

If the agent does not stop at once, the task is `PR_CLOSED` but keeps its runner, and
every refresh tries again. `foreman start` and `foreman clean` refuse it until that
runner is gone, so a second agent never shares the worktree with the first.

## States

| State | Meaning | Your move |
|---|---|---|
| `WORKING` | The implementer is running | Wait, or `foreman cancel` to stop it |
| `NEEDS_DECISION` | The agent asked a question | `foreman respond` |
| `REVIEWING` | PR open; under review, or waiting on CI | Wait, or `foreman cancel` to stop a running review |
| `READY_FOR_HUMAN` | Review clean, CI finished (✓ or ✗ shown) | Review and merge the PR |
| `DONE` | PR merged or issue closed | Nothing |
| `FAILED` | The agent failed, the run was cancelled, or the runner died | `foreman show` to see why, then `foreman start` to retry |
| `PR_CLOSED` | You closed the PR without merging it | Nothing, or reopen the PR, or `foreman start` to retry |

## Task files

Each task is one Markdown file with YAML frontmatter, stored in
`~/.local/share/foreman/tasks/<owner>-<repo>-<number>.md`. The frontmatter holds the
state, session ids, PR, validation, review rounds, CI, and decisions. The body is yours,
so you can add notes there.

Foreman rewrites only the frontmatter keys it owns. Keys you add, such as `tags`,
`aliases`, or anything else, stay in place along with their comments.

### Obsidian

The files are ready for Obsidian. On the first interactive `foreman inbox`, Foreman
explains the configured task directory and offers to create it. To use a folder inside
your vault, point `tasks_dir` at it before running inbox:

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
repositories_dir: ~/.local/share/foreman/repositories # inbox source checkouts
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
  on_done: true                   # inbox removes DONE tasks' worktrees
  delete_merged_branch: true      # and the local branch, once the PR is merged
```

`FOREMAN_TASKS_DIR` overrides `tasks_dir`.

Planning, implementation, and review can use separate models. PR creation is part of
implementation. The agent chosen for the task plans and implements; the competing
agent reviews. Run `foreman config --edit` to set the values, then
`foreman config --show` to see what each stage will use. For example:

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
repositories. The filter applies to fresh inbox results and tracked-task refreshes,
including `foreman inbox --non-interactive`. Press `v` in the dashboard or the
interactive inbox to see every repository for the rest of the session; `mentions`
still applies. `foreman status` and the dashboard's delegated work list every task
whatever its repository.

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
