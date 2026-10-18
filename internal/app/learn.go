package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/task"
)

// earlierEvaluations caps how many of the repository's other evaluations the
// evaluator sees, so the prompt stays bounded as a repository's history grows.
const earlierEvaluations = 12

// learnRetryAfter is how long a refresh waits before it retries an evaluation that
// failed on the same evidence.
const learnRetryAfter = time.Hour

// Learn evaluates one task and folds the result into its repository's knowledge. It
// holds the repository's lock for the whole evaluation and returns
// knowledge.ErrLocked when another evaluation holds it. A failed or invalid
// evaluator turn returns before anything is written, so the stored fingerprint stays
// and the next refresh tries again. It never changes the task.
func (a App) Learn(ctx context.Context, key task.Key, log io.Writer) error {
	if a.Knowledge == nil || a.Evaluator == nil {
		return errors.New("learning is not configured")
	}
	if log == nil {
		log = io.Discard
	}
	// A learner spawned before learning.enabled was turned off starts nothing new.
	if !a.Learning {
		fmt.Fprintf(log, "learn: learning is off; %s#%d is not evaluated\n", key.Repo, key.Number)
		return nil
	}
	rec, err := a.Store.Get(ctx, key)
	if err != nil {
		return err
	}
	if rec == nil || rec.Delegation == nil {
		return fmt.Errorf("%s#%d: no delegated task", key.Repo, key.Number)
	}
	if !learnable(rec) {
		return fmt.Errorf("%s#%d: no clean review or stalled self-review to learn from", key.Repo, key.Number)
	}
	repo, id := string(rec.Repo), learnTaskID(rec.Key)
	unlock, err := a.Knowledge.Lock(repo)
	if err != nil {
		return err
	}
	defer unlock()
	if err := a.Knowledge.RecordAttempt(repo, id, knowledge.Attempt{Fingerprint: fingerprint(rec), At: a.Now()}); err != nil {
		return err
	}

	learnings, err := a.Knowledge.Learnings(repo)
	if err != nil {
		return err
	}
	stored, err := a.Knowledge.Evaluations(repo, earlierEvaluations+1)
	if err != nil {
		return err
	}
	var earlier []knowledge.Evaluation
	for _, ev := range stored {
		if ev.Task != id && len(earlier) < earlierEvaluations {
			earlier = append(earlier, ev)
		}
	}
	feedback, err := a.feedbackBodies(ctx, rec)
	if err != nil {
		return err
	}
	brief := evidence(rec, a.Now())
	brief.Feedback, brief.Learnings, brief.Earlier = feedback, learnings, earlier

	in := agent.EvaluateInput{Agent: rec.Delegation.Implementer.Agent, Prompt: agent.EvaluationPrompt(brief), MaxBudgetUSD: a.LearningBudgetUSD, Log: log}
	if p := rec.Delegation.Profile; p != nil {
		in.Agent, in.Model, in.ReasoningEffort = p.Agent, p.PlanModel, p.PlanReasoningEffort
	}
	if in.Agent == "" {
		in.Agent = task.Claude
	}
	out, err := a.Evaluator.Evaluate(ctx, in)
	if err != nil {
		return fmt.Errorf("evaluate %s#%d: %w", rec.Repo, rec.Number, err)
	}
	ev := knowledge.Evaluation{
		Task: id, TaskAt: taskStart(rec), At: a.Now(), Agent: string(in.Agent),
		Fingerprint:    fingerprint(rec),
		MaintainerRefs: brief.MaintainerRefs(),
		Assessment:     out.Assessment, Signals: out.Signals, Proposals: out.Proposals, Revisions: out.Revisions,
	}
	if err := a.Knowledge.Apply(repo, ev); err != nil {
		return err
	}
	fmt.Fprintf(log, "learn: evaluated %s task %s: %d proposals, %d revisions\n", repo, id, len(out.Proposals), len(out.Revisions))
	return nil
}

// LearningDue reports whether a learnable task has evidence its stored evaluation did
// not see: a first evaluation, or a changed fingerprint.
func (a App) LearningDue(rec *task.Record) (bool, error) {
	if a.Knowledge == nil || rec == nil || rec.Delegation == nil || !learnable(rec) {
		return false, nil
	}
	ev, err := a.Knowledge.Evaluation(string(rec.Repo), learnTaskID(rec.Key))
	if err != nil {
		return false, err
	}
	return ev == nil || ev.Fingerprint != fingerprint(rec), nil
}

