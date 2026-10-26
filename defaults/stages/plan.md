# Plan

Write four documents before any code. Each is posted on the issue as its own comment, for the developer and reviewers to read there. Each has one job. Scale each one to the change: a one-line fix gets a few sentences per document, a new feature gets more.

## Style

1. Start each document with `**Summary.**` and one or two plain sentences. A reader who stops there knows what the document decides.
2. Write only what the document's job needs. Cut background the issue already gives and long reasons for choices with no real alternative.
3. Analysis, PRD, and Design may refer to earlier comments instead of repeating them. The implementation plan must stand alone.

## Analysis

The current code, the gap, and the constraints.

4. Read the confirmed triage scope and source links Foreman supplies in the prompt. Ordinary discussion comments do not change the confirmed scope. Do not restate the issue.
5. What the code does today, with `file:line` references where they help a reader find it.
6. The root cause for a bug, or the gap for a feature.
7. Constraints: callers, stored data, config, public API, compatibility.
8. Scope. If the issue holds several independent changes, say so and report "blocked" to ask how to split it.

## PRD

The required behavior.

9. Problem: who is affected and how, in two to four sentences.
10. Goals and non-goals.
11. Requirements, numbered R1, R2, ... Each one must be testable.
12. Acceptance criteria: what a reviewer checks to call the issue done.

## Design

The chosen approach and its meaningful trade-offs.

13. The approach you chose and why. Name an alternative only when it has a real trade-off.
14. The chosen design: components, data flow, error handling, and what stays the same.
15. Testing: the behavior to verify and which requirement it covers.

## Implementation plan

A short, high-level order of work.

16. Start with the goal in one sentence. Include every requirement, constraint, and acceptance criterion the implementer and self-reviewer need; they receive this document without the issue, triage, Analysis, PRD, or Design.
17. Group the work into a few tasks in the order to do them: `### Task N: <component>`. Do not make a task for each file or test.
18. For each task, name the component, the behavior it adds or changes, and the check that verifies it. Give an exact path or API only when it clarifies a constraint.
19. Describe the behavior the tests verify, written first. Do not list every test by name.
20. No line numbers, no commit messages, no code listings. You implement the plan in the next turn.

## Before you report

21. Check every requirement maps to a task, names match across the four documents, and nothing is left as a placeholder or only available through a link to another document. Fix what you find.
22. Build only what the requirements need.
23. Follow the writing rules from the implement instructions in every document.
