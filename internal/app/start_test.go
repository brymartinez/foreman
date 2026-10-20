package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/task"
)

type fakeWorktrees struct {
	reqs            []WorktreeRequest
	head            string
	checkpointHeads []string
	checkpointPrior []string          // the prior commit of each Checkpoint call
	remoteHeads     map[string]string // branches on origin, for RemoteBranchHead
	checkpointErr   error
	removed         []WorktreeRemoval
	removeAttempts  []WorktreeRemoval
	removeErr       error
	onRemove        func() // runs inside Remove, before it records the removal
	ensureErr       error
	onEnsure        func()          // runs inside Ensure
	branches        map[string]bool // local branches BranchExists reports
	common          string          // CommonDir's answer
	// contains answers Contains; nil reports every ancestor as merged.
	contains  func(dir, ancestor, descendant string) (bool, error)
	checks    []MergeCheck
	conflicts [][]string // consumed one per check; clean once empty
	checkErr  error
	// identities records each ConfigureIdentity call as "dir name <email>".
	identities  []string
	identityErr error
	// syncs records each SyncDefaultBranch call as "dir branch"; onSync runs inside it.
	syncs   []string
	syncErr error
	onSync  func(dir string)
	// changed answers ChangedPaths by "from..to"; a pair it lacks is not an ancestor.
	changed map[string][]string
}

func (f *fakeWorktrees) ChangedPaths(_ context.Context, _, from, to string) ([]string, error) {
	if from == to {
		return nil, nil
	}
	paths, ok := f.changed[from+".."+to]
	if !ok {
		return nil, fmt.Errorf("%s is not an ancestor of %s", from, to)
	}
	return paths, nil
}

func (f *fakeWorktrees) SyncDefaultBranch(_ context.Context, dir, branch string) error {
	f.syncs = append(f.syncs, dir+" "+branch)
	if f.syncErr != nil {
		return f.syncErr
	}
	if f.onSync != nil {
		f.onSync(dir)
	}
	return nil
}

func (f *fakeWorktrees) ConfigureIdentity(_ context.Context, dir, name, email string) error {
	f.identities = append(f.identities, dir+" "+name+" <"+email+">")
	return f.identityErr
}

// fakeAccounts plays gh's active account and the SSH key's account. Both start as
// octo-work, the account most tests pin.
type fakeAccounts struct {
	active  Account
	err     error
	ssh     []SSHAccount
	sshErr  error
	lookups int
}

func (f *fakeAccounts) ActiveAccount(context.Context) (Account, error) {
	f.lookups++
	return f.active, f.err
}

func (f *fakeAccounts) PushAccounts(context.Context, string) ([]SSHAccount, error) {
	return f.ssh, f.sshErr
}

func (f *fakeWorktrees) Head(_ context.Context, _ string) (string, error) {
	if f.head == "" {
		return "basehead", nil
	}
	return f.head, nil
}

func (f *fakeWorktrees) Checkpoint(_ context.Context, _, _, prior string) (string, error) {
	return f.committedHead(prior, false)
}

func (f *fakeWorktrees) ReviewHead(_ context.Context, _, _, prior string) (string, error) {
	return f.committedHead(prior, true)
}

func (f *fakeWorktrees) committedHead(prior string, allowSame bool) (string, error) {
	f.checkpointPrior = append(f.checkpointPrior, prior)
	if f.checkpointErr != nil {
		return "", f.checkpointErr
	}
	if len(f.checkpointHeads) == 0 {
		return "", errors.New("no checkpoint head")
	}
	head := f.checkpointHeads[0]
	f.checkpointHeads = f.checkpointHeads[1:]
	if head == prior && !allowSame {
		return "", errors.New("checkpoint has no new commit")
	}
	return head, nil
}

func (f *fakeWorktrees) RemoteBranchHead(_ context.Context, _, branch string) (string, error) {
	return f.remoteHeads[branch], nil
}

func (f *fakeWorktrees) Contains(_ context.Context, dir, ancestor, descendant string) (bool, error) {
	if f.contains == nil {
		return true, nil
	}
	return f.contains(dir, ancestor, descendant)
}

