package app

import (
	"context"
	"errors"
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
	reqs      []WorktreeRequest
	removed   []WorktreeRemoval
	removeErr error
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
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removed = append(f.removed, r)
	return nil
}

func (f *fakeWorktrees) Ensure(_ context.Context, r WorktreeRequest) error {
	f.reqs = append(f.reqs, r)
	return nil
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
	alive   map[int]bool
}

func (f *fakeRunners) Spawn(k task.Key) (int, string, error) {
	f.spawned = append(f.spawned, k)
	return 4242, "/logs/" + string(k.Repo) + ".log", nil
}
func (f *fakeRunners) Alive(pid int) bool { return f.alive[pid] }

type fakeAgent struct {
	name      task.Agent
	turn      agent.Turn
	turns     []agent.Turn // consumed before turn, one per Start/Resume
	err       error
	prompt    string
	starts    int
	resumed   []string
	resumeErr error
	reviews   []agent.ReviewOutcome // consumed in order; clean once empty
	reviewErr error
	reviewed  []string
	during    func() // runs inside Start, while the turn is in progress
}

func (f *fakeAgent) next() (agent.Turn, error) {
	if len(f.turns) > 0 {
		t := f.turns[0]
		f.turns = f.turns[1:]
		return t, nil
	}
	return f.turn, f.err
}

func (f *fakeAgent) Review(_ context.Context, in agent.StartInput) (agent.ReviewOutcome, error) {
	f.reviewed = append(f.reviewed, in.Prompt)
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
	if f.resumeErr != nil {
		return agent.Turn{}, f.resumeErr
	}
	return f.next()
}

func (f *fakeAgent) Start(_ context.Context, in agent.StartInput, onSession func(task.Session)) (agent.Turn, error) {
	f.starts++
	if f.during != nil {
		f.during()
	}
	f.prompt = in.Prompt
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
}

func newFixture() fixture {
	f := fixture{
		store: &fakeStore{recs: map[task.Key]*task.Record{}},
		gh: &fakeGitHub{
			repo:  RepoInfo{Repo: "ridezoomo/armada-backend", DefaultBranch: "main"},
			issue: Issue{Title: "Provider retries", Body: "Retry failed requests.", URL: "https://x/825", State: "open", UpdatedAt: t0},
			prs: map[int]PullRequest{912: {
				Key: task.Key{Repo: "ridezoomo/armada-backend", Number: 912}, URL: "https://github.com/ridezoomo/armada-backend/pull/912",
				State: "open", HeadRefName: "feat/825-provider-retries", BaseRefName: "main", HeadSHA: "c0ffee1",
			}},
		},
		wt:      &fakeWorktrees{},
		runners: &fakeRunners{alive: map[int]bool{}},
		stages:  &fakeStages{},
		repos:   &fakeRepositories{info: RepoInfo{Repo: "ridezoomo/armada-backend", DefaultBranch: "main"}},
		claude:  &fakeAgent{name: task.Claude},
		codex:   &fakeAgent{name: task.Codex},
		notes:   &fakeNotifier{},
	}
	f.app = App{
		Notifier: f.notes,
		Store:    f.store, GitHub: f.gh, Worktrees: f.wt, Runners: f.runners, Stages: f.stages,
		Agents:       map[task.Agent]AgentAdapter{task.Claude: f.claude, task.Codex: f.codex},
		WorktreesDir: "/wt", Repositories: f.repos, RepositoriesDir: "/repos",
		Now: func() time.Time { return now },
	}
	return f
}

func TestStartFromInboxUsesRepositoryCheckoutInsteadOfCurrentDirectory(t *testing.T) {
	f := newFixture()
	rec, err := f.app.Start(context.Background(), StartRequest{Repo: "ridezoomo/armada-backend", Issue: 825, Agent: task.Claude})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.repos.calls) != 1 || f.repos.calls[0] != "ridezoomo/armada-backend" {
		t.Fatalf("repository checkout calls = %v", f.repos.calls)
	}
	if len(f.wt.reqs) != 1 || f.wt.reqs[0].RepoDir != "/repos/ridezoomo-armada-backend" {
		t.Fatalf("worktree source = %+v", f.wt.reqs)
	}
	if rec.Repo != "ridezoomo/armada-backend" {
		t.Fatalf("record repo = %s", rec.Repo)
	}
}

func startReq(t *testing.T) StartRequest {
	ctxFile := filepath.Join(t.TempDir(), "825.md")
	os.WriteFile(ctxFile, []byte("Preserve the existing 5xx-only retry semantics."), 0o644)
	return StartRequest{Issue: 825, Agent: task.Claude, ContextFile: ctxFile, Dir: "/src/armada-backend"}
}

