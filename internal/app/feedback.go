package app

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

// maxFeedbackRounds caps how many times one run answers comments. Without it, an agent
// that forgets the reply marker would read its own reply as new feedback and answer
// itself until the task file ran out of patience. Anything past the cap waits in
// REVIEWED for the developer, who can see the rounds in the log.
const maxFeedbackRounds = 3

// commentKey identifies a comment the way the task file does: a review id and a
// comment id come from different sequences on GitHub and can be the same number.
type commentKey struct {
	kind task.CommentKind
	id   int64
}

func asFeedback(c PRComment) task.Feedback {
	return task.Feedback{Kind: c.Kind, ID: c.ID, Author: c.Author, URL: c.URL, At: c.UpdatedAt, Digest: task.Digest(c.Body)}
}

func answered(comments []PRComment) []task.Feedback {
	out := make([]task.Feedback, len(comments))
	for i, c := range comments {
		out[i] = asFeedback(c)
	}
	return out
}

// readFeedback records the comments on a task's pull request and returns the record as
// it now stands with the ones still waiting for an answer, text and all. The task file
// keeps each comment's identity alone, so the text is read from GitHub whenever a turn
// needs it.
func (a App) readFeedback(ctx context.Context, rec *task.Record) (*task.Record, []PRComment, error) {
	pr := rec.Delegation.PR
	comments, err := a.GitHub.PullRequestComments(ctx, task.Key{Repo: rec.Repo, Number: pr})
	if err != nil {
		return rec, nil, fmt.Errorf("refresh %s#%d PR comments: %w", rec.Repo, rec.Number, err)
	}
	var kept []PRComment
	for _, c := range comments {
		// The agent answers through the developer's own gh login, so its replies carry
		// the marker instead; nothing else can tell them from the developer's own
		// comments, which have to count.
		if strings.TrimSpace(c.Body) == "" || strings.Contains(c.Body, agent.ReplyMarker) {
			continue
		}
		kept = append(kept, c)
	}
	updated, err := a.update(ctx, rec.Key, func(cur *task.Record) (*task.Record, error) {
		// Read again under the lock: a start since may have given the task another PR,
		// whose conversation this is not.
		if cur == nil || cur.Delegation == nil || cur.Delegation.PR != pr {
			return cur, nil
		}
		cur.MergeFeedback(answered(kept))
		return cur, nil
	})
	if err != nil || updated == nil || updated.Delegation == nil || updated.Delegation.PR != pr {
		return rec, nil, err
	}
	waiting := map[commentKey]bool{}
	for _, f := range updated.UnhandledFeedback() {
		waiting[commentKey{f.Kind, f.ID}] = true
	}
	var unanswered []PRComment
	for _, c := range kept {
		if waiting[commentKey{c.Kind, c.ID}] {
			unanswered = append(unanswered, c)
		}
	}
	return updated, unanswered, nil
}

// feedback is the gate's look at a handed-over task's pull request. Comments waiting
// for an answer move it to REVIEWED, which is where INBOX offers it; a task whose
// comments have all been answered, or deleted, goes back to the developer.
func (a App) feedback(ctx context.Context, rec *task.Record) (*task.Record, string, error) {
	updated, waiting, err := a.readFeedback(ctx, rec)
	if err != nil {
		return rec, "", err
	}
	want := task.ReadyForHuman
	if len(waiting) > 0 {
		want = task.Reviewed
	}
	if updated.State == want {
		return updated, "", nil
	}
	var note string
	updated, err = a.update(ctx, rec.Key, func(cur *task.Record) (*task.Record, error) {
		// Read again under the lock: a resume since the gate's list owns the task now.
		if cur == nil || cur.State != rec.State || cur.Delegation == nil || cur.Delegation.PR != rec.Delegation.PR {
			return cur, nil
		}
		cur.State = want
		status := "reviewed"
		note = feedbackNote(len(waiting), cur.Delegation.PR)
		if want == task.ReadyForHuman {
			status, note = "feedback_answered", fmt.Sprintf("every comment on PR #%d is answered", cur.Delegation.PR)
		}
		cur.LastOutcome = &task.Outcome{Status: status, Summary: note, At: a.Now()}
		return cur, nil
	})
	return updated, note, err
}