func (f *fakeWorktrees) MergeConflicts(_ context.Context, c MergeCheck) ([]string, error) {
	f.checks = append(f.checks, c)
	if f.checkErr != nil {
		return nil, f.checkErr
	}
	if len(f.conflicts) == 0 {
		return nil, nil
	}
	files := f.conflicts[0]
	f.conflicts = f.conflicts[1:]
	return files, nil
}

type fakeRepositories struct {
	path  string
	info  RepoInfo
	calls []task.Repo
}

func (f *fakeRepositories) Ensure(_ context.Context, repo task.Repo, _ string) (RepoInfo, error) {
	f.calls = append(f.calls, repo)
	return f.info, nil
}

func (f *fakeWorktrees) Remove(_ context.Context, r WorktreeRemoval) error {
	f.removeAttempts = append(f.removeAttempts, r)
	if f.onRemove != nil {
		f.onRemove()
	}
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removed = append(f.removed, r)
	return nil
}

func (f *fakeWorktrees) CommonDir(context.Context, string) (string, error) {
	return f.common, nil
}

func (f *fakeWorktrees) BranchExists(_ context.Context, _, branch string) (bool, error) {
	return f.branches[branch], nil
}

func (f *fakeWorktrees) Ensure(_ context.Context, r WorktreeRequest) error {
	f.reqs = append(f.reqs, r)
	if f.onEnsure != nil {
		f.onEnsure()
	}
	return f.ensureErr
}

// fakeStages returns "<stage> instructions" for every stage and records each lookup.
type fakeStages struct {
	calls []string
	err   error
}

func (f *fakeStages) Resolve(repoRoot string, s task.Stage) (task.StageInstructions, error) {
	f.calls = append(f.calls, repoRoot+" "+string(s))
	if f.err != nil {
		return task.StageInstructions{}, f.err
	}
	return task.StageInstructions{Stage: s, GlobalPath: "/cfg/stages/" + string(s) + ".md", Global: string(s) + " instructions"}, nil
}

type fakeRunners struct {
	spawned []task.Key
	tokens  []string // the launch token each spawn was given
	// entered, when set, receives once the next spawn is about to start a runner,
	// which then waits on release. Later spawns do not wait.
	entered  chan struct{}
	release  chan struct{}
	alive    map[int]bool
	stopped  []int
	onSpawn  func()
	onStop   func(pid int) // plays the runner recording its stop
	stuck    bool
	stopErr  error
	spawnErr error
	learned  []task.Key
	learnErr error
}

func (f *fakeRunners) SpawnLearn(k task.Key) error {
	f.learned = append(f.learned, k)
	return f.learnErr
}

func (f *fakeRunners) Spawn(k task.Key, token string) (int, string, error) {
	f.spawned = append(f.spawned, k)
	if f.onSpawn != nil {
		f.onSpawn()
	}
	f.tokens = append(f.tokens, token)
	if entered := f.entered; entered != nil {
		f.entered = nil
		entered <- struct{}{}
		<-f.release
	}
	if f.spawnErr != nil {
		return 0, "", f.spawnErr
	}
	return 4242, "/logs/" + string(k.Repo) + ".log", nil
}
func (f *fakeRunners) Alive(pid int) bool { return f.alive[pid] }
func (f *fakeRunners) Stop(_ task.Key, pid int) (bool, error) {
	f.stopped = append(f.stopped, pid)
	if f.stopErr != nil {
		return false, f.stopErr
	}
	if f.stuck {
		return false, nil
	}
	if f.onStop != nil {
		f.onStop(pid)
	}
	delete(f.alive, pid)
	return true, nil
}

type fakeAgent struct {
	name     task.Agent
	turn     agent.Turn
	turns    []agent.Turn // consumed before turn, one per Start/Resume
	turnErrs []error      // consumed with turns
	err      error
	prompt   string
	prompts  []string
	starts   int
	resumed  []string
	stages   []task.Stage
	profiles []*task.Profile // StartInput.Profile of each Start and Resume
	steps    []string        // StartInput.Step of each Start, Resume, and Review
	// reviewBudgets is StartInput.MaxBudgetUSD of each Review.
	reviewBudgets []float64
	resumeErr     error
	reviews       []agent.ReviewOutcome // consumed in order; clean once empty
	scopeReviews  []agent.ReviewOutcome
	scopeReviewed []string
	reviewErr     error
	reviewed      []string
	during        func() // runs inside Start, while the turn is in progress
	onResume      func() // the same, inside Resume
	onReview      func() // the same, inside Review
}