// learnable reports a task with evidence to learn from: a clean competing review, or
// a self-review loop stalled on a missed practice.
func learnable(rec *task.Record) bool {
	for _, r := range rec.Reviews {
		if r.Clean && r.Kind == task.CompetingReview {
			return true
		}
	}
	return stalledOnPractice(rec)
}

// stalledOnPractice reports a task whose latest decision is a self-review fix limit
// diagnosed as a missed generic practice, still waiting or answered with a stop. An
// answer that goes on leaves the task to finish its review loop first.
func stalledOnPractice(rec *task.Record) bool {
	if len(rec.Decisions) == 0 {
		return false
	}
	d := rec.Decisions[len(rec.Decisions)-1]
	if d.Diagnosis == nil || d.Diagnosis.Kind != task.DiagnosedPracticeGap {
		return false
	}
	return rec.State == task.NeedsDecision && d.Answer == "" || rec.State == task.Failed && stopAnswer(d)
}

// learnTaskID names a task's evaluation file. A Linear task has no number.
func learnTaskID(k task.Key) string {
	if k.Source == task.Linear {
		return "linear-" + k.ExternalID
	}
	return strconv.Itoa(k.Number)
}

// taskStart orders tasks for the store: an observed revision applies only from a
// task that started later than the learning's evidence.
func taskStart(rec *task.Record) time.Time {
	if len(rec.Attempts) > 0 && !rec.Attempts[0].StartedAt.IsZero() {
		return rec.Attempts[0].StartedAt
	}
	return rec.Delegation.StartedAt
}

// fingerprint identifies the evidence an evaluation used: review rounds, answered
// decisions, the developer's PR comments and their text, the PR and its head, and
// the PR's outcome. CI is left out on purpose, so a CI change never re-evaluates.
func fingerprint(rec *task.Record) string {
	var b strings.Builder
	answered := 0
	for _, d := range rec.Decisions {
		if d.Answer != "" {
			answered++
		}
	}
	fmt.Fprintf(&b, "reviews=%d decisions=%d pr=%d head=%s outcome=%s\n", len(rec.Reviews), answered, rec.Delegation.PR, rec.Delegation.PRHead, prOutcome(rec))
	for _, f := range rec.Feedback {
		fmt.Fprintf(&b, "feedback=%s/%d/%s\n", f.Kind, f.ID, f.Digest)
	}
	if a := rec.CurrentAttempt(); a != nil && !a.EndedAt.IsZero() {
		fmt.Fprintf(&b, "ended=%s\n", a.EndedAt.UTC().Format(time.RFC3339))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:12])
}

func prOutcome(rec *task.Record) string {
	switch {
	case rec.Delegation.PR == 0:
		return "none"
	case rec.State == task.Done:
		return "merged"
	case rec.State == task.PRClosed:
		return "closed"
	}
	return "open"
}

// evidence builds the packet from the task record alone. It reads finding
// summaries but not their detail, and nothing about CI, validation, or logs.
func evidence(rec *task.Record, now time.Time) agent.EvaluationBrief {
	b := agent.EvaluationBrief{Repo: rec.Repo, Title: rec.Title, Usage: rec.Usage, Outcome: prOutcome(rec)}
	for _, d := range rec.Decisions {
		// A limit decision without a conflict only offers another round or a stop, so
		// its answer says nothing a learning could cite as the developer's word.
		if d.Answer == "" || d.Diagnosis != nil && d.Diagnosis.Kind != task.DiagnosedConflict {
			continue
		}
		q := d.Question
		ed := agent.EvidenceDecision{Question: knowledge.Redact(q.Issue), Recommendation: q.Recommendation, Answer: knowledge.Redact(d.Answer)}
		for _, o := range q.Options {
			ed.Options = append(ed.Options, knowledge.Redact(o.Label+": "+o.Description))
		}
		if rec, ok := q.Recommended(); ok {
			ed.Recommendation = rec.Label
			chosen, ok := q.Option(d.Answer)
			ed.Overrode = !ok || chosen.Label != rec.Label
		}
		b.Decisions = append(b.Decisions, ed)
	}
	for i, r := range rec.Reviews {
		er := agent.EvidenceReview{Round: r.Round, Kind: r.Kind, Clean: r.Clean}
		for _, f := range r.Findings {
			er.Findings = append(er.Findings, knowledge.Redact(f.Summary))
		}
		if !r.Clean {
			er.HeadAdvanced = headAdvanced(rec, i)
		}
		b.Reviews = append(b.Reviews, er)
	}
	if a := rec.CurrentAttempt(); a != nil {
		end := a.HandoffAt
		if end.IsZero() {
			end = a.EndedAt
		}
		if end.IsZero() {
			end = now
		}
		b.Phases = a.Phases(end)
	}
	return b
}