func feedbackNote(n, pr int) string {
	return fmt.Sprintf("%d %s to answer on PR #%d", n, plural(n, "comment", "comments"), pr)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// handsOver reports a task whose pull request is the developer's to comment on: the
// agents are done with it and no runner is driving it, so a comment is feedback rather
// than something the current run is about to answer itself. The runner's pid is
// judged by whether the process is alive, so a pid a killed runner left behind cannot
// hide a comment for good.
func (a App) handsOver(r *task.Record) bool {
	d := r.Delegation
	if d == nil || d.PR == 0 || a.liveRunner(r) {
		return false
	}
	return r.State == task.ReadyForHuman || r.State == task.Reviewed
}

// unanswered is the comments a run has to answer before it does anything else.
// Decisions come first, so a run carrying an answer the agent has not received yet
// delivers that and finds the comments on its way out. done reports that the run
// has nothing else to do: a task whose clean review covers its PR head and whose
// comments are gone, or could not be read, must not fall through to an
// implementation turn that redoes the issue.
func (a App) unanswered(ctx, final context.Context, rec *task.Record, log io.Writer) (comments []PRComment, updated *task.Record, done bool, err error) {
	d := rec.Delegation
	if d == nil || d.PR == 0 || len(rec.UnhandledFeedback()) == 0 || len(rec.Undelivered()) > 0 {
		return nil, rec, false, nil
	}
	updated, waiting, readErr := a.readFeedback(ctx, rec)
	if len(waiting) > 0 {
		return waiting, updated, false, nil
	}
	current := false
	if reviewCovers(updated, d.PRHead, d.PRDigest) {
		if updated, current, err = a.currentPR(ctx, final, updated); err != nil {
			return nil, updated, false, err
		}
	}
	if !current || !reviewCovers(updated, updated.Delegation.PRHead, updated.Delegation.PRDigest) {
		// Open findings or an unreviewed head: the run has that work to do anyway, and
		// the comments are picked up before it hands over.
		if readErr != nil {
			fmt.Fprintf(log, "foreman: %v; the next refresh will pick the comments up\n", readErr)
		}
		return nil, updated, false, nil
	}
	if readErr != nil {
		// The comments are still waiting, so the task stays where the developer can
		// resume it once GitHub answers.
		fmt.Fprintf(log, "foreman: %v; leaving the comments for the next resume\n", readErr)
		updated, err = a.update(final, rec.Key, existing(func(cur *task.Record) {
			cur.State = task.Reviewed
			cur.LastOutcome = &task.Outcome{Status: "reviewed", Summary: "could not read the PR comments; resume to try again", At: a.Now()}
		}))
		return nil, updated, true, err
	}
	fmt.Fprintf(log, "foreman: PR #%d has no comments left to answer\n", d.PR)
	updated, err = a.update(final, rec.Key, existing(func(cur *task.Record) {
		cur.State = task.WaitingForCI
		if handsOverNow(cur, cur.CI) {
			cur.State = task.ReadyForHuman
		}
		cur.LastOutcome = &task.Outcome{Status: "feedback_answered", Summary: fmt.Sprintf("every comment on PR #%d is answered", cur.Delegation.PR), At: a.Now()}
	}))
	return nil, updated, true, err
}

// currentPR stores the head and description digest the PR has on GitHub now, so a path
// that skips review judges coverage by them, not by what a refresh stored earlier.
// current is false when the PR could not be read, and the run then reviews instead of
// handing over.
func (a App) currentPR(ctx, final context.Context, rec *task.Record) (updated *task.Record, current bool, err error) {
	pr, err := a.GitHub.GetPullRequest(ctx, task.Key{Repo: rec.Repo, Number: rec.Delegation.PR})
	if err != nil || pr.HeadSHA == "" {
		return rec, false, nil
	}
	digest := task.ProposalDigest(pr.Title, pr.Body)
	if digest == rec.Delegation.PRDigest && pr.HeadSHA == rec.Delegation.PRHead {
		return rec, true, nil
	}
	updated, err = a.update(final, rec.Key, existing(func(cur *task.Record) {
		cur.Delegation.PRDigest, cur.Delegation.PRHead = digest, pr.HeadSHA
	}))
	return updated, err == nil, err
}

// handsOverNow reports whether a task whose clean review covers its PR goes straight
// to the developer once its comments are answered: CI has finished. A task with claim
// verification waits for the gate instead, which reads the PRD before handing it over.
func handsOverNow(rec *task.Record, ci *task.CIStatus) bool {
	return ci != nil && ci.State != task.CIPending && !rec.Delegation.ClaimVerification && !rec.FinalCheckPending()
}

// reviewCovers reports a clean competing review of head and of the description with
// digest on the task's current PR, which is the review bar the gate applies before it
// hands a task over. A review or digest from before descriptions were recorded is
// empty, and the review then covers its head alone. A task with claim verification
// also needs the review of its current claims and PRD.
func reviewCovers(rec *task.Record, head, digest string) bool {
	latest := rec.LatestCompeting()
	described := latest != nil && (latest.Proposal == "" || digest == "" || latest.Proposal == digest)
	return described && rec.ClaimsCovered(latest) && rec.ScopeCovered(latest) && latest.Clean && latest.PR == rec.Delegation.PR && head != "" && latest.Head == head
}

// beforeHandover answers comments that arrived while the run worked, so the developer
// never gets a task back with feedback nobody has read. It returns the comments
// the next turn should answer; none means the run is done. Past maxFeedbackRounds it
// leaves them in REVIEWED instead, for the developer to resume.
func (a App) beforeHandover(ctx, final context.Context, rec *task.Record, rounds int, log io.Writer) ([]PRComment, *task.Record, error) {
	if rec.Delegation == nil || rec.Delegation.PR == 0 {
		return nil, rec, nil
	}
	updated, waiting, err := a.readFeedback(ctx, rec)
	if err != nil {
		fmt.Fprintf(log, "foreman: %v; the next refresh will pick the comments up\n", err)
		return nil, rec, nil
	}
	if len(waiting) == 0 {
		return nil, updated, nil
	}
	if rounds < maxFeedbackRounds {
		fmt.Fprintf(log, "foreman: %s arrived during the run; answering before handing over\n", feedbackNote(len(waiting), updated.Delegation.PR))
		return waiting, updated, nil
	}
	fmt.Fprintf(log, "foreman: %s after %d rounds; leaving them for you\n", feedbackNote(len(waiting), updated.Delegation.PR), rounds)
	updated, err = a.update(final, rec.Key, existing(func(cur *task.Record) {
		cur.State = task.Reviewed
		cur.LastOutcome = &task.Outcome{Status: "reviewed", Summary: feedbackNote(len(waiting), cur.Delegation.PR), At: a.Now()}
	}))
	return nil, updated, err
}

// feedbackItems is what the agent is shown: the comment, where it sits, and its text.
func feedbackItems(comments []PRComment) []agent.FeedbackItem {
	items := make([]agent.FeedbackItem, len(comments))
	for i, c := range comments {
		items[i] = agent.FeedbackItem{Kind: c.Kind, ID: c.ID, Author: c.Author, Body: c.Body,
			URL: c.URL, Path: c.Path, Line: c.Line, InReplyTo: c.InReplyTo}
	}
	return items
}