func (f *fakeAgent) next() (agent.Turn, error) {
	if len(f.turns) > 0 {
		t := f.turns[0]
		f.turns = f.turns[1:]
		if len(f.turnErrs) > 0 {
			err := f.turnErrs[0]
			f.turnErrs = f.turnErrs[1:]
			return t, err
		}
		return t, nil
	}
	return f.turn, f.err
}

func (f *fakeAgent) Review(_ context.Context, in agent.StartInput) (agent.ReviewOutcome, error) {
	if strings.HasPrefix(in.Prompt, "You are checking planning documents") {
		f.scopeReviewed = append(f.scopeReviewed, in.Prompt)
		if len(f.scopeReviews) > 0 {
			r := f.scopeReviews[0]
			f.scopeReviews = f.scopeReviews[1:]
			return r, nil
		}
		return agent.ReviewOutcome{Clean: true, Summary: "plan agrees with triage"}, nil
	}
	f.reviewed = append(f.reviewed, in.Prompt)
	f.reviewBudgets = append(f.reviewBudgets, in.MaxBudgetUSD)
	f.steps = append(f.steps, in.Step)
	if f.onReview != nil {
		f.onReview()
	}
	if f.reviewErr != nil {
		return agent.ReviewOutcome{}, f.reviewErr
	}
	if len(f.reviews) == 0 {
		return agent.ReviewOutcome{Clean: true, Summary: "looks good"}, nil
	}
	r := f.reviews[0]
	f.reviews = f.reviews[1:]
	return r, nil
}

func (f *fakeAgent) Name() task.Agent { return f.name }
func (f *fakeAgent) Resume(_ context.Context, s task.Session, in agent.StartInput) (agent.Turn, error) {
	f.resumed = append(f.resumed, s.ID+": "+in.Prompt)
	f.profiles = append(f.profiles, in.Profile)
	f.stages = append(f.stages, in.Stage)
	f.steps = append(f.steps, in.Step)
	if f.onResume != nil {
		f.onResume()
	}
	if f.resumeErr != nil {
		return agent.Turn{}, f.resumeErr
	}
	return f.next()
}

func (f *fakeAgent) Start(_ context.Context, in agent.StartInput, onSession func(task.Session)) (agent.Turn, error) {
	f.starts++
	f.profiles = append(f.profiles, in.Profile)
	f.stages = append(f.stages, in.Stage)
	f.steps = append(f.steps, in.Step)
	if f.during != nil {
		f.during()
	}
	f.prompt = in.Prompt
	f.prompts = append(f.prompts, in.Prompt)
	t, err := f.next()
	onSession(t.Session)
	return t, err
}

type fixture struct {
	app     App
	store   *fakeStore
	gh      *fakeGitHub
	wt      *fakeWorktrees
	runners *fakeRunners
	stages  *fakeStages
	repos   *fakeRepositories
	claude  *fakeAgent
	codex   *fakeAgent
	notes   *fakeNotifier
	accts   *fakeAccounts
}

func newFixture() fixture {
	f := fixture{
		store: &fakeStore{recs: map[task.Key]*task.Record{}},
		gh: &fakeGitHub{
			repo:  RepoInfo{Repo: "testorg/example", DefaultBranch: "main"},
			issue: Issue{Title: "Provider retries", Body: "Retry failed requests.", URL: "https://x/825", State: "open", Labels: []string{"triaged"}, UpdatedAt: t0},
			prs: map[int]PullRequest{912: {
				Key: task.Key{Repo: "testorg/example", Number: 912}, URL: "https://github.com/testorg/example/pull/912",
				State: "open", HeadRefName: "feat/825-provider-retries", BaseRefName: "main", HeadSHA: "c0ffee1",
				Body: "Retry 5xx only.\n\nCloses #825",
			}},
		},
		wt:      &fakeWorktrees{},
		runners: &fakeRunners{alive: map[int]bool{}},
		stages:  &fakeStages{},
		repos:   &fakeRepositories{info: RepoInfo{Repo: "testorg/example", DefaultBranch: "main"}},
		claude:  &fakeAgent{name: task.Claude},
		codex:   &fakeAgent{name: task.Codex},
		notes:   &fakeNotifier{},
		accts: &fakeAccounts{
			active: Account{Login: "octo-work", ID: 42},
			ssh:    []SSHAccount{{Host: "git@github-work", Login: "octo-work"}},
		},
	}
	f.app = App{
		Notifier:          f.notes,
		Accounts:          f.accts,
		RemoteAuth:        f.accts,
		ReplyToPRFeedback: true,
		Store:             f.store, GitHub: f.gh, Worktrees: f.wt, Runners: f.runners, Stages: f.stages,
		Agents:       map[task.Agent]AgentAdapter{task.Claude: f.claude, task.Codex: f.codex},
		WorktreesDir: "/wt", Repositories: f.repos, RepositoriesDir: "/repos",
		Now: func() time.Time { return now },
	}
	return f
}

