# Pull request

Follow the repository's PR template and rules. Use these defaults where it has none.

- Open the PR only after validation passes. Otherwise report `failed` and explain why.
- Use a conventional title under 72 characters that says what changed.
- Keep the body short, in this order:
  1. `## Problem`: the issue and root cause in two to four sentences. For a feature, say what was missing and why it matters.
  2. `## Fix`: the behavior changed, without a file-by-file tour.
  3. `## Risk`: what could break and where. If risk is low, explain why in one sentence.
  4. `## Review notes`: only decisions, plan deviations, or limits a reviewer needs to know, one line each. Add later developer answers here with `gh pr edit --body` when relevant.
  5. For GitHub issues, `Closes #N`. For Linear issues, follow the repository's link rule.
- Omit test commands, results, reviewer test steps, planning-comment links, and agent attribution. Use short, plain sentences; cut filler, unsupported claims, em dashes, and "This PR...".
