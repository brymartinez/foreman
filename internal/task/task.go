// Package task holds the durable task model persisted as Markdown frontmatter.
package task

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Agent string

const (
	Claude Agent = "claude"
	Codex  Agent = "codex"
)

func ParseAgent(s string) (Agent, error) {
	switch a := Agent(s); a {
	case Claude, Codex:
		return a, nil
	}
	return "", fmt.Errorf("unknown agent %q (want claude or codex)", s)
}

// Reviewer is the competing agent: Claude implements, Codex reviews, and vice versa.
func (a Agent) Reviewer() Agent {
	if a == Claude {
		return Codex
	}
	return Claude
}

type State string

const (
	Discovered State = "DISCOVERED"
	// The stages name what a delegated task is doing now. Foreman writes each one
	// before the agent turn it names, so a refresh or a restart shows the step that runs.
	Planning     State = "PLANNING"
	Implementing State = "IMPLEMENTING" // an implementer turn: code, a fix, an answer, a merge
	// SelfReviewing and AdversarialReviewing are the implementer's own review and the
	// competing agent's review of the current change.
	SelfReviewing        State = "SELF_REVIEW"
	AdversarialReviewing State = "ADVERSARIAL_REVIEW"
	// OpeningPR is the turn that pushes a cleanly reviewed local commit and opens its PR.
	OpeningPR State = "OPENING_PR"
	// WaitingForCI is a PR with a clean adversarial review, waiting for the gate to
	// read CI. No agent turn runs for it.
	WaitingForCI  State = "WAITING_FOR_CI"
	NeedsDecision State = "NEEDS_DECISION"
	ReadyForHuman State = "READY_FOR_HUMAN"
	// Reviewed means the developer commented on the task's pull request and the agent
	// has not answered yet. Resuming the task hands it those comments.
	Reviewed State = "REVIEWED"
	Done     State = "DONE"
	Failed   State = "FAILED"
	// PRClosed means the task's PR was closed without merging. The agent did not
	// fail, so the task keeps its file and branch. Cleanup may remove its worktree.
	PRClosed State = "PR_CLOSED"
	// LegacyWorking and LegacyReviewing are the states task files held before the
	// stages. Foreman never writes them; UpgradeState maps them when a file is read.
	LegacyWorking   State = "WORKING"
	LegacyReviewing State = "REVIEWING"
)

// Active reports a stage: the task is delegated work in progress.
func (s State) Active() bool {
	switch s {
	case Planning, Implementing, SelfReviewing, AdversarialReviewing, OpeningPR, WaitingForCI:
		return true
	}
	return false
}

// Runnable reports a stage that ends in an agent turn, which a launch starts a runner for.
func (s State) Runnable() bool { return s.Active() && s != WaitingForCI }

// Reviewing reports either review stage.
func (s State) Reviewing() bool { return s == SelfReviewing || s == AdversarialReviewing }

// Repo is "owner/name", as gh reports nameWithOwner.
type Repo string

var repoPattern = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)

func ParseRepo(s string) (Repo, error) {
	if !repoPattern.MatchString(s) {
		return "", fmt.Errorf("invalid repo %q (want owner/name)", s)
	}
	return Repo(s), nil
}

type Key struct {
	Repo       Repo
	Number     int
	Source     Source
	ExternalID string
}

type Source string

const (
	GitHub Source = "github"
	Linear Source = "linear"
)

type Kind string

const (
	Issue       Kind = "issue"
	PullRequest Kind = "pr"
)

type InboxReason string

const (
	Assigned        InboxReason = "assigned"
	Mentioned       InboxReason = "mentioned"
	ReviewRequested InboxReason = "review_requested"
	Tracked         InboxReason = "tracked"
)

// Snapshot is what GitHub looked like the last time the user ran inbox.
// The inbox diff compares current GitHub state against it.
type Snapshot struct {
	UpdatedAt time.Time
	State     string // open, closed, merged
	Reasons   []InboxReason
}

type Session struct {
	Agent Agent
	ID    string
}

type ModelCall struct {
	Agent      Agent     `yaml:"agent"`
	Step       string    `yaml:"step"`
	Model      string    `yaml:"model"`
	StartedAt  time.Time `yaml:"started_at"`
	FinishedAt time.Time `yaml:"finished_at"`
	Error      string    `yaml:"error,omitempty"`
	Usage      Usage     `yaml:"usage"`
}

