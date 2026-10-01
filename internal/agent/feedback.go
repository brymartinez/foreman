package agent

import (
	"fmt"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

// FeedbackItem is one comment on the pull request the agent has to answer. Path, Line,
// and InReplyTo are set for an inline comment, and decide where the reply goes.
type FeedbackItem struct {
	Kind      task.CommentKind
	ID        int64
	Author    string
	Body      string
	URL       string
	Path      string
	Line      int
	InReplyTo int64
}

// FeedbackMessage resumes the session that wrote the pull request so it answers the
// comments on it. The agent replies where each comment is, so the conversation stays
// where the developer left it (D118).
func FeedbackMessage(repo task.Repo, pr int, items []FeedbackItem) string {
	var s strings.Builder
	fmt.Fprintf(&s, "Pull request #%d has %s you have not answered.\n\n", pr, countText(len(items)))
	feedbackRules(&s, repo, pr)
	writeFeedback(&s, repo, pr, items)
	fmt.Fprintf(&s, "\nWhen every comment is answered, commit and push anything you changed, then end with the JSON outcome \"pr_opened\" for PR #%d and the validation you ran. Use \"blocked\" if a comment needs a decision from the developer, and \"failed\" if you cannot finish.\n", pr)
	return s.String()
}

func countText(n int) string {
	if n == 1 {
		return "1 comment"
	}
	return fmt.Sprintf("%d comments", n)
}

// feedbackRules are the same for a resumed turn and a fresh one, so the agent is held
// to one contract whichever way it is told about the comments.
func feedbackRules(s *strings.Builder, repo task.Repo, pr int) {
	fmt.Fprintf(s, `Answer every one of them before you finish this turn:

- Decide whether the comment needs a code change, and make the change when it does.
- Reply where the comment is, with the command under it. Say what you changed, or say that nothing needed to change and why. Answer every comment, including the ones you agree with.
- Start every reply with the line %s on its own. It is how foreman tells your replies from the developer's comments, and leaving it out makes foreman ask you to answer your own reply.
- If a reply command fails, post the answer on the pull request instead:
  gh api --method POST repos/%s/issues/%d/comments -f body="..."
- Do not resolve the threads yourself. The developer decides what is settled.

`, ReplyMarker, repo, pr)
}

func writeFeedback(s *strings.Builder, repo task.Repo, pr int, items []FeedbackItem) {
	for i, it := range items {
		fmt.Fprintf(s, "### %d. %s, %s\n%s\n\nReply with:\n  %s\n\n%s\n\n", i+1, author(it), where(it), it.URL, replyCommand(repo, pr, it), quote(it.Body))
	}
}

func author(it FeedbackItem) string {
	if it.Author == "" {
		return "someone"
	}
	return it.Author
}

// where says which part of the pull request the comment sits on, so the agent can find
// the code it is about without re-reading the whole diff.
func where(it FeedbackItem) string {
	switch it.Kind {
	case task.ReviewComment:
		at := "an inline comment"
		if it.InReplyTo != 0 {
			at = "a reply in an inline thread"
		}
		if it.Path == "" {
			return at
		}
		if it.Line > 0 {
			return fmt.Sprintf("%s on %s:%d", at, it.Path, it.Line)
		}
		return fmt.Sprintf("%s on %s", at, it.Path)
	case task.ReviewSummary:
		return "a review summary"
	}
	return "a comment on the pull request"
}

// replyCommand is how the agent answers in the thread the comment is in. An inline
// comment is answered in its own thread, at the comment that started it; everything
// else is answered on the pull request's conversation.
func replyCommand(repo task.Repo, pr int, it FeedbackItem) string {
	if it.Kind == task.ReviewComment {
		root := it.ID
		if it.InReplyTo != 0 {
			root = it.InReplyTo
		}
		return fmt.Sprintf(`gh api --method POST repos/%s/pulls/%d/comments/%d/replies -f body="..."`, repo, pr, root)
	}
	return fmt.Sprintf(`gh api --method POST repos/%s/issues/%d/comments -f body="..."`, repo, pr)
}

// quote keeps the comment's own text apart from foreman's instructions, so a comment
// that holds Markdown headings cannot be read as part of the prompt.
func quote(body string) string {
	lines := strings.Split(strings.TrimSpace(body), "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}
