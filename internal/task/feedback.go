package task

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// CommentKind is where a comment on a pull request lives, which decides how the agent
// replies to it.
type CommentKind string

const (
	// IssueComment is a comment on the pull request's conversation.
	IssueComment CommentKind = "issue"
	// ReviewComment is an inline comment on the diff, or a reply in its thread.
	ReviewComment CommentKind = "review_comment"
	// ReviewSummary is the body of a submitted review.
	ReviewSummary CommentKind = "review"
)

// Feedback is one comment on the task's pull request, with whether the agent has
// answered the text it holds now. The body itself is not stored: it is read from
// GitHub again when a turn needs it.
type Feedback struct {
	Kind   CommentKind
	ID     int64
	Author string
	URL    string
	At     time.Time
	// Digest identifies the comment's text. Only the text decides whether a comment is
	// answered, so editing it raises the comment again and a reaction does not.
	Digest string
	// HandledAt is when a turn answered this text. Zero means it is still waiting.
	HandledAt time.Time
}

// commentKey identifies a comment. The kind is part of it because a review id and a
// comment id come from different sequences on GitHub and can be the same number.
type commentKey struct {
	Kind CommentKind
	ID   int64
}

func (f Feedback) key() commentKey { return commentKey{Kind: f.Kind, ID: f.ID} }

// Digest is the stored identity of a comment's text.
func Digest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:6])
}

// MergeFeedback records the comments GitHub reports now. A comment foreman has not
// seen is unanswered, a comment whose text changed is unanswered again, and a comment
// GitHub no longer returns is dropped so a deleted one cannot hold a task back.
func (r *Record) MergeFeedback(seen []Feedback) {
	stored := map[commentKey]Feedback{}
	for _, f := range r.Feedback {
		stored[f.key()] = f
	}
	merged := make([]Feedback, 0, len(seen))
	for _, f := range seen {
		if was, ok := stored[f.key()]; ok && was.Digest == f.Digest {
			f.HandledAt = was.HandledAt
		}
		merged = append(merged, f)
	}
	r.Feedback = merged
}

// UnhandledFeedback lists the comments waiting for an answer, oldest first.
func (r *Record) UnhandledFeedback() []Feedback {
	var out []Feedback
	for _, f := range r.Feedback {
		if f.HandledAt.IsZero() {
			out = append(out, f)
		}
	}
	return out
}

// MarkFeedbackHandled stamps the comments a turn answered. The digest has to match
// too, so a comment edited while the agent was answering it is asked again.
func (r *Record) MarkFeedbackHandled(handled []Feedback, now time.Time) {
	answered := map[commentKey]string{}
	for _, f := range handled {
		answered[f.key()] = f.Digest
	}
	for i := range r.Feedback {
		if f := &r.Feedback[i]; answered[f.key()] == f.Digest {
			f.HandledAt = now
		}
	}
}