// headAdvanced reports whether the PR head moved after review round i.
func headAdvanced(rec *task.Record, i int) bool {
	head := rec.Reviews[i].Head
	if head == "" {
		return false
	}
	for _, later := range rec.Reviews[i+1:] {
		if later.Head != "" && later.Head != head {
			return true
		}
	}
	return rec.Delegation.PRHead != "" && rec.Delegation.PRHead != head
}

// feedbackBodies reads the people's comments on the task's PR: what readFeedback
// counts, less bots, whose comments are no maintainer's word. It reads all of them,
// not only those the task file recorded:
// the gate stops recording when the PR merges or closes, and the evaluation that
// follows the merge must still see a comment posted just before it.
func (a App) feedbackBodies(ctx context.Context, rec *task.Record) ([]string, error) {
	if rec.Delegation.PR == 0 {
		return nil, nil
	}
	comments, err := a.GitHub.PullRequestComments(ctx, task.Key{Repo: rec.Repo, Number: rec.Delegation.PR})
	if err != nil {
		return nil, fmt.Errorf("read %s#%d PR comments: %w", rec.Repo, rec.Delegation.PR, err)
	}
	var out []string
	for _, c := range comments {
		if strings.TrimSpace(c.Body) != "" && !strings.Contains(c.Body, agent.ReplyMarker) && !strings.HasSuffix(c.Author, "[bot]") {
			out = append(out, knowledge.Redact(c.Body))
		}
	}
	return out, nil
}

// startLearners starts an evaluation of each finished task whose evidence changed
// since its stored evaluation, at most one per repository. A task a runner still
// drives is the runner's to start, a repository with an evaluation running waits,
// and holdLearning says which other due tasks wait.
func (a App) startLearners(recs []*task.Record) {
	if !a.Learning || a.Knowledge == nil {
		return
	}
	started := map[task.Repo]bool{}
	for _, r := range recs {
		if r.State == task.Working || r.State == task.Reviewing || started[r.Repo] {
			continue
		}
		if due, err := a.LearningDue(r); err != nil || !due || a.holdLearning(r) || a.Knowledge.Busy(string(r.Repo)) {
			continue
		}
		// A learner that fails to start is tried again on the next refresh.
		if a.Runners.SpawnLearn(r.Key) == nil {
			started[r.Repo] = true
		}
	}
}

// holdLearning reports whether a due task waits for a later refresh. An evaluation
// that failed on the same evidence waits learnRetryAfter, so a failing evaluator is
// not paid for on every refresh. A merged or closed task never tried before
// predates learning, and evaluating that backlog would spend on old work.
func (a App) holdLearning(rec *task.Record) bool {
	at, err := a.Knowledge.Attempt(string(rec.Repo), learnTaskID(rec.Key))
	switch {
	case err != nil:
		return true
	case at == nil:
		return rec.State == task.Done || rec.State == task.PRClosed
	}
	return at.Fingerprint == fingerprint(rec) && a.Now().Sub(at.At) < learnRetryAfter
}

// learnAfterRun starts the task's evaluation when its run ends on a clean review, or
// at a self-review fix limit diagnosed as a missed practice. Learning is a side job: a
// learner that fails to start is noted in the task log, and the refresh starts it later.
func (a App) learnAfterRun(rec *task.Record, log io.Writer) {
	if !a.Learning || a.Knowledge == nil {
		return
	}
	if r := rec.LatestCompeting(); (r == nil || !r.Clean) && !stalledOnPractice(rec) {
		return
	}
	if due, err := a.LearningDue(rec); err != nil || !due {
		return
	}
	if err := a.Runners.SpawnLearn(rec.Key); err != nil {
		fmt.Fprintf(log, "foreman: learning not started: %v\n", err)
		return
	}
	fmt.Fprintln(log, "foreman: learning from this task in the background")
}