// run plays the runner the task's launch spawned. A record a test wrote by hand
// gets a launch claim first, as a launch would have taken.
func (f fixture) run(ctx context.Context, key task.Key, pid int, log io.Writer) error {
	token := ""
	if rec := f.store.recs[key]; rec != nil && rec.Delegation != nil {
		if rec.Delegation.Launch == nil {
			rec.Delegation.Launch = &task.Launch{Token: "test-launch", PID: os.Getpid(), Since: now}
		}
		token = rec.Delegation.Launch.Token
	}
	return f.app.RunTask(ctx, key, pid, token, log)
}

func TestStartFromInboxUsesRepositoryCheckoutInsteadOfCurrentDirectory(t *testing.T) {
	f := newFixture()
	rec, err := f.app.Start(context.Background(), StartRequest{Repo: "testorg/example", Issue: 825, Agent: task.Claude})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.repos.calls) != 1 || f.repos.calls[0] != "testorg/example" {
		t.Fatalf("repository checkout calls = %v", f.repos.calls)
	}
	if len(f.wt.reqs) != 1 || f.wt.reqs[0].RepoDir != "/repos/testorg-example" {
		t.Fatalf("worktree source = %+v", f.wt.reqs)
	}
	if rec.Repo != "testorg/example" {
		t.Fatalf("record repo = %s", rec.Repo)
	}
}

func TestNewTaskWorktreeUsesFullBranchName(t *testing.T) {
	for _, tc := range []struct {
		name   string
		branch string
	}{
		{"edited branch", "fix/825-provider-retries"},
		{"branch with several components", "bryan/db/825-provider-retries"},
		{"default branch", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()
			rec, err := f.app.Start(context.Background(), StartRequest{Dir: "/src/example", Issue: 825, Agent: task.Claude, BranchName: tc.branch})
			if err != nil {
				t.Fatal(err)
			}
			branch := tc.branch
			if branch == "" {
				branch = "feat/825-provider-retries"
			}
			want := filepath.Join("/wt/testorg-example", branch)
			if f.wt.reqs[0].Path != want || rec.Delegation.Worktree != want {
				t.Fatalf("worktree request %q, stored %q, want %q", f.wt.reqs[0].Path, rec.Delegation.Worktree, want)
			}
		})
	}
}

func TestRestartKeepsStoredWorktreePath(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: "testorg/example", Number: 825}
	want := "/wt/testorg-example/legacy/825-provider-retries"
	f.store.recs[key] = &task.Record{Key: key, Kind: task.Issue, State: task.Failed,
		Delegation: &task.Delegation{Branch: "fix/825-provider-retries", Worktree: want}}
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if f.wt.reqs[0].Path != want || rec.Delegation.Worktree != want {
		t.Fatalf("worktree request %q, stored %q, want %q", f.wt.reqs[0].Path, rec.Delegation.Worktree, want)
	}
}

func startReq(t *testing.T) StartRequest {
	ctxFile := filepath.Join(t.TempDir(), "825.md")
	os.WriteFile(ctxFile, []byte("Preserve the existing 5xx-only retry semantics."), 0o644)
	return StartRequest{Issue: 825, Agent: task.Claude, ContextFile: ctxFile, Dir: "/src/example"}
}

