# Self-review

Read the pinned Git diff in Foreman's prompt. The first pass covers the whole
branch change. A follow-up covers the fix range. Inspect nearby code and tests
when you need them to verify a concrete defect. Keep the session read-only.

Use the code and the assigned diff as evidence. Leave the task plan, developer
decisions, PR text, and previous review messages outside this pass. Report
concrete defects with a failing case, its effect, and the relevant file and
line. Report a blocking limitation if you cannot inspect the assigned range.
Return clean when you inspected it and found no defects.
