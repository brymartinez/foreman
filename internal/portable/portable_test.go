package portable

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/db"
	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/localissues"
	"github.com/brymartinez/foreman/internal/store"
	"github.com/brymartinez/foreman/internal/task"
)

var (
	ctx = context.Background()
	t0  = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
)

func clock() time.Time { return t0.Add(time.Hour) }

func newService(t *testing.T) Service {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "foreman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return Service{DB: d, Now: clock}
}

const (
	secretSession = "sess-claude-SECRET-1234"
	secretPath    = "/Users/dana/.local/share/foreman/worktrees/acme-web/feat/x"
	secretLog     = "/Users/dana/.local/share/foreman/logs/acme-web-local-L.log"
	secretEmail   = "42+dana@users.noreply.github.com"
)

// seed builds a database with one issue awaiting triage, one with a finished run's
// durable records, and learnings in every status.
func seed(t *testing.T, s Service) (awaiting, worked localissues.Issue) {
	t.Helper()
	issues := localissues.Store{DB: s.DB, Now: clock}
	var err error
	awaiting, err = issues.Create(ctx, localissues.Input{Title: "Awaiting one", Description: "Not triaged yet.", SourceURL: "https://linear.app/acme/issue/WEB-1"})
	if err != nil {
		t.Fatal(err)
	}
	worked, err = issues.Create(ctx, localissues.Input{Title: "Fix cache", Description: "Use bounded retries.", SourceURL: "https://jira.example/browse/WEB-42", Repo: "acme/web", Notes: "private: ask Dana", Tags: []string{"backend", "urgent"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := (store.SQLiteTriage{DB: s.DB}).Save(task.Key{Source: task.Local, ExternalID: worked.ID}, "Keep the cache API.\nRetry 3 times."); err != nil {
		t.Fatal(err)
	}
	rec := &task.Record{
		Key: localissues.Key(worked.ID, "acme/web"), Identifier: worked.ID, Kind: task.Issue, Title: worked.Title, URL: worked.SourceURL, State: task.WaitingForCI,
		Body: "# Fix cache\n\n## Notes\n\nmy notes\n",
		Decisions: []task.Decision{{AskedAt: t0, AnsweredAt: t0.Add(time.Minute), Answer: "A",
			Question: task.Question{Issue: "Which cache?", Options: []task.Option{{Label: "A", Description: "LRU"}, {Label: "B", Description: "TTL"}}, Recommendation: "A"}}},
		Reviews: []task.Review{
			{Kind: task.SelfReview, Round: 1, Reviewer: task.Claude, Summary: "one finding", Findings: []task.Finding{{Severity: "blocking", File: "cache.go", Line: 12, Summary: "leak", Detail: "close the body"}}, Head: "abc1234", At: t0},
			{Round: 1, Reviewer: task.Codex, Clean: true, Summary: "clean", Head: "def5678", At: t0.Add(time.Hour)},
		},
		Delegation: &task.Delegation{
			Implementer: task.Session{Agent: task.Claude, ID: secretSession}, Planner: task.Session{Agent: task.Claude, ID: secretSession + "-plan"},
			Account: "dana", AccountEmail: secretEmail, Branch: "feat/local-cache", Worktree: secretPath, RepoDir: "/Users/dana/repos/web", RunnerLog: secretLog, RunnerPID: 4242,
			LocalDocuments: true, LocalPlan: &task.LocalPlan{Fingerprint: task.DescriptionFingerprint(worked.Description), Documents: map[task.PlanDoc]string{task.PRD: "R1. Retries are bounded.", task.ImplementationPlan: "1. Add the retry loop (R1)."}},
			CheckpointHead: "abc1234", CheckpointNext: "write tests", Checkpoints: 2, PR: 912, PRURL: "https://github.com/acme/web/pull/912", PRHead: "def5678", LocalHead: "def5678",
		},
	}
	if err := (store.SQLite{DB: s.DB}).Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	kn := knowledge.SQLStore{DB: s.DB}
	mk := func(task string, d int, p ...knowledge.Proposal) knowledge.Evaluation {
		at := t0.Add(time.Duration(d) * 24 * time.Hour)
		return knowledge.Evaluation{Task: task, TaskAt: at, At: at, Fingerprint: "fp" + task, Assessment: "raw judgment", Proposals: p, MaintainerRefs: []string{"D1"}}
	}
	maint := knowledge.Proposal{Kind: knowledge.Convention, Text: "Wrap errors with the operation name.", Scope: knowledge.Reusable, Source: knowledge.Maintainer, Cites: []string{"D1"}}
	obs := knowledge.Proposal{Kind: knowledge.Fact, Text: "Config lives in config.yaml.", Scope: knowledge.Reusable, Source: knowledge.Observed, Why: "seen"}
	for _, ev := range []knowledge.Evaluation{mk("1", 0, maint, obs), mk("2", 1, knowledge.Proposal{Kind: knowledge.Fact, Text: "Tests run with go test.", Scope: knowledge.Reusable, Source: knowledge.Observed, Why: "seen"})} {
		if err := kn.Apply("acme/web", ev); err != nil {
			t.Fatal(err)
		}
	}
	return awaiting, worked
}

func exportYAML(t *testing.T, s Service, opts ExportOptions) (File, string) {
	t.Helper()
	f, err := s.Export(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	f.ExportedAt = time.Time{}
	for i := range f.Learnings {
		f.Learnings[i].ExportedAt = time.Time{}
	}
	raw, err := Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return f, string(raw)
}

func TestExportOmitsCredentialsSessionsLogsRunnerStateAndMachinePaths(t *testing.T) {
	s := newService(t)
	_, worked := seed(t, s)
	_, text := exportYAML(t, s, ExportOptions{Learnings: true})
	for _, secret := range []string{secretSession, secretPath, secretLog, secretEmail, "/Users/", "4242", "RunnerPID", "session", "raw judgment", "worktree"} {
		if strings.Contains(text, secret) {
			t.Errorf("the export contains %q", secret)
		}
	}
	for _, want := range []string{worked.ID, "Keep the cache API.", "private: ask Dana", "backend", "R1. Retries are bounded.", "Add the retry loop", "Which cache?", "close the body", "feat/local-cache", "abc1234", "pull/912", "https://jira.example/browse/WEB-42", "write tests"} {
		if !strings.Contains(text, want) {
			t.Errorf("the export lacks %q", want)
		}
	}
}

func TestExportThenImportIntoAFreshDatabaseRoundTripsWorkAndLearnings(t *testing.T) {
	src := newService(t)
	seed(t, src)
	_, want := exportYAML(t, src, ExportOptions{Learnings: true})
	file, _ := src.Export(ctx, ExportOptions{Learnings: true})
	data, err := Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	dst := newService(t)
	plan, err := dst.Apply(ctx, parsed, Fail)
	if err != nil || !plan.Applied || len(plan.Create) != 3 || len(plan.Conflicts) != 0 {
		t.Fatalf("import = %+v, %v", plan, err)
	}
	_, got := exportYAML(t, dst, ExportOptions{Learnings: true})
	if got != want {
		t.Fatalf("round trip changed the work\n--- want\n%s\n--- got\n%s", want, got)
	}
}

func TestImportingTheSameFileTwiceChangesNothing(t *testing.T) {
	src := newService(t)
	seed(t, src)
	file, _ := src.Export(ctx, ExportOptions{Learnings: true})
	dst := newService(t)
	if _, err := dst.Apply(ctx, file, Fail); err != nil {
		t.Fatal(err)
	}
	_, before := exportYAML(t, dst, ExportOptions{Learnings: true})
	plan, err := dst.Apply(ctx, file, Fail)
	if err != nil || len(plan.Create) != 0 || len(plan.Unchanged) != 3 || len(plan.Conflicts) != 0 {
		t.Fatalf("second import = %+v, %v", plan, err)
	}
	_, after := exportYAML(t, dst, ExportOptions{Learnings: true})
	if after != before {
		t.Fatal("a repeated import changed the database")
	}
	var n int
	dst.DB.QueryRow(`SELECT COUNT(*) FROM local_issues`).Scan(&n)
	if n != 2 {
		t.Fatalf("issues after two imports = %d, want 2 (no duplicates)", n)
	}
}

func TestOneIssueExportCarriesNoLearnings(t *testing.T) {
	s := newService(t)
	_, worked := seed(t, s)
	f, err := s.Export(ctx, ExportOptions{IssueID: worked.ID})
	if err != nil || len(f.Issues) != 1 || f.Issues[0].ID != worked.ID || len(f.Learnings) != 0 {
		t.Fatalf("export = %+v, %v", f, err)
	}
	if _, err := s.Export(ctx, ExportOptions{IssueID: worked.ID, Learnings: true}); err == nil {
		t.Fatal("a one-issue export took learnings")
	}
	if _, err := s.Export(ctx, ExportOptions{IssueID: "L-DEADBEEF"}); !errors.Is(err, localissues.ErrNotFound) {
		t.Fatalf("missing issue = %v", err)
	}
	// Learnings are opt in.
	if f, _ := s.Export(ctx, ExportOptions{}); len(f.Learnings) != 0 {
		t.Fatal("learnings were exported without opting in")
	}
}

func TestLearningsKeepStatusAndHistoryButNotPendingProposalsOrEvaluations(t *testing.T) {
	s := newService(t)
	seed(t, s)
	kn := knowledge.SQLStore{DB: s.DB}
	// Retire the maintainer-backed learning so the export has a retired entry.
	ev := knowledge.Evaluation{Task: "3", TaskAt: t0.Add(72 * time.Hour), At: t0.Add(73 * time.Hour), Fingerprint: "fp3", Assessment: "x", MaintainerRefs: []string{"D1"},
		Revisions: []knowledge.Revision{{ID: "L1", Action: knowledge.Retire, Reason: "no longer true", Source: knowledge.Maintainer, Cites: []string{"D1"}}}}
	if err := kn.Apply("acme/web", ev); err != nil {
		t.Fatal(err)
	}
	f, err := s.Export(ctx, ExportOptions{Learnings: true})
	if err != nil || len(f.Learnings) != 1 {
		t.Fatalf("export = %+v, %v", f, err)
	}
	snap := f.Learnings[0]
	if snap.Repo != "acme/web" || len(snap.Evaluations) != 0 {
		t.Fatalf("snapshot = %+v", snap)
	}
	statuses := map[string]knowledge.Status{}
	for _, l := range snap.Learnings {
		statuses[l.ID] = l.Status
		if l.Status == knowledge.Candidate {
			t.Errorf("pending proposal %s was exported", l.ID)
		}
	}
	if statuses["L1"] != knowledge.Retired || len(snap.Learnings[0].History) == 0 {
		t.Fatalf("the retired learning and its history were not kept: %+v", snap.Learnings)
	}
	dst := newService(t)
	if _, err := dst.Apply(ctx, f, Fail); err != nil {
		t.Fatal(err)
	}
	ls, _ := (knowledge.SQLStore{DB: dst.DB}).Learnings("acme/web")
	if len(ls) != len(snap.Learnings) || ls[0].Status != knowledge.Retired {
		t.Fatalf("imported learnings = %+v", ls)
	}
	if evs, _ := (knowledge.SQLStore{DB: dst.DB}).Evaluations("acme/web", 0); len(evs) != 0 {
		t.Fatal("import brought raw evaluations")
	}
}

func TestConflictsAreShownFirstAndStopTheImportBeforeAnyChange(t *testing.T) {
	src := newService(t)
	_, worked := seed(t, src)
	file, _ := src.Export(ctx, ExportOptions{})
	dst := newService(t)
	if _, err := dst.Apply(ctx, file, Fail); err != nil {
		t.Fatal(err)
	}
	// The destination edits the issue, and the file also carries a brand new one.
	if _, err := dst.DB.Exec(`UPDATE local_issues SET title = 'Edited here' WHERE id = ?`, worked.ID); err != nil {
		t.Fatal(err)
	}
	extra := file
	extra.Issues = append(append([]Issue{}, file.Issues...), Issue{ID: "L-0000AAAA", Title: "New one", Status: "open", CreatedAt: t0, UpdatedAt: t0})
	preview, err := dst.Preview(ctx, extra)
	if err != nil || len(preview.Conflicts) != 1 || preview.Conflicts[0].ID != worked.ID || !strings.Contains(preview.Conflicts[0].Detail, "title") || len(preview.Create) != 1 || preview.Applied {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	if _, err := dst.Apply(ctx, extra, Fail); !errors.Is(err, ErrConflicts) {
		t.Fatalf("apply = %v, want ErrConflicts", err)
	}
	var n int
	dst.DB.QueryRow(`SELECT COUNT(*) FROM local_issues WHERE id = 'L-0000AAAA'`).Scan(&n)
	if n != 0 {
		t.Fatal("a refused import still saved its new records")
	}
	// Skip keeps the local version of the conflict and adds the rest.
	if _, err := dst.Apply(ctx, extra, Skip); err != nil {
		t.Fatal(err)
	}
	if i, _ := (localissues.Store{DB: dst.DB}).Get(ctx, worked.ID); i.Title != "Edited here" {
		t.Fatalf("skip overwrote the local version: %+v", i)
	}
	dst.DB.QueryRow(`SELECT COUNT(*) FROM local_issues WHERE id = 'L-0000AAAA'`).Scan(&n)
	if n != 1 {
		t.Fatal("skip did not add the new record")
	}
	// Replace takes the file's version.
	if _, err := dst.Apply(ctx, extra, Replace); err != nil {
		t.Fatal(err)
	}
	if i, _ := (localissues.Store{DB: dst.DB}).Get(ctx, worked.ID); i.Title != "Fix cache" {
		t.Fatalf("replace kept the local version: %+v", i)
	}
}

func TestAFailureMidImportLeavesTheDatabaseUnchanged(t *testing.T) {
	src := newService(t)
	seed(t, src)
	file, _ := src.Export(ctx, ExportOptions{})
	dst := newService(t)
	// The second issue's work points at a task row the first write makes impossible.
	if _, err := dst.DB.Exec(`CREATE TRIGGER refuse BEFORE INSERT ON tasks BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.Apply(ctx, file, Fail); err == nil {
		t.Fatal("import succeeded despite a failing write")
	}
	var n int
	dst.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM local_issues) + (SELECT COUNT(*) FROM triage) + (SELECT COUNT(*) FROM tasks)`).Scan(&n)
	if n != 0 {
		t.Fatalf("a failed import left %d rows behind", n)
	}
}

func TestImportRestoresRecordsWithoutStartingAnything(t *testing.T) {
	src := newService(t)
	_, worked := seed(t, src)
	file, _ := src.Export(ctx, ExportOptions{})
	dst := newService(t)
	if _, err := dst.Apply(ctx, file, Fail); err != nil {
		t.Fatal(err)
	}
	rec, err := (store.SQLite{DB: dst.DB}).Get(ctx, localissues.Key(worked.ID, "acme/web"))
	if err != nil || rec == nil {
		t.Fatalf("task = %+v, %v", rec, err)
	}
	if rec.Delegation != nil {
		t.Fatalf("an import created a delegation: %+v", rec.Delegation)
	}
	if rec.State.Active() || rec.Reset == nil || rec.Reset.PR != 912 || rec.Reset.Branch != "feat/local-cache" {
		t.Fatalf("restored state %s, reset %+v; want an idle record that can reconnect to its PR", rec.State, rec.Reset)
	}
	if rec.Imported == nil || rec.Imported.Documents[task.PRD] == "" || rec.Imported.CheckpointHead != "abc1234" {
		t.Fatalf("imported work = %+v", rec.Imported)
	}
	if len(rec.Decisions) != 1 || len(rec.Reviews) != 2 || !strings.Contains(rec.Body, "my notes") {
		t.Fatalf("decisions %d, reviews %d, body %q", len(rec.Decisions), len(rec.Reviews), rec.Body)
	}
	triage, _ := (store.SQLiteTriage{DB: dst.DB}).Read(task.Key{Source: task.Local, ExternalID: worked.ID})
	if !strings.Contains(triage, "Retry 3 times.") {
		t.Fatalf("triage = %q", triage)
	}
}

func TestParseRejectsInvalidFilesBeforeAnythingIsRead(t *testing.T) {
	src := newService(t)
	seed(t, src)
	file, _ := src.Export(ctx, ExportOptions{Learnings: true})
	good, _ := Marshal(file)
	cases := map[string]string{
		"empty":           "",
		"wrong format":    strings.Replace(string(good), Format, "foreman-work/9", 1),
		"unknown field":   strings.Replace(string(good), "issues:", "surprise: true\nissues:", 1),
		"bad id":          strings.Replace(string(good), file.Issues[0].ID, "L-nope", 1),
		"bad status":      strings.Replace(string(good), "status: open", "status: maybe", 1),
		"two documents":   string(good) + "---\nformat: x\n",
		"not yaml":        "{{{",
		"file source URL": strings.Replace(string(good), "https://jira.example/browse/WEB-42", "file:///etc/passwd", 1),
		"bad commit":      strings.Replace(string(good), "abc1234", "not-a-sha!", 1),
		"bad branch":      strings.Replace(string(good), "feat/local-cache", "../outside", 1),
	}
	for name, body := range cases {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s: Parse accepted it", name)
		}
	}
	if _, err := Parse(good); err != nil {
		t.Fatalf("the unmodified export no longer parses: %v", err)
	}
}

func TestParseRejectsPendingLearningsAndEvaluations(t *testing.T) {
	f := File{Format: Format, ExportedAt: t0, Issues: []Issue{}, Learnings: []knowledge.Snapshot{{Format: knowledge.FormatVersion, Repo: "acme/web", NextID: 1,
		Learnings: []knowledge.Learning{{ID: "L1", Kind: knowledge.Fact, Text: "A fact.", Status: knowledge.Candidate}}, Evaluations: []knowledge.Evaluation{}}}}
	data, _ := Marshal(f)
	if _, err := Parse(data); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("a pending proposal parsed: %v", err)
	}
	f.Learnings[0].Learnings[0].Status = knowledge.Active
	f.Learnings[0].Evaluations = []knowledge.Evaluation{{Task: "1", TaskAt: t0, At: t0}}
	data, _ = Marshal(f)
	if _, err := Parse(data); err == nil || !strings.Contains(err.Error(), "evaluations") {
		t.Fatalf("raw evaluations parsed: %v", err)
	}
}

func TestReplacingWorkWithALiveAgentIsRefused(t *testing.T) {
	src := newService(t)
	_, worked := seed(t, src)
	file, _ := src.Export(ctx, ExportOptions{})
	dst := newService(t)
	if _, err := dst.Apply(ctx, file, Fail); err != nil {
		t.Fatal(err)
	}
	st := store.SQLite{DB: dst.DB}
	key := localissues.Key(worked.ID, "acme/web")
	if _, err := st.Update(ctx, key, func(cur *task.Record) (*task.Record, error) {
		cur.Delegation = &task.Delegation{Implementer: task.Session{Agent: task.Claude}, RunnerPID: testPID()}
		cur.Title = "changed"
		return cur, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.DB.Exec(`UPDATE local_issues SET title = 'changed' WHERE id = ?`, worked.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.Apply(ctx, file, Replace); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("replace over a live agent = %v", err)
	}
}

func TestReplaceWithoutWorkDropsTheOldBranchPRAndCheckpoints(t *testing.T) {
	src := newService(t)
	_, worked := seed(t, src)
	file, _ := src.Export(ctx, ExportOptions{})
	dst := newService(t)
	if _, err := dst.Apply(ctx, file, Fail); err != nil {
		t.Fatal(err)
	}
	// The incoming version of the issue carries no work, and differs, so it conflicts.
	incoming := file
	incoming.Issues = nil
	for _, i := range file.Issues {
		if i.ID == worked.ID {
			i.Work = nil
			i.Title = "Fix cache, rescoped"
		}
		incoming.Issues = append(incoming.Issues, i)
	}
	if _, err := dst.Apply(ctx, incoming, Replace); err != nil {
		t.Fatal(err)
	}
	var n int
	dst.DB.QueryRow(`SELECT COUNT(*) FROM tasks WHERE external_id = ?`, worked.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("replace left %d stale task rows", n)
	}
	out, err := dst.Export(ctx, ExportOptions{IssueID: worked.ID})
	if err != nil || len(out.Issues) != 1 || out.Issues[0].Work != nil || out.Issues[0].Title != "Fix cache, rescoped" {
		t.Fatalf("export after replace = %+v, %v", out.Issues, err)
	}
	// Skip never touches existing work.
	if _, err := dst.Apply(ctx, file, Skip); err != nil {
		t.Fatal(err)
	}
	dst.DB.QueryRow(`SELECT COUNT(*) FROM tasks WHERE external_id = ?`, worked.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("skip restored work: %d rows", n)
	}
}