func TestStartDispatches(t *testing.T) {
	f := newFixture()
	req := startReq(t)
	rec, err := f.app.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	wantWT := WorktreeRequest{
		RepoDir: "/src/example",
		Path:    "/wt/testorg-example/feat/825-provider-retries",
		Branch:  "feat/825-provider-retries",
		BaseRef: "origin/main",
	}
	if len(f.wt.reqs) != 1 || f.wt.reqs[0] != wantWT {
		t.Errorf("worktree = %+v", f.wt.reqs)
	}
	d := rec.Delegation
	if rec.State != task.Planning || d.Implementer.Agent != task.Claude || d.Reviewer != task.Codex ||
		d.Branch != wantWT.Branch || d.Worktree != wantWT.Path || d.ContextFile != req.ContextFile ||
		d.RunnerPID != 4242 || d.RunnerLog == "" || d.StartedAt.IsZero() || rec.Title != "Provider retries" {
		t.Errorf("record = %+v / %+v", rec, d)
	}
	if len(f.runners.spawned) != 1 || !strings.HasPrefix(rec.Body, "# #825 Provider retries") {
		t.Errorf("spawned %v body %q", f.runners.spawned, rec.Body)
	}
}

func TestStartRefuses(t *testing.T) {
	cases := map[string]func(f *fixture, r *StartRequest){
		"other repo":      func(f *fixture, r *StartRequest) { r.Repo = "testorg/other-example" },
		"closed issue":    func(f *fixture, r *StartRequest) { f.gh.issue.State = "closed" },
		"missing context": func(f *fixture, r *StartRequest) { r.ContextFile = "/nope/825.md" },
		"running": func(f *fixture, r *StartRequest) {
			rec := seen(825, task.Implementing, "open", t0)
			rec.Repo = "testorg/example"
			rec.Key = task.Key{Repo: "testorg/example", Number: 825}
			rec.Delegation = &task.Delegation{RunnerPID: 77}
			f.store.recs[rec.Key] = rec
			f.runners.alive[77] = true
		},
		"needs decision": func(f *fixture, r *StartRequest) {
			rec := seen(825, task.NeedsDecision, "open", t0)
			rec.Key = task.Key{Repo: "testorg/example", Number: 825}
			f.store.recs[rec.Key] = rec
		},
		// The gate leaves a PR_CLOSED task its pid until the stop succeeds, and
		// that runner is still working in the worktree a second one would share.
		"PR_CLOSED with a live runner": func(f *fixture, r *StartRequest) {
			rec := seen(825, task.PRClosed, "open", t0)
			rec.Key = task.Key{Repo: "testorg/example", Number: 825}
			rec.Delegation = &task.Delegation{Implementer: task.Session{Agent: task.Claude}, RunnerPID: 77}
			f.store.recs[rec.Key] = rec
			f.runners.alive[77] = true
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			req := startReq(t)
			mutate(&f, &req)
			if _, err := f.app.Start(context.Background(), req); err == nil {
				t.Fatal("start succeeded")
			}
			if len(f.runners.spawned) != 0 {
				t.Error("runner spawned despite refusal")
			}
		})
	}
}

func TestStartAcceptsPRClosed(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: "testorg/example", Number: 825}
	f.store.recs[key] = &task.Record{Key: key, Kind: task.Issue, State: task.PRClosed,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Branch: "feat/825-provider-retries", PR: 912}}
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != task.Planning || len(f.runners.spawned) != 1 {
		t.Errorf("state %s spawned %d", rec.State, len(f.runners.spawned))
	}
	// Keeping the closed PR's number would have the gate end this run on its next
	// refresh, and a closed PR cannot be picked up at its review anyway.
	if rec.Delegation.PR != 0 || rec.Delegation.Branch != "feat/825-provider-retries" {
		t.Errorf("the restart carried the closed PR: pr %d branch %s", rec.Delegation.PR, rec.Delegation.Branch)
	}
}

func TestRestartWithAnotherAgentKeepsThePRWithCommentsWaiting(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: "testorg/example", Number: 825}
	f.store.recs[key] = &task.Record{Key: key, Kind: task.Issue, State: task.Failed,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Codex, ID: "old"}, Branch: "feat/825-provider-retries", PR: 912, PRHead: "abc"},
		Feedback:   []task.Feedback{{Kind: task.IssueComment, ID: 1, Digest: "d"}}}
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	d := rec.Delegation
	if d.Implementer.Agent != task.Claude || d.Implementer.ID != "" {
		t.Errorf("implementer = %+v, want a fresh claude session", d.Implementer)
	}
	if d.PR != 912 || d.PRHead != "abc" || len(rec.UnhandledFeedback()) != 1 {
		t.Errorf("pr %d head %q unanswered %d; the comments lost their PR", d.PR, d.PRHead, len(rec.UnhandledFeedback()))
	}
}

