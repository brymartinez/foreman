# foreman

A local CLI that hands GitHub or Linear issues to Claude Code or Codex. It gives the
PR back to you once it is ready for your review.

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
if [ ! -e ~/.config/foreman/config.yaml ]; then
  install -m 600 foreman_*_darwin_arm64/config.yaml ~/.config/foreman/config.yaml
fi
cp -n foreman_*_darwin_arm64/stages/*.md ~/.config/foreman/stages/
cp -n foreman_*_darwin_arm64/routing.yaml ~/.config/foreman/   # optional: turns on routing
```

The binaries are not notarized. If you downloaded the archive in a browser, macOS may
block the binary; run `xattr -d com.apple.quarantine ~/.local/bin/foreman` to allow it.

To build from source instead, clone the repository and run:

```bash
make setup
```

`make setup` builds foreman, installs it to `~/.local/bin`, copies the shipping
`defaults/config.yaml` into `~/.config/foreman/config.yaml`, copies the default stage
instructions from `defaults/stages/` into `~/.config/foreman/stages/`, and copies the
starter `defaults/routing.yaml` to `~/.config/foreman/routing.yaml`. It never
overwrites a config, stage, or routing file you already have. When a stage file differs
from the current default, it prints `differs <path>` and the `cp` command that replaces
it. `make install` and `make config` run the
steps separately; set `BIN_DIR` or `CONFIG_DIR` to change where they go.

## Usage

Foreman is a dashboard. Everything you do with a task happens there.

```bash
foreman                         # dashboard for default_source (GitHub unless set)
foreman -a                      # auto mode on for this launch
foreman --no-auto               # auto mode off for this launch
foreman -s github               # show GitHub issues and delegated tasks
foreman --source linear         # show Linear issues and delegated tasks
foreman -s all                  # show both trackers
foreman --mentions              # show mention-only items for this launch
foreman -c                      # edit config in $VISUAL, $EDITOR, or vi
foreman knowledge export owner/repo -o knowledge.md
foreman knowledge ingest knowledge.md --dry-run
foreman --help
foreman --version
```

Dashboard launch flags override a setting for one launch and save nothing:

| Launch override | Effect |
|---|---|
| `-s`, `--source github\|linear\|all` | Select the issue tracker for INBOX, DELEGATED WORK, and auto mode's starts. `all` selects both. Overrides `default_source`. |
| `-a`, `--auto` | Turn auto mode on. Overrides the choice saved with `a`. |
| `--no-auto` | Turn auto mode off. Overrides the choice saved with `a`. |
| `--mentions` | Show items found only because they mention you. Overrides `mentions`. |
| `--no-mentions` | Hide those items. Overrides `mentions`. |

Conflicting flags, such as `--mentions --no-mentions` or `-s github -s all`, are a
usage error. The other flags are inputs to one command:

| Command | Flags |
|---|---|
| `foreman -c` | Open the config editor. Use it alone, without launch overrides. |
| `foreman knowledge export owner/repo` | `-o`, `--output FILE` |
| `foreman knowledge ingest FILE` | `--repo owner/repo`, `--rebind`, `--agent claude\|codex`, `--dry-run` |
| `foreman -h`, `--help` | Print command help. |
| `foreman --version` | Print the version. |

Precedence for the dashboard's source and mentions is the launch flag, then
`default_source` or `mentions` in `config.yaml`, then the built-in default
(`github`, and mention-only items shown). Auto mode has no config key: the launch
flag wins, then the choice saved by pressing `a`, and with no saved choice auto mode
is off. Every other setting lives in `config.yaml` or `routing.yaml` and has no
launch flag.

`-c` opens the config even if it cannot load. A missing or empty config is filled
with defaults before the editor opens. Foreman checks it again when the editor exits.

`knowledge export owner/repo` writes to stdout unless you pass `-o FILE` or
`--output FILE`. `knowledge ingest FILE` accepts `--dry-run`, `--agent claude|codex`,
and `--repo owner/repo`. Use `--rebind` with `--repo` to import an export under a
different repository. See [Moving knowledge between machines](#moving-knowledge-between-machines).

## Dashboard

Run `foreman` with no arguments to open a live view. This example shows the log after
pressing Enter on a delegated task:

```
foreman · 2 delegated · 3 in the inbox · auto on 1/3 · refreshed 10:01:22
▸ INBOX                           │ DELEGATED WORK
> midsoln/foreman#1  Be interactive │ testorg/example#340   Codex   READY_FOR_HUMAN
    assigned to you                │   Cache fleet lookups
                                   │   PR #912 · reviewer: Claude · CI ✓ · review ✓
  testorg/example#351  Retry 5xx  │
    mentions you                   │ > midsoln/foreman#2      Claude  IMPLEMENTING
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
`s`, with a log open, to swap it for everything recorded for the task: its state,
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
| `enter` on an issue | delegate it: pick `1` for Claude, `2` for Codex, or `3` to route it, type an optional context file, then review and edit the proposed branch. A `FAILED` issue starts again on its stored branch |
| `↑` `↓`, `pgup` `pgdn` in the branch prompt | scroll the proposal rationale; the branch field stays visible |
| `←` `→`, `home` `end`, `backspace` `delete` in the branch prompt | move the branch cursor or remove a character; typing inserts at the cursor |
| `enter` on a task | open its log below, or the waiting question on a `NEEDS_DECISION` task |
| `c` | cancel the highlighted delegated task, after a `y`/`n` confirm |
| `x` on a `FAILED` task | confirm a reset of its local run, then offer the issue for a manual start |
| `a` | turn auto mode on or off; the choice is kept for the next launch |
| `r` | refresh the selected issue sources now |
| `p` | open merged and closed PR history; Enter or Tab focuses its log, and Esc returns |
| `space` with a log open | pause or resume log redraws; any navigation key resumes |
| `u` `d` with a log open | scroll up or down one page |
| `ctrl+u` `ctrl+d` with a log open | scroll up or down half a page |
| `g` `G` with a log open | jump to the log's first line or follow the tail |
| `R` with a log open | switch between cleaned and raw log lines |
| `s` with a log open | switch between the log and everything recorded for the task |
| `pgup` `pgdn`, `end` | optional log scroll keys for keyboards that have them |
| `esc` | close the log or abandon a prompt |
| `?` | show the keys and the version |
| `q`, `ctrl+c` | quit |

The branch prompt shows the complete proposal rationale, including repository guidance.
Its heading marks proposals that need your choice. The line range shows where you are
when the rationale is longer than the screen. The branch field scrolls horizontally
around the cursor for long names. Press Enter to start with the edited name, or Escape
to cancel.

Delegated work is re-read from the task files every second, so a state change shows up
on its own. The selected issue sources refresh every `refresh` seconds, 60 by default,
and when you press `r`. A refresh runs the searches, the readiness gate, and the DONE cleanup. The
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

Auto mode is off until you turn it on with `a` or `-a`. While it is on, it starts open issues
assigned to you that foreman has never delegated, from the selected source, oldest first, with `auto.agent` and
no context file. It looks after the first refresh, after each successful refresh, and
when a run ends. Only issues with the exact `triaged` label
start automatically; manual starts also require the exact `triaged` label. Mentions, FAILED
tasks, and previously REVIEWED tasks never start on their own. At most `auto.max_concurrent`
runs are active at once, and
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

For GitHub repositories, the inbox searches for open issues assigned to you, open
issues and PRs that mention you, and PRs that request your review. For repositories
mapped to Linear, it lists open,
nonterminal issues assigned to the Linear user named by `LINEAR_API_KEY`. INBOX groups
delegatable issues under GITHUB and LINEAR, with
what changed since the last refresh beneath each one. Each search returns up to 1000
results.

Set `mentions: false`, or launch with `--no-mentions`, to leave out the items found only because they mention you, in the
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
state, and retries next time. An undelegated issue that no longer exists or is
inaccessible in its source is dropped and not checked again. Failed PR or CI lookups
are counted as warnings in the header.

Local builds show the commit in `foreman --version` and in the `?` help, for example
`foreman 0.1.0-dev (72a70ce12345-dirty)`. The `dirty` suffix means the build included
uncommitted changes. Builds without Git metadata show `unknown`; release builds
show their stamped version.

INBOX shows repo-qualified identities such as `acme/widgets#825`. When you delegate
one, Foreman clones or reuses its source checkout under `repositories_dir`, then
creates the agent worktree under `worktrees_dir`, so you can run `foreman` from any
directory.

The optional context file is available to routing and remains on the task record. It
is not passed to planning or implementation; put requirements into the issue and
confirmed triage decisions before delegation.

### What a start does

1. For a new task, it fast-forwards the managed checkout's default branch from
   origin, then proposes a branch from the issue and repository guidance in
   `AGENTS.md` and the files it names as branch naming guidance (for example
   "Branch naming: see `docs/branches.md`"). Other linked documents are not read.
   If the checkout cannot fast-forward safely, the start stops and says why.
   Manual starts show the name for editing;
   auto mode waits if the workstream or guidance is unclear. Foreman checks the chosen
   name, then creates the branch and worktree. A branch named
   `fix/825-provider-retries` uses
   `<worktrees_dir>/<owner>-<repo>/fix/825-provider-retries`. Each slash in the branch
   name separates directories. Existing tasks keep their stored branch and worktree
   path, including paths from before this convention.
   Without repository guidance, the default is `fix/825-provider-retries` for a fix
   issue; its slug is capped at 5 words.
   - The prefix is a conventional-commit type. Foreman takes it from the title's own
     prefix (`fix: …`), otherwise from a label (`bug` → `fix`, `documentation` →
     `docs`, …), otherwise it uses `feat`.
   - For a Linear issue without repository guidance, Foreman uses Linear's suggested
     branch when available. Repository conventions take precedence over that suggestion.
2. It launches a detached runner that logs to `~/.local/share/foreman/logs/`. The
   runner keeps going after you quit the dashboard.
3. The runner has the agent implement the issue, run validation, push, and open a PR
   with `gh pr create`. Foreman then checks with GitHub that the PR exists and targets
   the right branch. When screenshots are requested or the agent captured them, the
   opening agent attaches them in a PR comment after the PR exists, or reports why it
   could not. It uses the repository's documented setup and sample data when taking
   screenshots; Foreman does not create sample data. It leaves the PR description as
   written for review.
4. The implementing agent first reviews its own PR in a fresh, read-only session
   using the checked Requirements and Work plan and the file diff. The other agent reviews
   the diff in its own fresh, read-only session. When claim verification is enabled,
   both also receive the PRD and claims. Neither receives the PR description, triage
   discussion, comments, or prior review rounds as review requirements. Findings
   start a fresh correction session with only the latest findings as task context.
   Each fix gets a new self-review before the next
   competing review. The loop repeats until the competing review is clean. A
   self-review that errors fails the task.
   Both reviews read code and tests without running validation commands. The
   implementer runs those commands; inability to rerun them in review alone does not
   request changes.
   The implementer runs the repository's full documented suite before the first
   review and focused checks for each fix. Before handoff, Foreman needs a full-suite
   result that covers the final commit. If a fix changed behavior or a validation
   input since the last full run, the implementer runs the suite once more, and a fix
   it commits then is reviewed again. A repository with no identifiable full suite is
   handed off with the agent's explanation of what it could not verify.
   Foreman has no `wait_for_pr` flag or config key: the first push and PR happen
   before this review. Fixes found during review are committed and pushed to the
   same PR before the next review pass.
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
6. Every review records the commit and description it covered. If the PR gets new
   commits or a new description after a clean review, the next refresh has both
   reviews look at it again before the task is
   `READY_FOR_HUMAN`. Once it is, the PR is yours: new commits are flagged but not taken
   back. A runner that crashes during a review picks up at the review when you start
   the `FAILED` task again from the inbox.

## Planning

Before any code, the implementer writes two short documents: Requirements (`prd`)
and Work plan (`plan`). `plan.md` sets what each one covers and asks for B1 English:
common words and short sentences. Foreman reads
`## Triage decisions` and later `## Triage correction` comments in order; if there
are none, the issue description is the confirmed scope. It gives that scope and its
source links to the planner, checks the documents against it, then posts each as an
issue comment. If the checker finds a concrete mismatch in a new plan, Foreman sends
the findings back to the same planning session for up to three revisions. Only changed
documents need to be returned; Foreman checks the combined set each time. Remaining
findings then pause for a decision. Existing plans are checked before adoption unless
the exact scope and document texts already passed the check. The checker compares the
supplied text without reading the repository and reports only mismatches. An unreadable
or inconclusive check fails with a reason; a nonblocking limitation on a clean
check does not stop planning. Requirements says what must work and how to check it.
Work plan gives ordered tasks that refer to requirement IDs without copying the rules.
Foreman checks the two documents together against triage. The implementer and
self-review receive both; the competing reviewer receives the diff (and Requirements
when claim verification is on). A changed scope or document invalidates earlier
checks before handoff.

The `s` view links the two comments. A task that plans again, for example after a
failed run, edits the same comments instead of adding new ones. A task that already
has an open PR but no complete plan runs planning before reviewing that PR.

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
| `plan` | the implementer, for the two planning documents | `~/.config/foreman/stages/plan.md` | `.foreman/plan.md` |
| `implement` | the implementer | `~/.config/foreman/stages/implement.md` | `.foreman/implement.md` |
| `review` | the self-review and the competing reviewer | `~/.config/foreman/stages/review.md` | `.foreman/review.md` |
| `pr` | the implementer, for the PR description | `~/.config/foreman/stages/pr.md` | `.foreman/pr.md` |

- The four global files are required. The dashboard won't open when one is missing,
  and says which. `make config` installs the defaults from `defaults/stages/`, and the
  release archives carry them in `stages/`. Edit your copies freely.
- Repo files are optional.
- When both files exist, the agent gets both: global first, then the repo's more
  specific rules.
- The repository's `AGENTS.md` and PR template take precedence over the global
  `pr.md` defaults when they give different title or body rules.
- The checked Requirements and Work plan are the implementer's task specification. Answers
  to later blocking questions take precedence over stage instructions.
- Tasks read `.foreman/` from their worktree, so only the version committed on the base
  branch counts.
- A GitHub issue PR body includes `Closes #N`; a Linear issue PR links its issue URL.
  A review always ends in foreman's JSON format.
  Otherwise the text is passed through as written.

## Cleaning up worktrees

When a refresh finds a task DONE, it removes the task's worktree. When the PR was
merged, it also deletes the local branch. A PR_CLOSED task keeps its worktree and
branch. A FAILED task also keeps them so you can retry the run.

To discard a FAILED run, select it in DELEGATED WORK and press `x`. Foreman shows
the task file, runner log, worktree, and local branch it will remove. Press `y` to
confirm. The replacement task record keeps the PR link, and Foreman offers the
issue for a manual start. The reset does not change GitHub; inspect the branch for
uncommitted work before confirming.

- DONE cleanup keeps a worktree with uncommitted or untracked changes. A later
  refresh removes it once it is tidy. A confirmed reset of a FAILED run removes
  its worktree even when it has such changes.
- A branch whose PR wasn't merged is kept, so a new start can pick the work up again.
- Remote branches are left to GitHub's "Automatically delete head branches" setting.

Turn either behavior off in the config, under `cleanup`.

## When the agent needs a decision

Agents make routine calls themselves. When something needs your judgment, such as a
product choice or conflicting requirements, the agent stops and returns one or more structured
questions. The task becomes `NEEDS_DECISION`, and the agent process ends. Nothing waits in
a terminal.

Foreman posts to a Discord webhook when a task needs a decision, fails or is cancelled,
becomes ready for review. Each post names the alert, the task
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
and, once all are answered, resumes the current stage's session when one exists.
After review findings it starts a fresh correction session with the findings and
your answer. The answer also appears in the PR description.

The implementer and reviewers take turns: every fix is reviewed again. Fixes asked
for by the self-review and the competing review share one limit (`fix_rounds`). If the
competing review after the third fix still has findings, foreman asks the same way: do
another round, or accept the remaining findings. If the self-review is the one still
finding problems, foreman never offers to accept them. It first diagnoses why, then
asks you to do another round or stop the task:

- a missed generic practice: the findings also go to learning;
- a conflict in the stored task evidence (issue, plan, context file, your
  answered decisions, PR description, `AGENTS.md` or `CLAUDE.md`, the implement stage
  file, repository knowledge): each statement is
  quoted with its source, and you pick which one governs. Foreman shows a conflict only
  when every quote is in its source;
- no concrete conflict found: the question says so.

The implementer gets the findings along with your answer, and the next change gets a
fresh self-review. Stopping fails the task with the open findings and leaves the PR
and branch as they are.

With `review_before_pr: true`, a new task is reviewed before its PR opens, so
automated reviews that run on new PRs see the finished change. The implementer
commits locally and proposes a PR title and description. Both reviews read that
description and the local diff, and fixes stay local. After a clean review, the
implementer pushes the reviewed commit and opens the PR with the reviewed description,
and foreman checks that the PR matches. Requested screenshots go in a PR comment
after it opens; they never alter the reviewed description. If the fix limit is reached
first, you choose another round or stopping the task without a PR. A task keeps the
setting it started with, including when you restart it.

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
finished CI makes the task `READY_FOR_HUMAN`, and with CI still running it waits in
`WAITING_FOR_CI`. A clean review of an earlier head or description starts a run that
reviews the PR again. Without a clean review, the task shows the stage it needs next and
waits for a start.

If the agent does not stop at once, the task is `PR_CLOSED` but keeps its runner, and
every refresh tries again.

## States

| State | Meaning | Your move |
|---|---|---|
| `PLANNING` | The implementer writes Requirements and Work plan, and foreman posts them | Wait, or `c` on the task to stop it |
| `IMPLEMENTING` | The implementer changes the branch: code, review fixes, answers, or a merge | Wait, or `c` on the task to stop it |
| `SELF_REVIEW` | The implementer reviews its own change in a fresh session | Wait, or `c` on the task to stop it |
| `ADVERSARIAL_REVIEW` | The competing agent reviews the change | Wait, or `c` on the task to stop it |
| `OPENING_PR` | With `review_before_pr`, the implementer pushes the reviewed commit and opens its PR | Wait, or `c` on the task to stop it |
| `WAITING_FOR_CI` | The PR passed the adversarial review; foreman waits for CI | Wait |
| `NEEDS_DECISION` | The agent asked a question | Enter on the task to answer |
| `READY_FOR_HUMAN` | Review clean, CI finished (✓ or ✗ shown) | Review and merge the PR |
| `DONE` | PR merged or issue closed | Nothing |
| `FAILED` | The agent failed, the run was cancelled, or the runner died | `s` on the task to see why, then Enter on it in the inbox to retry or `x` on the task to reset its local run |
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

## Learning

Foreman evaluates each task once it has a clean review, or once its self-review
reaches the fix limit on a missed generic practice, and again when new evidence
arrives: a later review, an answered decision, a PR comment, a new PR head, or the PR
merging or closing. CI results are not evidence. The evaluation is one turn with the
task's implementer agent, with no tools, run by a detached `foreman __learn` process.
It reads the task file, the PR's comments from people (not bots), and the
repository's current learnings and recent evaluations, never the logs or the agent
transcripts. A turn that calls a tool anyway is discarded. Its output goes to
`<log_dir>/learning.log`. An evaluation that fails
never changes the task or sends an alert.

What it learns is kept per repository under `knowledge_dir`:

```
~/.local/share/foreman/knowledge/<owner>/<repo>/
  knowledge.md            active learnings, for people and prompts
  learnings.yaml          every learning with its status, evidence, and history
  evaluations/<task>.yaml each task's assessment, signals, and proposals
  attempts/<task>.yaml    the last evaluation attempt
  learning.lock           one evaluation per repository at a time
```

A learning becomes active when the developer backs it, through a decision answer or a
PR comment, or when two tasks support it. The evaluator must cite the decision or
comment by id; a claim without a real citation from the task counts as one task's
observation. One task's observation stays a candidate.
The developer's correction retires or rewrites a learning at once; observed work does
so only from a newer task. Learnings are short and generic: foreman drops any with
issue or PR numbers, issue keys such as `ABC-12`, commit hashes, URLs, emails, absolute paths, or secret-shaped
text. Paths inside the repository are kept.

The separate triage skill can use repository knowledge while researching an issue.
Foreman's diagnostic turn at a self-review fix limit can also use up to 8 active
learnings and 2000 characters from the task's repository. Planning, implementation,
correction, and review prompts omit that knowledge.

Set `learning.enabled: false` to stop new evaluations. Triage and fix-limit diagnosis
can still use what is already learned. To forget a repository's knowledge, delete its directory. To remove
one learning, delete its entry from `learnings.yaml`. Prompts read that file, and the
next evaluation rewrites `knowledge.md`.

### Moving knowledge between machines

`foreman knowledge export owner/repo -o knowledge.md` writes the repository's
knowledge to one Markdown file: the active guidance to read, then a versioned YAML
block with every learning (active, candidate, and retired), its evidence and
history, and every evaluation. Without `-o` it writes to stdout.

`foreman knowledge ingest knowledge.md` stores it under the repository the export
names. `--repo owner/repo` must name the same repository unless you add `--rebind`.
A file that is not a valid export changes nothing.

- If this machine has no knowledge for the repository, the export is restored as it
  is, with no agent call.
- If both machines have learnings, one tool-less agent turn groups equivalent
  learnings, and foreman combines their evidence. The agent is `auto.agent` unless
  you pass `--agent claude` or `--agent codex`; a Claude turn is capped at
  `learning.budget_usd`.
- Ingesting the same export again changes nothing and calls no agent. The same task
  on both machines counts once; a different task under the same issue number keeps
  its own evidence under a renamed id.
- A maintainer's correction beats observed work. When two maintainer corrections
  disagree, the later wins if they were recorded at least a minute apart. Otherwise the ingest
  stops, prints both, and saves nothing.
- `--dry-run` prints the resulting `knowledge.md` without saving.

The ingest replaces `learnings.yaml`, `evaluations/`, and `knowledge.md` together. If
it is interrupted after it commits, the next learner, refresh, export, or ingest
finishes it. An export waits for no one: it fails while an evaluation holds the
repository's lock, so run it again. It does not touch tasks, worktrees, config, `attempts/`, or the lock, and
it fails without saving when an evaluation runs at the same time.

`knowledge.md` is the contract for other tools, such as the `foreman-triage` skill.
It starts with a `# Repository knowledge` heading and a line saying the learnings are
fallible, then has up to three sections, `## Conventions`, `## Repository facts`,
and `## Working practices`, each a list of one-sentence bullets. It holds no ids,
issue or PR numbers, or evidence. A reader should pick the file by the verified target
repository, use the bullets that fit the issue, and not publish the file wholesale.

## Configuration

Foreman reads `~/.config/foreman/config.yaml`, or the file named in
`FOREMAN_CONFIG`. Run `foreman -c` to create or edit your file. Every key is
optional; unset keys keep the built-in defaults below, even when no shipping config
has been installed. A key foreman does not know, such as a misspelling, stops it
with an error that names the key and its line.

The built-in defaults are not the same file as the shipped `defaults/config.yaml`,
which `make setup` installs when you have no config. The shipped file chooses some
different values on purpose: `mentions: false`, `opus` and `sonnet` Claude models,
Codex plan and reasoning settings, and $25 and $10 budgets. It leaves the webhook
URL, repository filters, and Linear project mappings empty.

Built-in defaults:

```yaml
tasks_dir: ~/.local/share/foreman/tasks
worktrees_dir: ~/.local/share/foreman/worktrees
repositories_dir: ~/.local/share/foreman/repositories # source checkouts for delegated work
log_dir: ~/.local/share/foreman/logs
knowledge_dir: ~/.local/share/foreman/knowledge # what foreman learns, per repository
review_rounds: 3                     # passes over the whole PR per self-review and per review
fix_rounds: 3                        # fixes asked by either review before foreman asks you
review_before_pr: false              # open the PR before review; set true to review locally first
mentions: true                       # show items found only because they mention you
default_source: github               # dashboard issue source: github, linear, or all
refresh: 60                          # seconds between dashboard issue-source refreshes
auto:                                # on/off is not here; press a or launch with -a
  agent: claude                      # claude or codex
  max_concurrent: 3                  # active runs, manual ones included
budgets:                             # Claude only: Codex reports no cost, so it has no cap
  implementer_usd: 3.00              # Claude maximum per implementation turn
  reviewer_usd: 1.00                 # Claude maximum per review, self-review, or diagnosis turn
learning:
  enabled: true                      # evaluate finished tasks; off keeps existing knowledge in prompts
  budget_usd: 0.50                   # Claude maximum per evaluation; 0 sets no cap; Codex has none
notifications:
  enabled: true                      # decision, failure, and readiness alerts
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
linear_projects: {}                   # GitHub owner/name -> Linear project URL
cleanup:
  on_done: true                   # each refresh removes DONE tasks' worktrees
  delete_merged_branch: true      # and the local branch, once the PR is merged
```

`FOREMAN_TASKS_DIR` overrides `tasks_dir` after Foreman reads the file. Directory
values beginning with `~/` expand to your home directory. The `stages/` directory
and `routing.yaml` sit beside the selected config file; neither has a separate
config key. Auto mode's on/off choice lives in
`~/.local/share/foreman/state.json`, outside `config.yaml`.

To use Linear issues for a repository, map it to one Linear project URL and set a
personal API key before starting Foreman:

```yaml
linear_projects:
  acme/web: https://linear.app/acme/project/web-site-123
```

```bash
export LINEAR_API_KEY=your-personal-linear-api-key
foreman
```

Repositories without a mapping continue to use GitHub issues. A Linear project can
belong to only one repository. Set `default_source: linear` or `all` to show Linear
issues by default, or pass `-s linear` or `-s all` for one launch. `foreman -s github`
does not need `LINEAR_API_KEY`. If the selected source is `linear` or `all` and a
visible repository is mapped, foreman stops at startup until the key is set.
Foreman reads and comments on Linear issues but does not change their status.
Branches and pull requests stay on GitHub. A Linear task's PR title includes its
Linear identifier, and its body links to the Linear issue instead of using a GitHub
`Closes #N` line. New Linear tasks use Linear's suggested branch name when it is
available. If Linear returns no name, Foreman uses its conventional type prefix.

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
the selected issue sources whenever you ask.

`include_repositories` and `exclude_repositories` accept exact repository names such as
`acme/widgets`. A nonempty include list limits the inbox to those repositories and the
exclude list is then ignored. With an empty include list, the exclude list hides its
repositories. The filter applies to fresh inbox results and tracked-task refreshes
from GitHub and Linear alike. DELEGATED WORK lists only tasks from the source `-s`
selects. Hidden tasks keep running, count toward `auto.max_concurrent`, and show again
in a dashboard that includes their source. A failed refresh of either selected
source keeps results from the successful source visible with a warning and pauses all
automatic starts until both sources refresh successfully.

## Routing

`routing.yaml`, next to the selected `config.yaml`, lets foreman choose the agent
and models for each new issue. Without it, routing is off. `make setup` installs
the starter file without replacing an existing one. For a release archive, copy its
`routing.yaml` to the same directory as `config.yaml` to enable routing. The starter
file is:

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

Design references are in [`docs/control-layer/`](docs/control-layer/):

- [architecture.md](docs/control-layer/architecture.md) covers the package layout, the
  execution model, and the design rules with their reasons.
- [capabilities.md](docs/control-layer/capabilities.md) lists the verified CLI
  behavior.