// Tokens counts reported input and output, including cached input.
func (u Usage) Tokens() int64 {
	return u.ClaudeInputTokens + u.ClaudeOutputTokens + u.CodexInputTokens + u.CodexOutputTokens
}

type Usage struct {
	Model                string  `yaml:"model,omitempty"`
	ClaudeInputTokens    int64   `yaml:"claude_input_tokens,omitempty"`
	ClaudeCachedTokens   int64   `yaml:"claude_cached_tokens,omitempty"`
	ClaudeOutputTokens   int64   `yaml:"claude_output_tokens,omitempty"`
	ClaudeCostUSD        float64 `yaml:"claude_cost_usd,omitempty"`
	CodexInputTokens     int64   `yaml:"codex_input_tokens,omitempty"`
	CodexCachedTokens    int64   `yaml:"codex_cached_tokens,omitempty"`
	CodexOutputTokens    int64   `yaml:"codex_output_tokens,omitempty"`
	CodexReasoningTokens int64   `yaml:"codex_reasoning_tokens,omitempty"`
	ClaudeTurns          int     `yaml:"claude_turns,omitempty"`
	CodexTurns           int     `yaml:"codex_turns,omitempty"`
	// ToolCalls counts tool calls by name: Claude tool names, or Codex item types.
	ToolCalls map[string]int `yaml:"tool_calls,omitempty"`
}

// Profile is the agent, models, and effort routing chose for a task. It is stored
// resolved, so editing routing.yaml or config.yaml never changes a running task.
// Empty fields leave the adapter's own setting in place.
type Profile struct {
	Name                string `yaml:"name"`
	Agent               Agent  `yaml:"agent"`
	PlanModel           string `yaml:"plan_model,omitempty"`
	Model               string `yaml:"model,omitempty"`
	PlanReasoningEffort string `yaml:"plan_reasoning_effort,omitempty"`
	ReasoningEffort     string `yaml:"reasoning_effort,omitempty"`
}

// Delegation exists once the user has run start; tracked-only items have none.
type Delegation struct {
	Implementer Session
	// Profile is set when routing chose the implementer; RouteReason says why.
	// RouteLogged is set once the runner has written the route line to the log.
	Profile     *Profile
	RouteReason string
	RouteLogged bool
	StartedAt   time.Time
	Reviewer    Agent
	// Account is the GitHub login active in gh when the task first launched. Every
	// commit, push, and PR action of the task runs as it. AccountEmail is its noreply
	// address. Tasks started before pinning have neither until their next launch.
	Account      string
	AccountEmail string
	Branch       string
	BaseRef      string
	// RepoDir is the checkout the worktree was made from, which holds the branch.
	RepoDir  string
	Worktree string
	// WorktreeRemovedAt is set once cleanup removed the worktree; the branch may remain.
	WorktreeRemovedAt time.Time
	ContextFile       string
	// Launch is the claim of the launch that spawns the next runner, until that
	// runner takes the task over.
	Launch    *Launch
	RunnerPID int
	RunnerLog string
	PR        int
	PRURL     string
	// PriorPR is the PR of a run the developer reset. Each fresh session reads it
	// again as context. It equals PR when this run continues that PR.
	PriorPR int
	// PRHead is the PR's head commit when foreman last read it. A review covers one head.
	PRHead string
	// PRDigest is the ProposalDigest of the PR title and description the review step
	// last read. A self-review covers one head with one description.
	PRDigest string
	// CheckpointHead is the last accepted implementation commit, or the head when
	// the worktree was prepared. The commit and CheckpointNext cross to a fresh session.
	CheckpointHead string
	CheckpointNext string
	Checkpoints    int
	// ConflictAttempts lists every PR head and base head an automatic merge conflict
	// repair ran for, oldest first. A repair runs once per pair. ConflictPending is set
	// until the runner has run the last one.
	ConflictAttempts []ConflictAttempt
	ConflictPending  bool
	// ReviewBeforePR is config's review_before_pr when the task started. Set, the
	// agent review runs on the local branch and the PR opens only after a clean review.
	ReviewBeforePR bool
	// LocalHead is the committed, unpushed change the implementer last returned for
	// review, and ProposedTitle and ProposedBody are the PR it proposed with it. The
	// three change together, so a commit is always reviewed with its own description.
	LocalHead     string
	ProposedTitle string
	ProposedBody  string
	// ClaimVerification is set when a task that plans starts. Its implementer then
	// returns Claims with each change, and both reviews check them against the PRD.
	// Tasks started without it keep the review workflow they started with.
	ClaimVerification bool
	// FullSuiteGate is set when a task starts. Its change is then handed off only with
	// a full-suite result that covers it, and FinalChecks counts the final-check turns
	// run toward the current handoff. Tasks started without it keep the workflow they
	// started with.
	FullSuiteGate bool
	FinalChecks   int
	// Claims is the latest full claim set the implementer returned. It stays in the
	// task file and is never published.
	Claims []Claim
	// PRDDigest is the Digest of the PRD text the review step last loaded from the
	// issue. A review covers one PRD.
	PRDDigest string
	// ScopeDigest is the effective triage scope last read at a stage boundary.
	// PlanScopeDigest and PlanDigest identify the plan checked against that scope.
	ScopeDigest     string
	PlanScopeDigest string
	PlanDigest      string
}