func TestRestartWithAnotherAgentStartsOverWithNoCommentsWaiting(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: "testorg/example", Number: 825}
	f.store.recs[key] = &task.Record{Key: key, Kind: task.Issue, State: task.Failed,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Codex}, Branch: "feat/825-provider-retries", PR: 912}}
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Delegation.PR != 0 {
		t.Errorf("pr = %d; another implementer starts over", rec.Delegation.PR)
	}
}

func TestRestartKeepsBranchWhenTitleChanged(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: "testorg/example", Number: 825}
	f.store.recs[key] = &task.Record{Key: key, Kind: task.Issue, State: task.Failed,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Codex}, Branch: "agent/825-old-title", Worktree: "/wt/testorg-example/825-old-title"}}
	f.gh.issue.Title = "A completely new title"
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Delegation.Branch != "agent/825-old-title" || f.wt.reqs[0].Path != "/wt/testorg-example/825-old-title" {
		t.Errorf("branch %s path %s", rec.Delegation.Branch, f.wt.reqs[0].Path)
	}
	if rec.Delegation.Implementer.Agent != task.Claude {
		t.Errorf("agent not switched: %v", rec.Delegation.Implementer)
	}
}

func TestRunTaskAppliesOutcome(t *testing.T) {
	question := &task.Question{Issue: "Retry 4xx?", Options: []task.Option{{Label: "A"}, {Label: "B"}}, Recommendation: "A"}
	cases := []struct {
		name  string
		turn  agent.Turn
		err   error
		state task.State
		check func(t *testing.T, r *task.Record)
	}{
		{"pr opened", agent.Turn{Outcome: agent.Outcome{Status: agent.PROpened, Summary: "done", PRNumber: 912, Validation: []task.Check{{Command: "go test ./...", Passed: true}}}}, nil, task.WaitingForCI,
			func(t *testing.T, r *task.Record) {
				if r.Delegation.PR != 912 || r.Delegation.PRURL != "https://github.com/testorg/example/pull/912" || len(r.Validation) != 1 {
					t.Errorf("pr = %d %q validation %v", r.Delegation.PR, r.Delegation.PRURL, r.Validation)
				}
			}},
		{"blocked", agent.Turn{Outcome: agent.Outcome{Status: agent.Blocked, Summary: "need input", Questions: []task.Question{*question}}}, nil, task.NeedsDecision,
			func(t *testing.T, r *task.Record) {
				if len(r.Decisions) != 1 || r.Decisions[0].Question.Issue != "Retry 4xx?" || r.Decisions[0].Answer != "" {
					t.Errorf("decisions = %+v", r.Decisions)
				}
			}},
		{"agent failed", agent.Turn{Outcome: agent.Outcome{Status: agent.FailedRun, Summary: "tests cannot run"}}, nil, task.Failed, nil},
		{"crash", agent.Turn{}, errors.New("claude exited: signal: killed"), task.Failed,
			func(t *testing.T, r *task.Record) {
				if !strings.Contains(r.LastOutcome.Summary, "killed") {
					t.Errorf("outcome = %+v", r.LastOutcome)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture()
			rec, err := f.app.Start(context.Background(), startReq(t))
			if err != nil {
				t.Fatal(err)
			}
			c.turn.Session = task.Session{Agent: task.Claude, ID: "sid-9"}
			f.claude.turn, f.claude.err = c.turn, c.err
			if err := f.run(context.Background(), rec.Key, 4242, io.Discard); err != nil {
				t.Fatal(err)
			}
			got := f.store.recs[rec.Key]
			if got.State != c.state || got.Delegation.RunnerPID != 0 || got.Delegation.Implementer.ID != "sid-9" {
				t.Errorf("state %s pid %d session %+v", got.State, got.Delegation.RunnerPID, got.Delegation.Implementer)
			}
			if !strings.Contains(f.claude.prompt, "5xx-only retry semantics") || !strings.Contains(f.claude.prompt, "Retry failed requests.") {
				t.Error("prompt lacks issue body or context file")
			}
			if c.check != nil {
				c.check(t, got)
			}
		})
	}
}

func TestRunTaskRefusesNonWorkingTask(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: "testorg/example", Number: 825}
	f.store.recs[key] = &task.Record{Key: key, State: task.Discovered}
	if err := f.run(context.Background(), key, 1, io.Discard); err == nil {
		t.Fatal("ran a DISCOVERED task")
	}
}

