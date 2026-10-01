package task

import (
	"testing"
	"time"
)

func comment(kind CommentKind, id int64, body string) Feedback {
	return Feedback{Kind: kind, ID: id, Author: "dev", URL: "https://x", At: t1, Digest: Digest(body)}
}

var t1 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
var t2 = t1.Add(time.Hour)

func keys(fs []Feedback) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = string(f.Kind) + "/" + string(rune('0'+f.ID))
	}
	return out
}

func TestFeedbackMergeAddsEditsAndDrops(t *testing.T) {
	rec := &Record{}
	rec.MergeFeedback([]Feedback{comment(IssueComment, 1, "first"), comment(ReviewComment, 2, "inline")})
	if got := rec.UnhandledFeedback(); len(got) != 2 {
		t.Fatalf("a first fetch leaves %d unanswered, want 2: %v", len(got), keys(got))
	}

	rec.MarkFeedbackHandled(rec.UnhandledFeedback(), t2)
	if got := rec.UnhandledFeedback(); len(got) != 0 {
		t.Fatalf("after answering, %v are still unanswered", keys(got))
	}

	// The same bodies again: nothing to answer, and the stamps survive.
	rec.MergeFeedback([]Feedback{comment(IssueComment, 1, "first"), comment(ReviewComment, 2, "inline")})
	if got := rec.UnhandledFeedback(); len(got) != 0 {
		t.Fatalf("an unchanged refetch raised %v", keys(got))
	}
	if !rec.Feedback[0].HandledAt.Equal(t2) {
		t.Errorf("handled_at = %v, want %v", rec.Feedback[0].HandledAt, t2)
	}

	// Edited text, a new comment, and one GitHub no longer returns.
	rec.MergeFeedback([]Feedback{comment(IssueComment, 1, "first, edited"), comment(ReviewComment, 3, "reply")})
	got := rec.UnhandledFeedback()
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 {
		t.Fatalf("unanswered = %v, want the edited 1 and the new 3", keys(got))
	}
	if len(rec.Feedback) != 2 {
		t.Errorf("a comment GitHub no longer returns was kept: %v", keys(rec.Feedback))
	}
}

func TestFeedbackIdentityIncludesItsKind(t *testing.T) {
	rec := &Record{}
	rec.MergeFeedback([]Feedback{comment(IssueComment, 7, "a"), comment(ReviewSummary, 7, "b")})
	if len(rec.Feedback) != 2 {
		t.Fatalf("a review and an issue comment sharing an id collapsed: %v", keys(rec.Feedback))
	}
	rec.MarkFeedbackHandled([]Feedback{comment(IssueComment, 7, "a")}, t2)
	got := rec.UnhandledFeedback()
	if len(got) != 1 || got[0].Kind != ReviewSummary {
		t.Fatalf("unanswered = %v, want the review alone", keys(got))
	}
}

// A comment edited while the agent was answering it must be answered again, so the
// stamp only lands on the text the turn was given.
func TestMarkFeedbackHandledIgnoresEditedText(t *testing.T) {
	rec := &Record{}
	given := comment(IssueComment, 1, "first")
	rec.MergeFeedback([]Feedback{given})
	rec.MergeFeedback([]Feedback{comment(IssueComment, 1, "first, edited")})
	rec.MarkFeedbackHandled([]Feedback{given}, t2)
	if got := rec.UnhandledFeedback(); len(got) != 1 {
		t.Fatalf("the edit was stamped with the text the agent saw: %v", keys(got))
	}
}
