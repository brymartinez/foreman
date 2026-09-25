# Interactive inbox and task storage setup

## Outcome

`foreman inbox` is an interactive, repeatable picker for GitHub work. A user can
refresh attention items, choose one, select Claude or Codex, and add optional
context before delegation. Task identity is always repo-qualified, so issue
numbers from different repositories cannot be confused.

## Scope

- Show `owner/repo#number` everywhere an inbox item or task is selected.
- Keep Markdown task files as the only storage format. An Obsidian vault is
  supported by choosing its folder as `tasks_dir`; no separate Obsidian API is
  needed.
- On first use, explain the task directory and offer to create it when it does
  not exist. Configuration continues to come from `FOREMAN_CONFIG` and
  `FOREMAN_TASKS_DIR`.
- Make interactive inbox the default. Provide an explicit non-interactive mode
  for scripts and preserve the current report-oriented output there.
- Do not make a second inbox run erase the user's view of previously displayed
  items. Refresh snapshots remain durable, while the interactive picker can
  show all current actionable items again.

## Interaction

1. Refresh GitHub and reconcile tracked tasks.
2. Print grouped, indexed items with repo-qualified identities.
3. Let the user choose an item or quit.
4. Ask for the implementation agent, defaulting to the configured/recommended
   choice only when one exists.
5. Read optional context. An empty answer means no extra context.
6. Call the existing start flow so worktree creation, task persistence, and the
   detached runner have one owner.

Interactive input is line-based so it works in a normal terminal and can be
tested with a buffer. EOF and an explicit quit leave without starting work.
Invalid selections explain the valid range and reprompt. Non-interactive mode
never reads stdin.

## Boundaries and data flow

The GitHub reconciliation model remains unchanged. Rendering owns the
repo-qualified display label. The CLI owns prompting and selection. `app.App`
continues to own task lifecycle operations, and the existing `StartRequest`
receives the selected key, agent, context, and current checkout directory.

The first-run check is a storage setup boundary. It validates that the task
directory is usable or creates it after consent. It does not inspect or modify
Obsidian configuration, vault metadata, or unrelated files.

## Error handling

- A missing task directory is actionable and names the configured path.
- A declined setup exits without changing GitHub or task state.
- A non-directory or inaccessible path fails before GitHub work starts.
- Start failures use the existing CLI error and task-state handling.
- A refresh failure does not enter the picker.

## Verification

- Unit-test repo-qualified labels and repeated reconciliation visibility.
- Test setup acceptance, decline, non-directory, and inaccessible directory
  behavior.
- Test selection, quit, invalid input, agent choice, and context propagation.
- Test non-interactive rendering remains available and does not read stdin.
- Run `go vet ./...`, `go test ./...`, and `go build -o dist/foreman ./cmd/foreman`.

