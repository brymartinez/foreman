# Implement

## Working

1. Follow the repository's AGENTS.md, CLAUDE.md, and any relevant skills.
2. Plan, inspect the code, then implement. Resolve routine ambiguity yourself: if it can be inferred, tested, or fixed safely, do it.
3. Make the smallest change that closes the issue. Leave unrelated code alone.
4. Match the surrounding code's naming, comment density, and idioms. Reuse existing helpers before adding new ones.
5. Add or update tests when behavior changes.
6. Validate with the checks the repository supports: tests, typecheck, lint, build, and behavior checks where practical. Run the narrowest check first, then the full suite once before you push.
7. Review your own diff for correctness, regressions, missing tests, and needless complexity before you finish.
8. Open the pull request only when validation passes. If you can't make it pass, report "failed" and say why.
9. Treat the developer's answered decisions as settled requirements. Inspect work already committed on the branch before continuing.
10. Fix every valid review finding and rerun validation. Leave the code alone for a finding you judge invalid, and explain why in your summary. Report "blocked" if a finding needs a product decision.

## Asking

11. Stop and ask only when the choice materially affects product behavior, public API semantics, persistent schema semantics, security or authorization, destructive or irreversible data changes, an important architecture tradeoff, conflicting acceptance criteria, or a requirement you can't safely infer.
12. Ask every decision you already know you need in the same turn. The developer answers them together.

## Writing

These cover commit messages, code comments, and your final summary.

13. Say what changed and why. Name the function, file, or number. Cut anything that could appear unchanged in another project.
14. One idea per sentence. Active voice. Plain words ("use", not "leverage" or "utilize").
15. No filler ("in order to", "it is important to note"), no hedging, no puffery ("robust", "seamless", "comprehensive", "crucial").
16. No em dashes. Use a period or a comma.
17. Comments explain a non-obvious why. Delete comments that restate the code.
18. Commit subjects use conventional prefixes (`feat:`, `fix:`, `chore:`, `docs:`, `test:`, `refactor:`) and stay under 72 characters.
19. Commits carry no agent attribution. Leave out `Co-Authored-By` trailers for an agent and any "Generated with" or "Powered by" line. Commit under the repository's configured git identity.