// Launch is taken under the task lock before a runner is spawned, by the foreman
// process PID. Only the runner given Token may work the task, so two launches never
// run two runners. A claim holds other launches off while PID runs. PID is cleared
// once the launch has recorded its runner, and a later launch replaces a claim its
// launcher or runner left behind.
type Launch struct {
	Token string    `yaml:"token"`
	PID   int       `yaml:"pid,omitempty"`
	Since time.Time `yaml:"since,omitempty"`
}

// ResetMark is all a reset keeps of the discarded run: the PR it opened, if any, so
// the next start can continue that PR or read it as context. PRState is the state
// GitHub reported at the reset; the next start reads it again.
type ResetMark struct {
	At      time.Time
	PR      int
	PRURL   string
	Branch  string
	PRState string
	// Cleanup is the part of the run a reset has not removed yet, or nil.
	Cleanup *ResetCleanup
}

// ResetCleanup names the local run files a reset still has to remove. Since is set
// while Owner, the reset or one start in process PID, removes them and makes the new
// worktree; any other start waits while that process runs, because it would work at
// the same path. Since is zero once the owner finished or gave up. A start refuses
// while a listed worktree or branch is still there.
type ResetCleanup struct {
	Worktree  string    `yaml:"worktree,omitempty"`
	Branch    string    `yaml:"branch,omitempty"`
	RepoDir   string    `yaml:"repo_dir,omitempty"`
	RunnerLog string    `yaml:"runner_log,omitempty"`
	Since     time.Time `yaml:"since,omitempty"`
	Owner     string    `yaml:"owner,omitempty"`
	PID       int       `yaml:"pid,omitempty"`
}

// ConflictAttempt is one automatic merge conflict repair: the PR head that conflicted
// and the base head it conflicted with.
type ConflictAttempt struct {
	Head string `yaml:"head"`
	Base string `yaml:"base"`
}

// ConflictAttempted reports whether an automatic repair already ran, or is running,
// for this PR head and base head.
func (d *Delegation) ConflictAttempted(head, base string) bool {
	if head == "" || base == "" {
		return false
	}
	for _, c := range d.ConflictAttempts {
		if c.Head == head && c.Base == base {
			return true
		}
	}
	return false
}

// LastConflict is the most recent repair attempt, or the zero value.
func (d *Delegation) LastConflict() ConflictAttempt {
	if len(d.ConflictAttempts) == 0 {
		return ConflictAttempt{}
	}
	return d.ConflictAttempts[len(d.ConflictAttempts)-1]
}

// Check is one validation command the implementation agent ran, as it reported it.
type Check struct {
	Command string     `json:"command" yaml:"command"`
	Passed  bool       `json:"passed" yaml:"passed"`
	Scope   CheckScope `json:"scope" yaml:"scope,omitempty"`
}

// CheckScope tells a focused check from one in the repository's full supported suite.
// A check stored before scopes existed has none and counts as focused.
type CheckScope string

const (
	FocusedCheck CheckScope = "focused"
	FullCheck    CheckScope = "full"
)

