# Plan

Write four documents before any code. Each is posted on the issue as its own comment, for the developer and reviewers to read there. Scale each one to the change: a one-line fix gets a few sentences per document, a new feature gets more.

## Style

1. Start each document with `**Summary.**` and two or three plain sentences. A reader who stops there knows what the document decides.
2. Keep what the implementer and reviewer need: `file:line` references, constraints, decisions, and test steps.
3. Cut background the issue already gives, context an earlier document already covers, and long reasons for choices with no real alternative. Refer to an earlier document by name instead of restating it.

## Analysis

4. Read the issue and every comment (`gh issue view <number> --repo <owner>/<repo> --comments`). Then say what the issue asks, in your own words, and what the comments add.
5. What the code does today, with `file:line` references.
6. The root cause for a bug, or the gap for a feature.
7. Constraints: callers, stored data, config, public API, compatibility.
8. Scope. If the issue holds several independent changes, say so and report "blocked" to ask how to split it.

## PRD

9. Problem: who is affected and how, in two to four sentences.
10. Goals and non-goals.
11. Requirements, numbered R1, R2, ... Each one must be testable.
12. Acceptance criteria: what a reviewer checks to call the issue done.

## Design

13. Two or three approaches with their trade-offs. Lead with the one you chose and why.
14. The chosen design: components, data flow, error handling, and what stays the same.
15. Testing: what each test proves, and which requirement it covers.

## Implementation plan

16. Start with the goal in one sentence and the files you will create or modify, with each file's job.
17. Group the work into a few cohesive tasks: `### Task N: <component>`, then its files as `Create: path`, `Modify: path:lines`, or `Test: path`. Do not make a task for each file or test.
18. For each task, name the behavior, the focused check that verifies it, and a commit boundary. Add or update tests for changed behavior. Save full repository validation for the completed implementation unless an earlier full check is needed to diagnose a failure.
19. Name the functions, types, and test cases each task adds. Skip full code listings; you implement the plan in the next turn.
20. Use exact paths and exact commands. No placeholders: no "TBD", no "add error handling", no "similar to Task 2".

## Before you report

21. Check every requirement maps to a task, names match across the four documents, and nothing is left as a placeholder. Fix what you find.
22. Build only what the requirements need.
23. Follow the writing rules from the implement instructions in every document.
