package agent

import (
	"fmt"
	"strings"
)

func writeReviewScope(s *strings.Builder, b ReviewBrief) {
	s.WriteString("## The change\n\n")
	head, base := b.Head, b.BaseHead
	if head == "" {
		head = "HEAD"
	}
	if base == "" {
		base = "origin/" + b.BaseBranch
	}
	span := base + "..." + head
	if b.PreviousHead != "" {
		span = b.PreviousHead + ".." + head
	}
	if b.Area != nil {
		fmt.Fprintf(s, "Assigned review area: %s\n\nYou own only the files below. Inspect every assigned file. Follow callers, state transitions, authorization, and persistence boundaries as needed to verify their effects; do not reread unrelated parts of the branch. Collect all verified findings before returning.\n\n", b.Area.Name)
		for _, p := range b.Area.Paths {
			fmt.Fprintf(s, "- %q\n", p)
		}
		fmt.Fprintf(s, "\nRead the assigned diff with this single command from the existing working directory:\n\ngit diff %s --", span)
		for _, p := range b.Area.Paths {
			fmt.Fprintf(s, " %s", reviewShellQuote(":(literal)"+p))
		}
		s.WriteString("\n\nUse git diff directly. Do not add cd, git -C, pipelines, or compound shell commands. Use Read, Grep, and Glob for surrounding files. If a command is denied, use those tools instead of abandoning the remaining files.\n\n")
	} else if b.PreviousHead != "" {
		fmt.Fprintf(s, "Start with `git diff %s`. Review the changes since your previous review and verify each of your earlier findings below. Check for regressions caused by the fix. Read surrounding code or the complete diff with `git diff origin/%s...HEAD` when needed to judge a wider effect. Do not repeat a full review of unchanged code without a concrete reason. These findings are checks to verify, not facts to trust.\n\n", span, b.BaseBranch)
	} else {
		fmt.Fprintf(s, "Read the file diff with `git diff %s`. Review the complete diff for correctness, regressions, missing cases, and unnecessary changes.\n\n", span)
	}
	if b.PreviousHead != "" {
		s.WriteString("This is a focused re-review of fixes and their effects. Verify the following previous code findings against the current code; do not repeat a full review of unchanged areas without a concrete regression path. These findings are hypotheses, not facts.\n\n")
		for _, f := range b.PreviousFindings {
			fmt.Fprintf(s, "- %s:%d %s: %s\n", f.File, f.Line, f.Summary, f.Detail)
		}
	}
}

func reviewShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