// DocOnlyPath is a path changed since a full run, with the implementer's
// repository-specific evidence that it cannot affect behavior or the supported checks.
type DocOnlyPath struct {
	Path     string `json:"path" yaml:"path"`
	Evidence string `json:"evidence" yaml:"evidence"`
}

// FullResult is the repository's full supported suite as Foreman last verified it.
// Foreman checks Git facts alone: Revision is Head or its ancestor, and every path
// changed between them is in DocOnly with nonempty evidence. It cannot judge whether
// the evidence is sound.
type FullResult struct {
	// Head is the commit the result was checked against, and covers when Covered is set.
	Head string `yaml:"head"`
	// Revision is the commit the full suite ran on.
	Revision string        `yaml:"revision,omitempty"`
	Checks   []Check       `yaml:"checks,omitempty"`
	DocOnly  []DocOnlyPath `yaml:"doc_only,omitempty"`
	Covered  bool          `yaml:"covered,omitempty"`
	// Reason says why a result does not cover Head.
	Reason string `yaml:"reason,omitempty"`
	// Unverified is the implementer's explanation for a repository with no
	// identifiable full suite. It permits handoff with the explanation shown.
	Unverified string    `yaml:"unverified,omitempty"`
	At         time.Time `yaml:"at"`
}

// Covers reports whether the result lets head be handed off: a verified run of head,
// or a repository with no full suite to run.
func (f *FullResult) Covers(head string) bool {
	return f != nil && (f.Unverified != "" || f.Covered && head != "" && f.Head == head)
}

// Gap says why the result does not cover head, or "" when it does.
func (f *FullResult) Gap(head string) string {
	switch {
	case f.Covers(head):
		return ""
	case f == nil:
		return "no full-suite result was reported"
	case !f.Covered:
		return f.Reason
	}
	return fmt.Sprintf("the full result covers %s, and the change moved to %s since", ShortSHA(f.Head), ShortSHA(head))
}

// ShortSHA is the seven-character form of a commit, as Git shows it.
func ShortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// Outcome is the last thing an agent turn (or the runner) reported, kept for show.
type Outcome struct {
	Status  string
	Summary string
	At      time.Time
}

// Question is the BLOCKED: Need decision payload. Agents return it as
// structured output; foreman never scrapes it from terminal text.
type Question struct {
	Issue          string   `json:"issue" yaml:"issue"`
	Context        string   `json:"context" yaml:"context"`
	Options        []Option `json:"options" yaml:"options"`
	Recommendation string   `json:"recommendation" yaml:"recommendation"`
	ImpactIfWrong  string   `json:"impactIfWrong" yaml:"impact_if_wrong"`
}

func (q Question) Validate() error {
	if strings.TrimSpace(q.Issue) == "" {
		return errors.New("question has no issue")
	}
	if len(q.Options) < 2 {
		return fmt.Errorf("question needs at least two options, got %d", len(q.Options))
	}
	seen := map[string]bool{}
	for _, o := range q.Options {
		label := strings.ToUpper(strings.TrimSpace(o.Label))
		if label == "" || seen[label] {
			return fmt.Errorf("option labels must be unique and non-empty, got %q", o.Label)
		}
		seen[label] = true
	}
	return nil
}

// Option finds an option by label, case-insensitively.
func (q Question) Option(label string) (Option, bool) {
	for _, o := range q.Options {
		if strings.EqualFold(strings.TrimSpace(o.Label), strings.TrimSpace(label)) {
			return o, true
		}
	}
	return Option{}, false
}

// Recommended resolves the recommendation to an option. Agents are told to give the
// bare label, but also write "A", "A.", "A: because…" or "Option A – …".
func (q Question) Recommended() (Option, bool) {
	rec := strings.TrimSpace(q.Recommendation)
	rec = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(rec, "Option "), "option "))
	for _, o := range q.Options {
		label := strings.TrimSpace(o.Label)
		if label == "" || len(rec) < len(label) || !strings.EqualFold(rec[:len(label)], label) {
			continue
		}
		if rest := rec[len(label):]; rest == "" || !isWordChar(rest[0]) {
			return o, true
		}
	}
	return Option{}, false
}

