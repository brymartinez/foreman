# Interactive Inbox Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `foreman inbox` an interactive, repeatable repo-qualified picker with first-run task-directory setup while preserving a scriptable report mode.

**Architecture:** Keep Markdown files as the storage backend and keep reconciliation in `internal/app`. Add a small inbox view model containing all current actionable items, a CLI prompt layer with injected readers/writers, and reuse `App.Start` for delegation. The setup check remains at the command boundary before GitHub work begins.

**Tech Stack:** Go standard library, existing `internal/app`, `internal/cli`, `internal/render`, and Markdown store.

---

### Task 1: Define command modes and repo-qualified identity

**Files:**
- Modify: `internal/cli/cli.go`
- Test: `internal/cli/cli_test.go`
- Modify: `internal/render/render.go`
- Test: `internal/render/render_test.go`

- [x] Write failing parser tests for default interactive inbox and explicit non-interactive mode.
- [x] Run the focused CLI tests and verify the new mode fails before implementation.
- [x] Add the smallest command field and flag parsing needed for the two modes.
- [x] Add a shared repo-qualified identity formatter and use it in inbox output.
- [x] Run focused CLI and render tests.

### Task 2: Return a repeatable interactive inbox view

**Files:**
- Modify: `internal/app/inbox.go`
- Test: `internal/app/inbox_test.go`

- [x] Write a failing test proving a second refresh still exposes current actionable items.
- [x] Run the focused app test and verify the expected failure.
- [x] Add an `All` or equivalent current-item collection to `InboxReport` without changing durable snapshot semantics.
- [x] Populate it in stable priority order and keep existing report sections for non-interactive output.
- [x] Run focused app tests and the full app package tests.

### Task 3: Add testable interactive selection and delegation input

**Files:**
- Create: `internal/app/inbox_interactive.go`
- Test: `internal/app/inbox_interactive_test.go`
- Modify: `cmd/foreman/main.go`

- [x] Write failing tests for selection, invalid input retry, quit, agent choice, and optional context.
- [x] Run the focused interactive tests and verify they fail for missing behavior.
- [x] Implement line-based prompting through `io.Reader` and `io.Writer`, returning a selected key, agent, and context.
- [x] Wire the default inbox command to refresh, prompt, and call `App.Start` with the current directory.
- [x] Preserve non-interactive rendering and ensure it does not read stdin.
- [x] Run focused interactive tests.

### Task 4: Make first-run task storage explicit

**Files:**
- Create: `internal/config/setup.go`
- Test: `internal/config/setup_test.go`
- Modify: `cmd/foreman/main.go`
- Modify: `README.md`

- [x] Write failing tests for accepted creation, declined creation, existing directory, non-directory, and inaccessible path.
- [x] Run focused setup tests and verify the expected failures.
- [x] Implement setup with explicit path messaging and no Obsidian-specific API.
- [x] Call setup before GitHub work for inbox and document the configuration paths.
- [x] Run focused setup and command tests.

### Task 5: Update stage evidence and verify the real artifact

**Files:**
- Modify: `docs/control-layer/progress.md`
- Modify: `docs/control-layer/decisions.md`

- [x] Record the completed Stage 7 behavior and validation evidence.
- [x] Run `go vet ./...`.
- [x] Run `go test ./...`.
- [x] Run `go build -o dist/foreman ./cmd/foreman`.
- [x] Inspect `git diff --check` and the final diff.