func TestStatusReapsDeadRunner(t *testing.T) {
	f := newFixture()
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	recs, _, err := f.app.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].State != task.Failed || !strings.Contains(recs[0].LastOutcome.Summary, "pid 4242") {
		t.Fatalf("got %+v", recs[0])
	}
	if f.store.recs[rec.Key].Delegation.RunnerPID != 0 {
		t.Error("pid not cleared")
	}

	f.runners.alive[4242] = true
	f.store.recs[rec.Key].State, f.store.recs[rec.Key].Delegation.RunnerPID = task.Implementing, 4242
	if recs, _, _ = f.app.Status(context.Background()); recs[0].State != task.Implementing {
		t.Errorf("live runner reaped: %s", recs[0].State)
	}
}

func TestRunTaskVerifiesReportedPR(t *testing.T) {
	good := func() PullRequest {
		return PullRequest{Key: task.Key{Repo: "testorg/example", Number: 913}, URL: "u913", State: "open", HeadRefName: "feat/825-provider-retries", BaseRefName: "main", Body: "Retry 5xx only."}
	}
	cases := []struct {
		name     string
		reported int
		pr       func() PullRequest
		want     task.State
		wantPR   int
		errPart  string
	}{
		{"number verified", 913, good, task.WaitingForCI, 913, ""},
		{"no number, found by branch", 0, good, task.WaitingForCI, 913, ""},
		{"unknown number", 404, good, task.Failed, 0, "could not be loaded"},
		{"no PR for branch", 0, func() PullRequest { p := good(); p.HeadRefName = "other"; return p }, task.Failed, 0, "no open PR has head"},
		{"closed", 913, func() PullRequest { p := good(); p.State = "closed"; return p }, task.Failed, 0, "not open"},
		{"wrong branch", 913, func() PullRequest { p := good(); p.HeadRefName = "feature/x"; return p }, task.Failed, 0, "not the task branch"},
		{"wrong base", 913, func() PullRequest { p := good(); p.BaseRefName = "develop"; return p }, task.Failed, 0, "targets develop"},
		{"fork", 913, func() PullRequest { p := good(); p.IsCrossRepository = true; return p }, task.Failed, 0, "fork"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture()
			f.gh.prs = map[int]PullRequest{913: c.pr()}
			rec, err := f.app.Start(context.Background(), startReq(t))
			if err != nil {
				t.Fatal(err)
			}
			f.claude.turn = agent.Turn{Session: task.Session{Agent: task.Claude, ID: "s"}, Outcome: agent.Outcome{
				Status: agent.PROpened, Summary: "opened", PRNumber: c.reported, Validation: []task.Check{{Command: "go test ./...", Passed: true}},
			}}
			if err := f.run(context.Background(), rec.Key, 4242, io.Discard); err != nil {
				t.Fatal(err)
			}
			got := f.store.recs[rec.Key]
			if got.State != c.want || got.Delegation.PR != c.wantPR {
				t.Errorf("state %s pr %d; want %s %d (%v)", got.State, got.Delegation.PR, c.want, c.wantPR, got.LastOutcome)
			}
			if c.errPart != "" && !strings.Contains(got.LastOutcome.Summary, c.errPart) {
				t.Errorf("summary %q lacks %q", got.LastOutcome.Summary, c.errPart)
			}
		})
	}
}

