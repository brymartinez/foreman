# Implement

## Working

1. Follow the repository's AGENTS.md, CLAUDE.md, and any relevant skills.
2. Read the issue's comments (`gh issue view <number> --repo <owner>/<repo> --comments`), the posted plan, and work already committed on the branch before you change code. Search for relevant symbols before opening large files; read the sections you need.
3. Follow the posted implementation plan's tasks in order. If you must deviate from the PRD or the plan, say why in the pull request.
4. Match the surrounding code's naming, comment density, and idioms. Reuse existing helpers before adding new ones.
5. Add or update tests when behavior changes.
6. Run focused checks while editing. After the code is complete, run the repository's full supported checks once before you push: tests, typecheck, lint, build, and behavior checks where practical. If a later edit affects a checked path, rerun the affected check.
7. Review your own diff for correctness, regressions, missing tests, and needless complexity before you finish.
8. Treat the developer's answered decisions as settled requirements.
9. Fix every valid review finding and rerun validation. Leave the code alone for a finding you judge invalid, and explain why in your summary. Report "blocked" if a finding needs a product decision.

## Asking

10. Stop and ask only when the choice materially affects product behavior, public API semantics, persistent schema semantics, security or authorization, destructive or irreversible data changes, an important architecture tradeoff, conflicting acceptance criteria, or a requirement you can't safely infer.
11. Ask every decision you already know you need in the same turn. The developer answers them together.
12. Commit work in progress to the branch before you report "blocked".

## Writing

These cover commit messages, code comments, the planning documents, and your final summary.

13. Say what changed and why. Name the function, file, or number. Cut anything that could appear unchanged in another project.
14. One idea per sentence. Active voice. Plain words ("use", not "leverage" or "utilize").
15. No filler ("in order to", "it is important to note"), no hedging, no puffery ("robust", "seamless", "comprehensive", "crucial").
16. No em dashes. Use a period or a comma.
17. Comments explain a non-obvious why. Delete comments that restate the code.
18. Commit subjects use conventional prefixes (`feat:`, `fix:`, `chore:`, `docs:`, `test:`, `refactor:`) and stay under 72 characters.
19. Commits carry no agent attribution. Leave out `Co-Authored-By` trailers for an agent and any "Generated with" or "Powered by" line. Commit under the repository's configured git identity.
20. Lead with the result. The planning documents, commit bodies, and your final summary state the point first and the detail after.