func TestStartDispatches(t *testing.T) {
	f := newFixture()
	req := startReq(t)
	rec, err := f.app.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	wantWT := WorktreeRequest{
		RepoDir: "/src/armada-backend",
		Path:    "/wt/ridezoomo-armada-backend/825-provider-retries",
		Branch:  "feat/825-provider-retries",
		BaseRef: "origin/main",
	}
	if len(f.wt.reqs) != 1 || f.wt.reqs[0] != wantWT {
		t.Errorf("worktree = %+v", f.wt.reqs)
	}
	d := rec.Delegation
	if rec.State != task.Working || d.Implementer.Agent != task.Claude || d.Reviewer != task.Codex ||
		d.Branch != wantWT.Branch || d.Worktree != wantWT.Path || d.ContextFile != req.ContextFile ||
		d.RunnerPID != 4242 || d.RunnerLog == "" || rec.Title != "Provider retries" {
		t.Errorf("record = %+v / %+v", rec, d)
	}
	if len(f.runners.spawned) != 1 || !strings.HasPrefix(rec.Body, "# #825 Provider retries") {
		t.Errorf("spawned %v body %q", f.runners.spawned, rec.Body)
	}
}

func TestStartRefuses(t *testing.T) {
	cases := map[string]func(f *fixture, r *StartRequest){
		"other repo":      func(f *fixture, r *StartRequest) { r.Repo = "ridezoomo/sw-automations" },
		"closed issue":    func(f *fixture, r *StartRequest) { f.gh.issue.State = "closed" },
		"missing context": func(f *fixture, r *StartRequest) { r.ContextFile = "/nope/825.md" },
		"running": func(f *fixture, r *StartRequest) {
			rec := seen(825, task.Working, "open", t0)
			rec.Repo = "ridezoomo/armada-backend"
			rec.Key = task.Key{Repo: "ridezoomo/armada-backend", Number: 825}
			rec.Delegation = &task.Delegation{RunnerPID: 77}
			f.store.recs[rec.Key] = rec
			f.runners.alive[77] = true
		},
		"needs decision": func(f *fixture, r *StartRequest) {
			rec := seen(825, task.NeedsDecision, "open", t0)
			rec.Key = task.Key{Repo: "ridezoomo/armada-backend", Number: 825}
			f.store.recs[rec.Key] = rec
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

func TestRestartKeepsBranchWhenTitleChanged(t *testing.T) {
	f := newFixture()
	key := task.Key{Repo: "ridezoomo/armada-backend", Number: 825}
	f.store.recs[key] = &task.Record{Key: key, Kind: task.Issue, State: task.Failed,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Codex}, Branch: "agent/825-old-title"}}
	f.gh.issue.Title = "A completely new title"
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Delegation.Branch != "agent/825-old-title" || f.wt.reqs[0].Path != "/wt/ridezoomo-armada-backend/825-old-title" {
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
		{"pr opened", agent.Turn{Outcome: agent.Outcome{Status: agent.PROpened, Summary: "done", PRNumber: 912, Validation: []task.Check{{Command: "go test ./...", Passed: true}}}}, nil, task.Reviewing,
			func(t *testing.T, r *task.Record) {
				if r.Delegation.PR != 912 || r.Delegation.PRURL != "https://github.com/ridezoomo/armada-backend/pull/912" || len(r.Validation) != 1 {
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
			if err := f.app.RunTask(context.Background(), rec.Key, 4242, io.Discard); err != nil {
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
	key := task.Key{Repo: "ridezoomo/armada-backend", Number: 825}
	f.store.recs[key] = &task.Record{Key: key, State: task.Discovered}
	if err := f.app.RunTask(context.Background(), key, 1, io.Discard); err == nil {
		t.Fatal("ran a DISCOVERED task")
	}
}

func TestStatusReapsDeadRunner(t *testing.T) {
	f := newFixture()
	rec, err := f.app.Start(context.Background(), startReq(t))
	if err != nil {
		t.Fatal(err)
	}
	recs, err := f.app.Status(context.Background())
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
	f.store.recs[rec.Key].State, f.store.recs[rec.Key].Delegation.RunnerPID = task.Working, 4242
	if recs, _ = f.app.Status(context.Background()); recs[0].State != task.Working {
		t.Errorf("live runner reaped: %s", recs[0].State)
	}
}

func TestRunTaskVerifiesReportedPR(t *testing.T) {
	good := func() PullRequest {
		return PullRequest{Key: task.Key{Repo: "ridezoomo/armada-backend", Number: 913}, URL: "u913", State: "open", HeadRefName: "feat/825-provider-retries", BaseRefName: "main"}
	}
	cases := []struct {
		name     string
		reported int
		pr       func() PullRequest
		want     task.State
		wantPR   int
		errPart  string
	}{
		{"number verified", 913, good, task.Reviewing, 913, ""},
		{"no number, found by branch", 0, good, task.Reviewing, 913, ""},
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
			if err := f.app.RunTask(context.Background(), rec.Key, 4242, io.Discard); err != nil {
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
		f.gh.issue.Labels = []string{"bug"}
		req := startReq(t)
		req.BranchType = tc.override
		rec, err := f.app.Start(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Delegation.Branch != tc.want || f.wt.reqs[0].Path != "/wt/ridezoomo-armada-backend/825-provider-retries" {
			t.Errorf("override %q: branch %s path %s", tc.override, rec.Delegation.Branch, f.wt.reqs[0].Path)
		}
	}
}
