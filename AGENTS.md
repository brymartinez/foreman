# foreman

A local CLI that delegates GitHub issues to Claude Code or Codex. Design and status:

- `docs/control-layer/progress.md` is the current stage, decisions, and next step. Read
  it first and update it at every stage boundary.
- `docs/control-layer/decisions.md` is the append-only log of every decision point.
- `docs/control-layer/capabilities.md` lists the verified CLI behavior the adapters rely on.
- `docs/control-layer/architecture.md` covers the package layout and the execution model.

## Rules

- Log every decision point in `decisions.md` before committing: behavior, architecture,
  scope, tooling, and any user correction. Supersede old entries; never rewrite them.
- Work one stage at a time. Stop at the stage boundary, and don't build later stages early.
- Each stage leaves working software, tests, and a clean tree.
- Go standard library first. Add a dependency only with a logged decision in decisions.md.
- `internal/app` depends only on the interfaces in `ports.go`.
- Agent output enters the program only through `agent.ParseOutcome`.

## Commands

```bash
go vet ./... && go test ./...
go build -o dist/foreman ./cmd/foreman
scripts/e2e.sh            # built binary against a local origin, stand-in gh, stub agents
scripts/probe-agents.sh   # re-verify agent CLIs (uses a few cents of model calls)
```
