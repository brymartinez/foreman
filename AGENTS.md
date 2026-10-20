# foreman

A local CLI that delegates GitHub issues to Claude Code or Codex.

Read these only when the task needs them:

- `docs/control-layer/architecture.md`: the package layout, the execution model, and
  the design rules with their reasons. Read the relevant design rules before changing
  behavior.
- `docs/control-layer/capabilities.md`: the verified CLI behavior the adapters rely on.

## Rules

- `architecture.md` is the single source of truth for the design. When a change alters
  behavior or a design rule, update it in the same commit; edit the rule in place, don't
  append history.
- Put the reason for a change in the commit message and the PR description, not in the
  docs. Git history is the record of what changed and why.
- Work one stage at a time. Stop at the stage boundary, and don't build later stages early.
- Each stage leaves working software, tests, and a clean tree.
- Go standard library first. Add a dependency only with the user's approval, and list
  it under Tooling in `architecture.md`.
- `internal/app` depends only on the interfaces in `ports.go`.
- Agent output enters the program only through `agent.ParseOutcome`.

## Commands

```bash
go vet ./... && go test ./...   # full supported suite, with scripts/e2e.sh
go test ./internal/app          # focused: the packages you changed
scripts/e2e.sh full_check       # focused: the named scenarios you affected
go build -o dist/foreman ./cmd/foreman
make setup                # build, install to ~/.local/bin, copy default stage files
scripts/e2e.sh            # built binary against a local origin, stand-in gh, stub agents
scripts/probe-agents.sh   # re-verify agent CLIs (uses a few cents of model calls)
```
