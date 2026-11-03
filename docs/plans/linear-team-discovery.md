# Linear team discovery and repository selection

## Behavior

Implemented with explicit repository-link inference and manual confirmation.

Configure the Linear team independently of GitHub repositories:

```yaml
linear_team: https://linear.app/acme/team/ENG/overview
```

Discover the authenticated user's open issues throughout that team, including
issues without a project. Do not require team or project mappings to GitHub.
GitHub repository filters must not hide Linear issues before their repository is
known. Retire `linear_teams` with an actionable configuration error.

When an issue is selected for delegation, read its description and comments,
including the triage document. Inference reads explicit GitHub links in the description and marked triage comments.
External triage documents are not fetched automatically; when the available text
does not identify the destination, ask the developer for the repository.

Use explicit repository references and the triage's affected code to propose one
GitHub repository. Show the proposal and supporting evidence for manual starts,
and allow the user to correct it. Never choose a repository just because the issue
belongs to a team or to a particular Linear project.

If no repository is established, references conflict, or the work spans multiple
repositories, ask the user to choose the repository for this delegation. A single
delegation continues to target one repository. Do not introduce coordinated
multi-repository execution as part of this change.

Auto mode applies the existing assignment and triaged-label requirements, then
uses the same repository resolver. It starts only when the evidence establishes
one allowed repository. Otherwise keep the issue visible for manual selection and
explain what needs a decision. Apply repository filters after resolution.

Keep the confirmed repository on the delegated task. Refreshes and restarts must
reuse it and must not silently reassign existing work.

## Implementation

- Separate an undispatched Linear issue's identity from its execution repository.
  Identify it by its stable Linear UUID. The inbox must not manufacture an
  `owner/repo` to satisfy the current task key.
- Keep unresolved inbox entries separate from durable repository-bound task
  records. Reconcile them against existing Linear delegations by UUID, so refresh
  and restart do not offer the same issue as new work in another repository.
- Change the Linear adapter and app port to discover assigned team issues without
  repository mappings. Preserve workspace validation, pagination, and state checks.
- Add repository resolution before agent routing, branch proposal, checkout
  creation, or runner launch. Reuse existing issue/comment and triage readers.
- Resolve explicit repository links in the app and validate the selected repository.
  Use the existing GitHub checkout preparation to verify access. Repository inference
  needs no model call.
- Add a repository confirmation/input step to the dashboard. Reuse the selected
  repository throughout routing, branch selection, and task creation.
- Preserve stored Linear task filenames, keys, branches, and runner arguments.
  Existing delegated tasks require no migration.
- Update config defaults, help, README, and the architecture's discovery,
  identity, filtering, and delegation rules in the implementation pass.

## Verification

- Config accepts a team overview URL without repository mappings.
- Assigned open issues from multiple projects and no project appear in the inbox.
- Repository filters do not remove unresolved Linear issues.
- Explicit repository evidence yields an explainable proposal; missing,
  conflicting, inaccessible, or multiple-repository evidence requires a choice.
- Manual correction controls checkout, branch proposal, and PR destination.
- Auto mode never starts unresolved or disallowed work.
- Refresh and restart preserve confirmed destinations without duplicate tasks.
- Existing GitHub discovery and existing Linear delegations continue to work.
- Run focused app, adapter, config, agent, and dashboard tests; extend the Linear
  end-to-end scenario through selection and delegation; run vet, build, and the
  full Go suite. Report unrelated defaults wording failures separately if they
  remain present.

## Delivery

Implemented after the reported `linear_team` configuration error. Validate the
installed binary and the user's existing config before handoff.