func TestStartPicksBranchTypeFromLabelsOrOverride(t *testing.T) {
	for _, tc := range []struct {
		override task.BranchType
		want     string
	}{{"", "fix/825-provider-retries"}, {"chore", "chore/825-provider-retries"}} {
		f := newFixture()
		f.gh.issue.Labels = []string{"bug", "triaged"}
		req := startReq(t)
		req.BranchType = tc.override
		rec, err := f.app.Start(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Delegation.Branch != tc.want || f.wt.reqs[0].Path != "/wt/testorg-example/"+tc.want {
			t.Errorf("override %q: branch %s path %s", tc.override, rec.Delegation.Branch, f.wt.reqs[0].Path)
		}
	}
}

func TestLinearStartUsesTrackerBranchName(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: f.gh.repo.Repo, Source: task.Linear, ExternalID: "uuid-42"}
	f.app.Linear = &lifecycleTracker{issue: Issue{Key: key, Identifier: "WEB-42", BranchName: "bryan/web-42-fix-cache", Title: "Fix cache", State: "open", Labels: []string{"triaged"}}}
	rec, err := f.app.Start(context.Background(), StartRequest{Repo: key.Repo, Key: key, Agent: task.Claude})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Delegation.Branch != "bryan/web-42-fix-cache" || f.wt.reqs[0].Branch != rec.Delegation.Branch || f.wt.reqs[0].Path != "/wt/testorg-example/bryan/web-42-fix-cache" {
		t.Fatalf("branch = %q, worktree = %+v", rec.Delegation.Branch, f.wt.reqs[0])
	}
}

func TestLinearStartRejectsUnsafeTrackerBranchName(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: f.gh.repo.Repo, Source: task.Linear, ExternalID: "uuid-42"}
	f.app.Linear = &lifecycleTracker{issue: Issue{Key: key, Identifier: "WEB-42", BranchName: "../outside", Title: "Fix cache", State: "open", Labels: []string{"triaged"}}}
	if _, err := f.app.Start(context.Background(), StartRequest{Repo: key.Repo, Key: key, Agent: task.Claude}); err == nil || !strings.Contains(err.Error(), "invalid branch name") {
		t.Fatalf("start error = %v", err)
	}
	if len(f.wt.reqs) != 0 {
		t.Fatalf("worktree was created: %+v", f.wt.reqs)
	}
}

func TestStartNewOnlyRefusesAnIssueAlreadyDelegated(t *testing.T) {
	// The dashboard's inbox can be a refresh old: another command may have started the
	// issue, and it may already be PR_CLOSED, which the dashboard's task list leaves out.
	f := newFixture()
	key := task.Key{Repo: "testorg/example", Number: 825}
	f.store.recs[key] = &task.Record{Key: key, Kind: task.Issue, State: task.PRClosed,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Branch: "feat/825-provider-retries"}}
	_, err := f.app.Start(context.Background(), StartRequest{Repo: key.Repo, Issue: 825, Agent: task.Claude, NewOnly: true})
	if !errors.Is(err, ErrDelegated) {
		t.Fatalf("start = %v; want ErrDelegated", err)
	}
	if len(f.runners.spawned) != 0 || len(f.wt.reqs) != 0 {
		t.Errorf("a refused start spawned %d runners and made %d worktrees", len(f.runners.spawned), len(f.wt.reqs))
	}
}

func TestStartNewOnlyStartsAnIssueNeverDelegated(t *testing.T) {
	f := newFixture()
	f.gh.issue.Labels = []string{"triaged"}
	key := task.Key{Repo: "testorg/example", Number: 825}
	f.store.recs[key] = &task.Record{Key: key, Kind: task.Issue, State: task.Discovered}
	rec, err := f.app.Start(context.Background(), StartRequest{Repo: key.Repo, Issue: 825, Agent: task.Claude, NewOnly: true})
	if err != nil || rec.State != task.Planning || len(f.runners.spawned) != 1 {
		t.Fatalf("start = %+v, %v; want a PLANNING run for a tracked issue with no delegation", rec, err)
	}
}

func TestStartNewOnlyRejectsIssueWhoseTriagedLabelWasRemoved(t *testing.T) {
	f := newFixture()
	f.gh.issue.Labels = nil
	key := task.Key{Repo: "testorg/example", Number: 825}
	_, err := f.app.Start(context.Background(), StartRequest{Repo: key.Repo, Issue: key.Number, Agent: task.Claude, NewOnly: true})
	if err == nil || !strings.Contains(err.Error(), "triaged") {
		t.Fatalf("auto start = %v; want missing triaged label error", err)
	}
	if len(f.runners.spawned) != 0 || len(f.wt.reqs) != 0 {
		t.Fatalf("auto start spawned %d runners and made %d worktrees", len(f.runners.spawned), len(f.wt.reqs))
	}
	if _, err := f.app.Start(context.Background(), StartRequest{Repo: key.Repo, Issue: key.Number, Agent: task.Claude}); err == nil || !strings.Contains(err.Error(), "triaged") {
		t.Fatalf("manual start without triaged label = %v", err)
	}
}
