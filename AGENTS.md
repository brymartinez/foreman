# foreman

A local CLI that delegates GitHub issues to Claude Code or Codex. Design and status:

- `docs/control-layer/progress.md` is the current stage, open work, and next step. Read
  it first. At each stage boundary, move the finished stage's entry to `history.md` and
  update `progress.md`.
- `docs/control-layer/decisions.md` is the append-only log of recent decisions (D1 to
  D100 are in `decisions-archive.md`). Find the last number with
  `grep '^## D' docs/control-layer/decisions.md | tail -1` and append; read an entry
  only when you need it.

Read these only when the task needs them:

- `docs/control-layer/architecture.md`: the package layout and the execution model.
- `docs/control-layer/capabilities.md`: the verified CLI behavior the adapters rely on.
- `docs/control-layer/history.md`: what each completed stage did and how it was verified.

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
make setup                # build, install to ~/.local/bin, copy default stage files
scripts/e2e.sh            # built binary against a local origin, stand-in gh, stub agents
scripts/probe-agents.sh   # re-verify agent CLIs (uses a few cents of model calls)
```
