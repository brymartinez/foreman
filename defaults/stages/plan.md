# Plan

Write four documents before any code. Each is posted on the issue as its own comment, for the developer and reviewers to read there. Scale each one to the change: a one-line fix gets a few sentences per document, a new feature gets more.

## Analysis

1. What the issue asks, in your own words, and what the comments add.
2. What the code does today, with `file:line` references.
3. The root cause for a bug, or the gap for a feature.
4. Constraints: callers, stored data, config, public API, compatibility.
5. Scope. If the issue holds several independent changes, say so and report "blocked" to ask how to split it.

## PRD

6. Problem: who is affected and how, in two to four sentences.
7. Goals and non-goals.
8. Requirements, numbered R1, R2, ... Each one must be testable.
9. Acceptance criteria: what a reviewer checks to call the issue done.

## Design

10. Two or three approaches with their trade-offs. Lead with the one you chose and why.
11. The chosen design: components, data flow, error handling, and what stays the same.
12. Testing: what each test proves, and which requirement it covers.

## Implementation plan

13. Start with the goal in one sentence and the files you will create or modify, with each file's job.
14. Split the work into tasks: `### Task N: <component>`, then its files as `Create: path`, `Modify: path:lines`, or `Test: path`.
15. Give each task checkbox steps, one action each: write the failing test, run it and see it fail (the command and the expected failure), make the smallest change that passes, run it again, commit.
16. Name the functions, types, and test cases each task adds. Skip full code listings; you implement the plan in the next turn.
17. Use exact paths and exact commands. No placeholders: no "TBD", no "add error handling", no "similar to Task 2".

## Before you report

18. Check every requirement maps to a task, names match across the four documents, and nothing is left as a placeholder. Fix what you find.
19. Build only what the requirements need.
20. Follow the writing rules from the implement instructions in every document.
