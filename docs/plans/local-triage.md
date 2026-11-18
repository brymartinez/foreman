# Standalone triage and local planning

Run `foreman-triage` in Claude Code or Codex. The skill publishes the confirmed
decisions and applies the `triaged` label. Foreman reads that record on start.
GitHub receives the PR with a change summary and verification.

## Scope and readiness

- Use a valid existing local triage record when one exists at the first start.
  Otherwise require the exact tracker label and read confirmed tracker scope.
- Keep the selected triage source across restarts. An invalid local record
  blocks a start that would use it. Existing local records stay in
  `triage_dir` outside project checkouts.
- Use tracker repository links and confirmed triage to propose the GitHub
  repository for a Linear issue. Ask for a repository when links are ambiguous.

## Planning and review

- Save Requirements, a short ordered Implementation plan, and review findings
  in the local task record for new runs.
- Give the next agent the checked plan. If a step needs to change, send it
  back to the planner. Recheck the plan after changes to triage or the issue
  description and require fresh review coverage where needed.
- Keep the recorded document sources for existing runs.
