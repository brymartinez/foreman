# Implement

## Work

- Follow the repository's AGENTS.md, CLAUDE.md, and relevant skills. Use the implementation plan in Foreman's prompt as the task specification. Inspect committed branch work and nearby code, but do not seek requirements in the issue, triage, or other planning documents.
- Follow the plan's task order. Explain any deviation in the PR. Match local code style and reuse existing helpers.
- For each behavior change, write tests for its acceptance criteria first. Confirm they fail because the behavior is missing, then change the code and rerun the tests until they pass. If a test still fails, check the implementation against the criteria before changing the test. Never weaken an assertion just to pass. Correct a test expectation only when it conflicts with the criteria; explain why in your summary.
- Run the repository's focused checks while editing and after checkpoints, review fixes, answers, and merges. Before the first review, run its full supported suite once: tests, typecheck, lint, build, and practical behavior checks. Do not repeat the full suite for review fixes. Foreman requests another full run after clean review if later changes affect behavior or a validation input. Treat a path as documentation-only only with repository-specific evidence that it cannot affect either; its name or extension alone is insufficient.
- Review your diff for bugs, regressions, missing tests, and needless complexity.

## Correction turns

- Change only what the latest findings require. Do not recover the earlier implementation session or broader task documents.
- Fix valid findings and rerun their focused checks. Explain why you reject an invalid finding. Report `blocked` when a finding is ambiguous or needs a product decision.

## Questions

Ask only about product behavior, public API or persistent schema semantics, security or authorization, destructive or irreversible data changes, important architecture tradeoffs, conflicting criteria, or requirements you cannot safely infer. Ask all known questions together. Commit work in progress before reporting `blocked`.

## Writing

Lead with the result. Say what changed and why, naming concrete files, functions, or numbers. Use short, active sentences and plain words; cut filler, hedging, puffery, and em dashes. Comments explain a non-obvious reason, not the code. Use a conventional commit subject under 72 characters and the configured git identity. Omit agent attribution from commits.