func isWordChar(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

type Option struct {
	Label       string `json:"label" yaml:"label"`
	Description string `json:"description" yaml:"description"`
}

type Decision struct {
	AskedAt    time.Time
	Question   Question
	Answer     string
	AnsweredAt time.Time
	// DeliveredAt is set once a runner has handed the answer to the agent.
	DeliveredAt time.Time
	// Diagnosis is why the self-review loop stalled at the fix limit, kept so a restart
	// does not diagnose again. Nil on every other decision.
	Diagnosis *Diagnosis
}

// DiagnosisKind is the cause a stalled self-review loop was traced to.
type DiagnosisKind string

const (
	// DiagnosedPracticeGap means the implementer missed a generic practice, which
	// learning may take up.
	DiagnosedPracticeGap DiagnosisKind = "practice_gap"
	// DiagnosedConflict means statements in the implementer's context contradict.
	DiagnosedConflict DiagnosisKind = "conflict"
	// DiagnosedNone means no concrete cause was found, or the diagnosis was unavailable.
	DiagnosedNone DiagnosisKind = "none"
)

// Diagnosis is the outcome of the diagnosis turn at the self-review fix limit.
type Diagnosis struct {
	Kind      DiagnosisKind     `yaml:"kind"`
	Summary   string            `yaml:"summary,omitempty"`
	Conflicts []ContextConflict `yaml:"conflicts,omitempty"`
}

// ContextConflict is two statements given to the implementer that contradict each
// other. Each quote was found verbatim in its named source.
type ContextConflict struct {
	Point  string    `yaml:"point"`
	First  Statement `yaml:"first"`
	Second Statement `yaml:"second"`
}

type Statement struct {
	Source string `yaml:"source"`
	Quote  string `yaml:"quote"`
}

// Pending is the oldest unanswered decision, if any. A blocked turn can ask several
// questions; they are answered in the order they were asked.
func (r *Record) Pending() *Decision {
	for i := range r.Decisions {
		if r.Decisions[i].Answer == "" {
			return &r.Decisions[i]
		}
	}
	return nil
}

// PendingCount is how many decisions are waiting for an answer.
func (r *Record) PendingCount() int {
	n := 0
	for _, d := range r.Decisions {
		if d.Answer == "" {
			n++
		}
	}
	return n
}

// Undelivered lists the answered decisions the agent has not received yet.
func (r *Record) Undelivered() []Decision {
	var out []Decision
	for _, d := range r.Decisions {
		if d.Answer != "" && d.DeliveredAt.IsZero() {
			out = append(out, d)
		}
	}
	return out
}

type Record struct {
	Key
	Identifier     string
	Kind           Kind
	Title          string
	URL            string
	State          State
	Delegation     *Delegation
	GitHub         *Snapshot
	LastGitHubSync time.Time
	Decisions      []Decision
	LastOutcome    *Outcome
	Calls          []ModelCall
	Usage          Usage
	// Validation is what the implementation agent reported with its PR.
	Validation []Check
	// FullResult is the latest full-suite report Foreman verified. Handoff needs one
	// that covers the change it hands off.
	FullResult *FullResult
	// Reviews are the review rounds, oldest first.
	Reviews []Review
	// Plan holds the planning documents posted on the issue, in PlanDocs order.
	Plan []PlanComment
	// Feedback is every comment on the task's PR foreman has read, and whether the
	// agent has answered it.
	Feedback []Feedback
	// CI is the readiness gate's last look at the PR's checks on GitHub. Any implementer
	// turn clears it.
	CI *CIStatus
	// Attempts are the runs of work on the issue, one per PR, oldest first.
	Attempts []Attempt
	// Reset is set when the developer discarded a failed run. The record then has no
	// delegation, and auto mode leaves the issue for a manual start.
	Reset *ResetMark
	// Body is the Markdown below the frontmatter, round-tripped untouched so human edits survive.
	Body string
}

func (r *Record) IssueSource() Source {
	if r.Source == Linear {
		return Linear
	}
	return GitHub
}

// PlanDoc is one planning document the implementer writes before any code.
type PlanDoc string

const (
	Analysis           PlanDoc = "analysis"
	PRD                PlanDoc = "prd"
	Design             PlanDoc = "design"
	ImplementationPlan PlanDoc = "plan"
)

// PlanDocs lists the documents in the order they are written and posted.
var PlanDocs = []PlanDoc{PRD, ImplementationPlan}

// Title is the document's heading in its issue comment.
func (d PlanDoc) Title() string {
	switch d {
	case Analysis:
		return "Analysis"
	case PRD:
		return "Requirements"
	case Design:
		return "Design"
	case ImplementationPlan:
		return "Work plan"
	}
	return string(d)
}

// PlanComment is a document foreman posted on the issue as its own comment.
type PlanComment struct {
	Doc               PlanDoc `yaml:"doc"`
	CommentID         int64   `yaml:"comment_id,omitempty"`
	ExternalCommentID string  `yaml:"external_comment_id,omitempty"`
	URL               string  `yaml:"url"`
}

// PlanComment returns the posted comment for doc, or nil.
func (r *Record) PlanComment(doc PlanDoc) *PlanComment {
	for i := range r.Plan {
		if r.Plan[i].Doc == doc {
			return &r.Plan[i]
		}
	}
	return nil
}

// PlanPosted reports whether every planning document is on the issue.
func (r *Record) PlanPosted() bool {
	for _, d := range PlanDocs {
		if c := r.PlanComment(d); c == nil || c.URL == "" {
			return false
		}
	}
	return true
}

// SetPlanComment records a posted comment, replacing an earlier one for the same doc.
func (r *Record) SetPlanComment(c PlanComment) {
	if cur := r.PlanComment(c.Doc); cur != nil {
		*cur = c
		return
	}
	r.Plan = append(r.Plan, c)
}

type Finding struct {
	Severity string `json:"severity" yaml:"severity"` // blocking, should_fix
	File     string `json:"file" yaml:"file,omitempty"`
	Line     int    `json:"line" yaml:"line,omitempty"`
	Summary  string `json:"summary" yaml:"summary"`
	Detail   string `json:"detail" yaml:"detail,omitempty"`
}

// ReviewKind tells the implementer's own review from the competing agent's.
type ReviewKind string

const (
	// CompetingReview is the empty kind, so records from before self-reviews read as
	// competing reviews and keep passing the gate.
	CompetingReview ReviewKind = ""
	// SelfReview is the implementing agent's review in a fresh, read-only session. It
	// must be clean before the competing review runs, and it never passes the gate.
	SelfReview ReviewKind = "self"
)

type Review struct {
	Round    int
	Kind     ReviewKind
	Reviewer Agent
	PR       int
	Clean    bool
	Summary  string
	Findings []Finding
	At       time.Time
	Head     string // the PR head commit reviewed; empty on records from before heads were recorded
	// Proposal is the ProposalDigest of the title and description the review read with
	// Head: the proposed ones for a local review, the PR's for a self-review of a PR.
	// A fix may keep the commit and change only those.
	Proposal string
	// Claims and PRD are the ClaimsDigest and the PRD digest the review read, for a
	// task with claim verification.
	Claims string
	PRD    string
	Scope  string // confirmed triage scope the reviewer checked
	Plan   string // planning documents the reviewer checked
	// Limitations are the behaviors the reviewer could not check.
	Limitations []Limitation
}

// LatestReview is the most recent review round of either kind, or nil. Its findings
// are the ones the implementer fixes next.
func (r *Record) LatestReview() *Review {
	if len(r.Reviews) == 0 {
		return nil
	}
	return &r.Reviews[len(r.Reviews)-1]
}

// LatestCompeting is the most recent competing review, or nil. Only it can pass the
// gate or open a PR.
func (r *Record) LatestCompeting() *Review { return r.latest(CompetingReview) }

// LatestSelf is the most recent self-review, or nil.
func (r *Record) LatestSelf() *Review { return r.latest(SelfReview) }

func (r *Record) latest(kind ReviewKind) *Review {
	for i := len(r.Reviews) - 1; i >= 0; i-- {
		if r.Reviews[i].Kind == kind {
			return &r.Reviews[i]
		}
	}
	return nil
}

// CompetingReviews are the competing rounds alone, oldest first. The competing
// reviewer sees only these, so self-review findings never reach it.
func (r *Record) CompetingReviews() []Review {
	var out []Review
	for _, rv := range r.Reviews {
		if rv.Kind == CompetingReview {
			out = append(out, rv)
		}
	}
	return out
}

// CurrentChange is the change a review now would read: the local commit with its
// proposed title and description, or the PR head with the PR description the review
// step last read.
func (r *Record) CurrentChange() (head, proposal string) {
	d := r.Delegation
	if r.LocalReview() {
		return d.LocalHead, ProposalDigest(d.ProposedTitle, d.ProposedBody)
	}
	return d.PRHead, d.PRDigest
}

// selfCovers reports whether a self-review read the current change and description.
// Coverage is derived from the stored values alone, so a restart never treats a pass
// of another head or description as current.
func (r *Record) selfCovers(review *Review) bool {
	head, proposal := r.CurrentChange()
	return review != nil && review.Kind == SelfReview && review.PR == r.Delegation.PR && review.Head == head && review.Proposal == proposal && r.ClaimsCovered(review) && r.ScopeCovered(review)
}

// SelfReviewPassed reports a clean self-review of the current change and description.
func (r *Record) SelfReviewPassed() bool {
	latest := r.LatestSelf()
	return r.selfCovers(latest) && latest.Clean
}

// NextReview is the review the current change needs next: the self-review until a
// clean one covers it, then the competing review.
func (r *Record) NextReview() ReviewKind {
	if r.SelfReviewPassed() {
		return CompetingReview
	}
	return SelfReview
}

// ChangeRequestsSinceDecision counts review rounds that requested changes since the
// developer last answered a question. The round cap limits fix rounds, so
// clean re-reviews (after a CI fix) don't use it up; answering resets it.
func (r *Record) ChangeRequestsSinceDecision() int {
	var last time.Time
	for _, d := range r.Decisions {
		if d.AnsweredAt.After(last) {
			last = d.AnsweredAt
		}
	}
	n := 0
	for _, rv := range r.Reviews {
		if !rv.Clean && rv.At.After(last) {
			n++
		}
	}
	return n
}

type CIState string

const (
	CIPending CIState = "pending"
	CIPassing CIState = "passing"
	CIFailing CIState = "failing"
	// CINone means the PR has no checks at all. The gate treats it as passing.
	CINone CIState = "none"
)

// CheckRun is one failing or pending CI check, kept so the implementer and the
// developer can see which check to open.
type CheckRun struct {
	Name string `json:"name" yaml:"name"`
	Link string `json:"link" yaml:"link,omitempty"`
}

type CIStatus struct {
	State CIState
	// Failing lists the checks that failed or were cancelled.
	Failing   []CheckRun
	CheckedAt time.Time
}

// Green reports whether CI lets the task through the readiness gate.
func (c *CIStatus) Green() bool {
	return c != nil && (c.State == CIPassing || c.State == CINone)
}

// CommitsSinceReview reports whether the PR head moved after its latest competing review.
func (r *Record) CommitsSinceReview() bool {
	latest := r.LatestCompeting()
	return r.Delegation != nil && latest != nil && latest.Head != "" && r.Delegation.PRHead != "" && latest.Head != r.Delegation.PRHead
}

// ReviewPending reports whether the verified PR head or local commit has no competing
// review, so a run continues with the review step instead of implementing again: after
// a crash during a review, or after new commits on a PR that had a clean review. Open
// findings from either reviewer on the current change go to the implementer instead.
func (r *Record) ReviewPending() bool {
	d := r.Delegation
	if d == nil || len(r.Undelivered()) > 0 {
		return false
	}
	if !r.LocalReview() && (d.PR == 0 || d.PRHead == "") {
		return false
	}
	self := r.LatestSelf()
	if r.selfCovers(self) && self.Clean && r.newer(self, r.LatestCompeting()) {
		// The change passed its self-review after its last competing review, which a
		// restart must not skip even when that review read the same head.
		return true
	}
	if r.competingCovers(r.LatestCompeting()) {
		return false
	}
	return !r.selfCovers(self) || self.Clean
}

// newer reports whether review a comes after review b in the record. Both point into
// r.Reviews, as LatestSelf and LatestCompeting return; any review is newer than none.
func (r *Record) newer(a, b *Review) bool {
	if b == nil {
		return a != nil
	}
	for i := range r.Reviews {
		switch &r.Reviews[i] {
		case a:
			return false
		case b:
			return a != nil
		}
	}
	return false
}

// competingCovers reports whether a competing review read the current change: the
// local commit with its proposal, or the PR head with its description. A review or a
// task from before descriptions were recorded has no digest, and covers its head
// alone, so older tasks are not reviewed again.
func (r *Record) competingCovers(review *Review) bool {
	d := r.Delegation
	if review == nil || review.Kind != CompetingReview {
		return false
	}
	if !r.ClaimsCovered(review) {
		return false
	}
	if !r.ScopeCovered(review) {
		return false
	}
	if r.LocalReview() {
		return r.coversLocal(review)
	}
	return review.PR == d.PR && review.Head == d.PRHead && (review.Proposal == "" || d.PRDigest == "" || review.Proposal == d.PRDigest)
}

func (r *Record) ScopeCovered(review *Review) bool {
	if review == nil {
		return false
	}
	if r.Delegation == nil || r.Delegation.ScopeDigest == "" {
		return true
	}
	return review.Scope == r.Delegation.ScopeDigest && review.Plan == r.Delegation.PlanDigest
}

// BeforePR reports a task reviewed before its PR opens that has no PR yet. Its
// implementer commits locally and proposes the PR instead of opening it.
func (d *Delegation) BeforePR() bool {
	return d != nil && d.ReviewBeforePR && d.PR == 0
}

// LocalReview reports a task reviewed before its PR opens that has a local commit
// for review. Once the PR exists, the task follows the PR workflow.
func (r *Record) LocalReview() bool {
	return r.Delegation.BeforePR() && r.Delegation.LocalHead != ""
}

// OpenPending reports a local commit with a clean competing review and no PR yet, so
// the next step pushes that commit and opens the PR with the reviewed description.
// Answers the implementer has not received go to it with that step.
func (r *Record) OpenPending() bool {
	if !r.LocalReview() {
		return false
	}
	latest := r.LatestCompeting()
	return r.competingCovers(latest) && latest.Clean
}

// FinalCheckPending reports a change with a clean competing review that cannot be
// handed off yet, because no full-suite result covers it. Its next turn is the
// implementer's final check.
func (r *Record) FinalCheckPending() bool {
	d := r.Delegation
	if d == nil || !d.FullSuiteGate {
		return false
	}
	latest := r.LatestCompeting()
	head, _ := r.CurrentChange()
	return latest != nil && latest.Clean && r.competingCovers(latest) && !r.FullResult.Covers(head)
}

// coversLocal reports whether review read the stored local commit together with its
// current proposed title and description.
func (r *Record) coversLocal(review *Review) bool {
	d := r.Delegation
	return review != nil && review.Head == d.LocalHead && review.Proposal == ProposalDigest(d.ProposedTitle, d.ProposedBody)
}

// ProposalDigest identifies a proposed PR title and description, so a review records
// which proposal it read. Line endings and surrounding space are ignored, because the
// PR on GitHub can differ from the reviewed text in those alone.
func ProposalDigest(title, body string) string {
	return Digest(strings.TrimSpace(title) + "\n\n" + strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n")))
}

// ReviewStage is the stage of the review the current change needs next.
func (r *Record) ReviewStage() State {
	if r.NextReview() == SelfReview {
		return SelfReviewing
	}
	return AdversarialReviewing
}

// NextStage is the stage a run started now begins with. It follows the runner's own
// order: a conflict repair or an undelivered answer goes to the implementer, then a
// reviewed local commit opens its PR, then a change without its reviews is reviewed,
// then the plan, then implementation.
func (r *Record) NextStage() State {
	d := r.Delegation
	switch {
	case d == nil:
		return Planning
	case d.ConflictPending, r.FinalCheckPending():
		return Implementing
	case r.OpenPending():
		return OpeningPR
	case r.ReviewPending():
		return r.ReviewStage()
	case !r.PlanPosted() && d.PR == 0:
		return Planning
	}
	return Implementing
}

// UpgradeState maps a state from before the stages to the stage the record shows is
// current. A REVIEWING task whose clean competing review covers the recorded change
// waits for CI. The next write saves the new state.
func (r *Record) UpgradeState() {
	switch r.State {
	case LegacyWorking:
		r.State = r.NextStage()
	case LegacyReviewing:
		r.State = r.NextStage()
		if latest := r.LatestCompeting(); r.Delegation != nil && r.Delegation.PR != 0 && r.competingCovers(latest) && latest.Clean {
			r.State = WaitingForCI
		}
	}
}
