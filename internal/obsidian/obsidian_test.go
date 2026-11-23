package obsidian

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/db"
	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/localissues"
	"github.com/brymartinez/foreman/internal/store"
	"github.com/brymartinez/foreman/internal/task"
	"github.com/brymartinez/foreman/internal/workspace"
	"github.com/brymartinez/foreman/internal/workspace/backendtest"
)

func newBackend(t *testing.T) *Backend {
	t.Helper()
	return &Backend{Vault: t.TempDir(), Folder: "Foreman", Now: func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) }}
}

func rec(id, status string) workspace.Record {
	return workspace.Record{Format: workspace.Format, ID: id, Source: task.Local, Title: "T " + id, Status: status}
}

func writeNote(t *testing.T, b *Backend, name, content string) string {
	t.Helper()
	dir := filepath.Join(b.Vault, b.Folder, "Tasks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestObsidianBackendMeetsTheContract(t *testing.T) {
	backendtest.Run(t, func(t *testing.T) backendtest.Env {
		b := newBackend(t)
		ctx := context.Background()
		return backendtest.Env{
			Backend: b,
			UserCreate: func(e workspace.TaskEntry) string {
				var fm string
				if e.ID != "" {
					fm = "foreman_id: " + e.ID + "\n"
				}
				return writeNote(t, b, e.Name, "---\n"+fm+"---\n"+e.Description+"\n")
			},
			UserEdit: func(ref string, fn func(*workspace.TaskEntry)) {
				entries, err := b.ListTasks(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range entries {
					if e.Ref != ref {
						continue
					}
					fn(&e)
					if err := b.writeUserFields(ctx, ref, e); err != nil {
						t.Fatal(err)
					}
				}
			},
		}
	})
}

const userNote = `---
# my own comment
aliases: [Alpha]
repository: o/r
tags:
  - one
  - two
cssclasses: wide # trailing
scope_confirmed: false
---
Line one of the description.

> a quote the user wrote

## Confirmed triage

Do the thing.
`

func TestPublishKeepsTheUsersKeysCommentsAndText(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	path := writeNote(t, b, "Alpha", userNote)
	if err := b.AssignID(ctx, path, "L-0A1B2C3D"); err != nil {
		t.Fatal(err)
	}
	r := rec("L-0A1B2C3D", "PLANNING")
	w := workspaceWork
	r.Work = &w
	if _, err := b.UpsertTask(ctx, workspace.TaskPublish{Record: r}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	text := string(got)
	for _, want := range []string{"# my own comment", "aliases:", "cssclasses: wide # trailing", "- one", "- two", "Line one of the description.", "> a quote the user wrote", "## Confirmed triage", "Do the thing.",
		"foreman_id: L-0A1B2C3D", "foreman_status: Planning", "<!-- foreman:begin -->", "<!-- foreman:end -->", "format: foreman-task/1"} {
		if !strings.Contains(text, want) {
			t.Errorf("note lost or lacks %q:\n%s", want, text)
		}
	}
	entries, _ := b.ListTasks(ctx)
	e := entries[0]
	if e.Description != "Line one of the description.\n\n> a quote the user wrote" || e.ConfirmedTriage != "Do the thing." || e.Repository != "o/r" || len(e.Tags) != 2 || e.Name != "Alpha" {
		t.Errorf("entry = %+v", e)
	}
	if e.Record == nil || e.Record.Status != "PLANNING" {
		t.Errorf("record = %+v", e.Record)
	}
}

var workspaceWork = workspaceWorkValue()

func TestPublishingTwiceKeepsOneForemanRegion(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	b.UpsertTask(ctx, workspace.TaskPublish{Record: rec("L-0A1B2C3D", "PLANNING"), Create: workspace.TaskSeed{Name: "N", Description: "desc", ConfirmedTriage: "scope"}})
	ref, _ := b.UpsertTask(ctx, workspace.TaskPublish{Record: rec("L-0A1B2C3D", "IMPLEMENTING"), Reason: "no repository"})
	raw, _ := os.ReadFile(ref)
	if n := strings.Count(string(raw), "<!-- foreman:begin -->"); n != 1 {
		t.Fatalf("regions = %d\n%s", n, raw)
	}
	if !strings.Contains(string(raw), "no repository") || !strings.Contains(string(raw), "foreman_status: Implementing") {
		t.Fatalf("note:\n%s", raw)
	}
	entries, _ := b.ListTasks(ctx)
	if entries[0].Description != "desc" || entries[0].ConfirmedTriage != "scope" {
		t.Fatalf("user text changed: %+v", entries[0])
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(ref), ".*"))
	if len(left) != 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}
}

func TestAConcurrentEditIsRetriedAndNotLost(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	ref, _ := b.UpsertTask(ctx, workspace.TaskPublish{Record: rec("L-0A1B2C3D", "PLANNING"), Create: workspace.TaskSeed{Name: "N", Description: "desc"}})
	edited := false
	b.beforeCommit = func(path string) {
		if edited {
			return
		}
		edited = true
		raw, _ := os.ReadFile(path)
		os.WriteFile(path, append(raw, []byte("\nTyped while Foreman was writing.\n")...), 0o644)
	}
	if _, err := b.UpsertTask(ctx, workspace.TaskPublish{Record: rec("L-0A1B2C3D", "IMPLEMENTING")}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(ref)
	if !strings.Contains(string(raw), "Typed while Foreman was writing.") || !strings.Contains(string(raw), "foreman_status: Implementing") {
		t.Fatalf("the user's edit or Foreman's update was lost:\n%s", raw)
	}
}

func TestANoteWithoutFrontmatterKeepsItsTextWhenItsIDIsAssigned(t *testing.T) {
	b := newBackend(t)
	path := writeNote(t, b, "Plain", "Just text, no frontmatter.\n")
	if err := b.AssignID(context.Background(), path, "L-AAAAAAAA"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(raw), "---\n") || !strings.Contains(string(raw), "foreman_id: L-AAAAAAAA") || !strings.Contains(string(raw), "Just text, no frontmatter.") {
		t.Fatalf("note:\n%s", raw)
	}
}

func TestANoteWhoseFrontmatterDoesNotParseIsReportedAndNeverRewritten(t *testing.T) {
	b := newBackend(t)
	bad := "---\nkey: [unclosed\n---\nbody\n"
	path := writeNote(t, b, "Broken", bad)
	if err := b.AssignID(context.Background(), path, "L-AAAAAAAA"); err == nil {
		t.Fatal("must fail")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != bad {
		t.Fatalf("a broken note was rewritten:\n%s", raw)
	}
}

func TestKnowledgeNotesLiveUnderOwnerAndRepoInTheExportFormat(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	snap := knowledge.Snapshot{Format: knowledge.FormatVersion, Repo: "acme/widgets", NextID: 1, Learnings: []knowledge.Learning{}, Evaluations: []knowledge.Evaluation{}}
	if err := b.WriteKnowledge(ctx, workspace.KnowledgePublish{Repo: "acme/widgets", Snapshot: snap}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(b.Vault, "Foreman", "Repository Knowledge", "acme", "widgets.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := knowledge.Parse(raw); err != nil {
		t.Fatalf("the note is not in the export format: %v", err)
	}
	os.WriteFile(path, []byte("not a knowledge export"), 0o644)
	got, _ := b.ListKnowledge(ctx)
	if len(got) != 1 || got[0].ParseErr == nil || got[0].Snapshot != nil {
		t.Fatalf("a broken knowledge note must be reported, not adopted: %+v", got)
	}
}

func TestRepositoriesThatDifferOnlyByALeadingDotKeepSeparateKnowledgeNotes(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	for _, repo := range []string{"acme/.github", "acme/github", "acme/x."} {
		snap := knowledge.Snapshot{Format: knowledge.FormatVersion, Repo: repo, NextID: 1, Learnings: []knowledge.Learning{}, Evaluations: []knowledge.Evaluation{}}
		if err := b.WriteKnowledge(ctx, workspace.KnowledgePublish{Repo: repo, Snapshot: snap}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := b.ListKnowledge(ctx)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]string{}
	for _, e := range got {
		if e.Snapshot == nil {
			t.Fatalf("%s did not parse: %v", e.Repo, e.ParseErr)
		}
		have[e.Repo] = e.Snapshot.Repo
	}
	for _, repo := range []string{"acme/.github", "acme/github", "acme/x."} {
		if have[repo] != repo {
			t.Fatalf("read back %v, want every repository under its own name", have)
		}
	}
}

func TestCheckCreatesUpdatesReadsAndDeletesATestNote(t *testing.T) {
	b := newBackend(t)
	var steps []workspace.Step
	if err := b.Check(context.Background(), func(s workspace.Step) { steps = append(steps, s) }); err != nil {
		t.Fatal(err)
	}
	if len(steps) < 4 {
		t.Fatalf("steps = %+v", steps)
	}
	files, _ := filepath.Glob(filepath.Join(b.Vault, "Foreman", "Tasks", "*"))
	if len(files) != 0 {
		t.Fatalf("test note left behind: %v", files)
	}
}

func TestCheckFailsWhenTheVaultCannotBeWritten(t *testing.T) {
	b := newBackend(t)
	b.Vault = filepath.Join(b.Vault, "missing")
	var steps []workspace.Step
	if err := b.Check(context.Background(), func(s workspace.Step) { steps = append(steps, s) }); err == nil {
		t.Fatal("must fail")
	}
	if len(steps) == 0 || steps[len(steps)-1].Err == nil {
		t.Fatalf("steps = %+v", steps)
	}
}

func TestNoteNamesAreSafeFileNames(t *testing.T) {
	if got := safeName(`a/b:c*d?"e<f>g|h#i`); strings.ContainsAny(got, `/\:*?"<>|#`) || got == "" {
		t.Fatalf("name = %q", got)
	}
	if safeName("  ... ") != "Untitled" {
		t.Fatal("an empty name must become Untitled")
	}
}

func TestTwoGitHubTasksWithOneTitleBothGetANote(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	for _, id := range []string{"o/r#1", "o/r#2"} {
		r := rec(id, "IMPLEMENTING")
		r.Source = task.GitHub
		r.Title = "Same title"
		if _, err := b.UpsertTask(ctx, workspace.TaskPublish{Record: r, Create: workspace.TaskSeed{Name: "Same title"}}); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	es, err := b.ListTasks(ctx)
	if err != nil || len(es) != 2 {
		t.Fatalf("entries = %d, %v", len(es), err)
	}
	for _, e := range es {
		if e.Name != "Same title" {
			t.Errorf("%s reads back as %q: the file name suffix is not part of the title", e.ID, e.Name)
		}
	}
}

func TestATitleWithReservedCharactersReadsBackUnchanged(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	if _, err := b.UpsertTask(ctx, workspace.TaskPublish{Record: rec("L-0A1B2C3D", "INBOX"), Create: workspace.TaskSeed{Name: "Fix API: retry"}}); err != nil {
		t.Fatal(err)
	}
	es, _ := b.ListTasks(ctx)
	if len(es) != 1 || es[0].Name != "Fix API: retry" {
		t.Fatalf("name = %q: the sanitized file name came back as the title", es[0].Name)
	}
}

func TestARenamedNoteReadsBackUnderItsNewName(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	ref, err := b.UpsertTask(ctx, workspace.TaskPublish{Record: rec("L-0A1B2C3D", "INBOX"), Create: workspace.TaskSeed{Name: "Old name"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(ref, filepath.Join(filepath.Dir(ref), "New name.md")); err != nil {
		t.Fatal(err)
	}
	es, _ := b.ListTasks(ctx)
	if len(es) != 1 || es[0].Name != "New name" {
		t.Fatalf("name = %q, want the user's rename", es[0].Name)
	}
}

func TestANoteWithWindowsLineEndingsKeepsItsFrontmatter(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	path := writeNote(t, b, "Crlf", "---\r\nrepository: o/r\r\nqueue: true\r\nforeman_id: L-0A1B2C3D\r\n---\r\nBody line.\r\n")
	es, err := b.ListTasks(ctx)
	if err != nil || len(es) != 1 {
		t.Fatalf("entries = %d, %v", len(es), err)
	}
	if e := es[0]; e.ID != "L-0A1B2C3D" || e.Repository != "o/r" || !e.Queue {
		t.Fatalf("the frontmatter was not read: %+v", e)
	}
	if err := b.ClearQueue(ctx, path); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Count(string(raw), "---") != 2 || strings.Contains(strings.ReplaceAll(string(raw), "\r\n", ""), "\n") {
		t.Fatalf("the rewrite must keep one frontmatter block and CRLF endings: %q", raw)
	}
}

func TestReseedReplacesTheConfirmedTriageSectionAndKeepsTheDescription(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	pub := workspace.TaskPublish{Record: rec("L-0A1B2C3D", "INBOX"), Create: workspace.TaskSeed{Name: "N", Description: "keep me", ConfirmedTriage: "Scope A", ScopeConfirmed: true}}
	if _, err := b.UpsertTask(ctx, pub); err != nil {
		t.Fatal(err)
	}
	pub.Create.ConfirmedTriage = "Scope B"
	if _, err := b.UpsertTask(ctx, pub); err != nil {
		t.Fatal(err)
	}
	if es, _ := b.ListTasks(ctx); es[0].ConfirmedTriage != "Scope A" {
		t.Fatalf("an ordinary publish must not touch user text: %q", es[0].ConfirmedTriage)
	}
	pub.Reseed = true
	if _, err := b.UpsertTask(ctx, pub); err != nil {
		t.Fatal(err)
	}
	es, _ := b.ListTasks(ctx)
	if es[0].ConfirmedTriage != "Scope B" || es[0].Description != "keep me" || !es[0].ScopeConfirmed {
		t.Fatalf("entry = %+v", es[0])
	}
}

func TestAPlanQuotingBothMarkersRoundTripsAndUserMarkerTextSurvives(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	quoted := "Quote: <!-- foreman:begin --> and <!-- foreman:end --> in a plan."
	path := writeNote(t, b, "Alpha", "Mine, quoting <!-- foreman:begin --> and <!-- foreman:end --> here.\n")
	if err := b.AssignID(ctx, path, "L-0A1B2C3D"); err != nil {
		t.Fatal(err)
	}
	r := rec("L-0A1B2C3D", "PLANNING")
	w := workspaceWork
	w.Plan = quoted
	r.Work = &w
	for i := 0; i < 2; i++ {
		if _, err := b.UpsertTask(ctx, workspace.TaskPublish{Record: r}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := b.ListTasks(ctx)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %d, err = %v", len(entries), err)
	}
	e := entries[0]
	if e.ParseErr != nil || e.Record == nil || e.Record.Work == nil || e.Record.Work.Plan != quoted {
		t.Fatalf("record did not round-trip: err = %v, record = %+v", e.ParseErr, e.Record)
	}
	if want := "Mine, quoting <!-- foreman:begin --> and <!-- foreman:end --> here."; e.Description != want {
		t.Fatalf("description = %q, want %q", e.Description, want)
	}
}

func TestALongMultibyteTitleGetsAFileNameUnderTheByteLimit(t *testing.T) {
	b := newBackend(t)
	title := strings.Repeat("漢", 100)
	r := rec("L-0A1B2C3D", "PLANNING")
	r.Title = title
	path, err := b.UpsertTask(context.Background(), workspace.TaskPublish{Record: r, Create: workspace.TaskSeed{Name: title}})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(filepath.Base(path)); n > 255 {
		t.Fatalf("file name is %d bytes", n)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestAnExampleOfTheMarkersAndFormatLineAfterTheRegionIsNotTakenForTheRegion(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	path := writeNote(t, b, "Alpha", "Mine.\n")
	if err := b.AssignID(ctx, path, "L-0A1B2C3D"); err != nil {
		t.Fatal(err)
	}
	r := rec("L-0A1B2C3D", "PLANNING")
	if _, err := b.UpsertTask(ctx, workspace.TaskPublish{Record: r}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	example := "\nExample: <!-- foreman:begin -->\nformat: foreman-task/1\n<!-- foreman:end -->\n"
	if err := os.WriteFile(path, append(raw, example...), 0o644); err != nil {
		t.Fatal(err)
	}
	r.Status = "IMPLEMENTING"
	for i := 0; i < 2; i++ {
		if _, err := b.UpsertTask(ctx, workspace.TaskPublish{Record: r}); err != nil {
			t.Fatal(err)
		}
	}
	out, _ := os.ReadFile(path)
	if !strings.Contains(string(out), example) {
		t.Fatalf("the example was replaced:\n%s", out)
	}
	if n := strings.Count(string(out), "foreman-task/1\n<!-- foreman:end"); n != 1 {
		t.Fatalf("the example ended up in a record: %d\n%s", n, out)
	}
	entries, _ := b.ListTasks(ctx)
	if len(entries) != 1 || entries[0].Record == nil || entries[0].Record.Status != "IMPLEMENTING" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestANoteWrittenUnderItsUncappedTitleStillReadsBackUnderThatTitle(t *testing.T) {
	b := newBackend(t)
	title := "a/b " + strings.Repeat("漢", 60)
	writeNote(t, b, sanitizeName(title), "---\nforeman_id: L-0A1B2C3D\nforeman_title: \""+title+"\"\n---\nbody\n")
	entries, err := b.ListTasks(context.Background())
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %d, err = %v", len(entries), err)
	}
	if entries[0].Name != title {
		t.Fatalf("name = %q, want the title %q", entries[0].Name, title)
	}
}

func TestBrokenGeneratedPayloadDoesNotEnterIssueFields(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	host, err := db.Open(filepath.Join(t.TempDir(), "foreman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close() })
	sync := &workspace.Sync{DB: host, Backend: b, Tasks: store.SQLite{DB: host}, Issues: localissues.Store{DB: host}, Knowledge: knowledge.SQLStore{DB: host}}
	r := rec("L-0A1B2C3D", "INBOX")
	pub := workspace.TaskPublish{Record: r, Create: workspace.TaskSeed{Name: "N", Description: "My description.", ConfirmedTriage: "My scope.", ScopeConfirmed: true}}
	path, err := b.UpsertTask(ctx, pub)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(raw), "format: foreman-task/1", "format: broken", 1)
	if broken == string(raw) {
		t.Fatal("published note has no payload to damage")
	}
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := b.ListTasks(ctx)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %d, err = %v", len(entries), err)
	}
	e := entries[0]
	if e.ParseErr == nil || e.Record != nil {
		t.Errorf("broken payload should be reported, got error %v and record %v", e.ParseErr, e.Record)
	}
	if _, err := sync.Pull(ctx); err != nil {
		t.Fatal(err)
	}
	li, err := localissues.GetIn(host, r.ID)
	if err != nil || li.Description != pub.Create.Description {
		t.Errorf("host description = %q, err = %v", li.Description, err)
	}
	var triage string
	if err := host.QueryRow("SELECT decisions FROM triage").Scan(&triage); err != nil || triage != pub.Create.ConfirmedTriage {
		t.Errorf("host triage = %q, err = %v", triage, err)
	}
	if _, err := b.UpsertTask(ctx, pub); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(raw), beginMarker); count != 1 {
		t.Errorf("published note has %d generated regions, want 1", count)
	}
}

func TestAnUnclosedFenceInThePlanLeavesTheTaskRecordReadable(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	r := rec("L-0A1B2C3D", "PLANNING")
	w := workspaceWork
	w.Plan = "First step.\n\n```\nAn unfinished example."
	r.Work = &w
	if _, err := b.UpsertTask(ctx, workspace.TaskPublish{Record: r, Create: workspace.TaskSeed{Description: "User description."}}); err != nil {
		t.Fatal(err)
	}
	entries, err := b.ListTasks(ctx)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %d, err = %v", len(entries), err)
	}
	e := entries[0]
	if e.ParseErr != nil || e.Record == nil || e.Record.Work == nil || e.Record.Work.Plan != w.Plan {
		t.Errorf("saved plan did not round-trip, error = %v, record = %+v", e.ParseErr, e.Record)
	}
	if e.Description != "User description." || e.ConfirmedTriage != "" {
		t.Errorf("description = %q, triage = %q", e.Description, e.ConfirmedTriage)
	}
}

func TestAPastedTaskRecordAfterTheGeneratedRegionRemainsUserText(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	r := rec("L-0A1B2C3D", "PLANNING")
	pub := workspace.TaskPublish{Record: r, Create: workspace.TaskSeed{ConfirmedTriage: "My scope."}}
	path, err := b.UpsertTask(ctx, pub)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pasted := "Example report:\n" + beginMarker + "\n" + rec("L-BADC0FFE", "INBOX").Render() + endMarker
	if err := os.WriteFile(path, append(raw, ("\n"+pasted+"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	pub.Record.Status = "IMPLEMENTING"
	if _, err := b.UpsertTask(ctx, pub); err != nil {
		t.Fatal(err)
	}
	entries, err := b.ListTasks(ctx)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %d, err = %v", len(entries), err)
	}
	e := entries[0]
	if e.Record == nil || e.Record.ID != r.ID || e.Record.Status != pub.Record.Status {
		t.Errorf("wrong record after republishing: %+v", e.Record)
	}
	prefix, ok := strings.CutSuffix(e.ConfirmedTriage, pasted)
	if !ok || strings.TrimSpace(prefix) != "My scope." {
		t.Errorf("confirmed triage lost user text: %q", e.ConfirmedTriage)
	}
}

func TestAPublishUpgradesALegacyRegionFromACopiedNote(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	const id = "L-0A1B2C3D"
	const original = "L-BADC0FFE"
	r := rec(original, "PLANNING")
	path := writeNote(t, b, "Legacy", "---\nforeman_id: "+id+"\nforeman_status: Planning\n---\n"+
		"Old description.\n\n"+beginMarker+"\n"+r.Render()+endMarker+"\n\nAn extra note.\n")
	entries, err := b.ListTasks(ctx)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %d, err = %v", len(entries), err)
	}
	e := entries[0]
	if e.ParseErr != nil || e.ID != id || e.Record == nil || e.Record.ID != original {
		t.Fatalf("copy cannot be detected: id = %s, payload = %+v, err = %v", e.ID, e.Record, e.ParseErr)
	}
	r.ID, r.Status = id, "IMPLEMENTING"
	if _, err := b.UpsertTask(ctx, workspace.TaskPublish{Record: r}); err != nil {
		t.Fatal(err)
	}
	entries, err = b.ListTasks(ctx)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %d, err = %v", len(entries), err)
	}
	e = entries[0]
	if e.ParseErr != nil || e.ID != id || e.Record == nil || e.Record.ID != id || e.Record.Status != r.Status {
		t.Fatalf("legacy note was not migrated: id = %s, payload = %+v, err = %v", e.ID, e.Record, e.ParseErr)
	}
	if fields := strings.Fields(e.Description); strings.Join(fields, " ") != "Old description. An extra note." {
		t.Fatalf("description = %q", e.Description)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), beginMarker) != 1 || !strings.Contains(string(raw), "foreman_region:") {
		t.Fatalf("expected one upgraded region, note = %q", raw)
	}
}

func TestADamagedOrAmbiguousRegionCannotSupplyIssueFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		legacy bool
		damage func(string) string
	}{
		{"legacy opening missing", true, func(s string) string { return strings.Replace(s, beginMarker, "", 1) }},
		{"legacy status missing", true, func(s string) string { return strings.Replace(s, "Status: Planning", "", 1) }},
		{"owned end missing", false, func(s string) string { return strings.Replace(s, endMarker, "", 1) }},
		{"owned region ID missing", false, func(s string) string {
			lines := strings.Split(s, "\n")
			for i, line := range lines {
				if strings.HasPrefix(line, "foreman_region:") {
					return strings.Join(append(lines[:i], lines[i+1:]...), "\n")
				}
			}
			return s
		}},
		{"same region pasted twice", false, func(s string) string { return s + "\n" + s[strings.Index(s, beginMarker):] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBackend(t)
			ctx := context.Background()
			r := rec("L-0A1B2C3D", "PLANNING")
			pub := workspace.TaskPublish{Record: r, Create: workspace.TaskSeed{Description: "My note."}}
			var path string
			var err error
			if tc.legacy {
				path = writeNote(t, b, "Legacy", "---\nforeman_id: "+r.ID+"\nforeman_status: Planning\n---\n"+
					"My note.\n\n"+beginMarker+"\n"+r.Render()+endMarker+"\n")
			} else if path, err = b.UpsertTask(ctx, pub); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			damaged := tc.damage(string(raw))
			if damaged == string(raw) {
				t.Fatal("the note did not change")
			}
			if err := os.WriteFile(path, []byte(damaged), 0o644); err != nil {
				t.Fatal(err)
			}
			entries, err := b.ListTasks(ctx)
			if err != nil || len(entries) != 1 {
				t.Fatalf("entries = %d, err = %v", len(entries), err)
			}
			e := entries[0]
			if e.ParseErr == nil || e.ID != "" || e.Record != nil || e.Description != "" || e.ConfirmedTriage != "" {
				t.Fatalf("unsafe fields are visible to sync: %+v", e)
			}
			if _, err := b.UpsertTask(ctx, pub); err == nil {
				t.Fatal("publish should refuse an unsafe rewrite")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != damaged {
				t.Fatalf("a refused publish changed the note, err = %v", err)
			}
		})
	}
}

func TestARecordPastedBeforeFirstPublishStaysInTheDescription(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	pasted := beginMarker + "\n" + rec("L-BADC0FFE", "INBOX").Render() + endMarker
	path := writeNote(t, b, "New", pasted+"\n")
	r := rec("L-0A1B2C3D", "PLANNING")
	if err := b.AssignID(ctx, path, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.UpsertTask(ctx, workspace.TaskPublish{Record: r}); err != nil {
		t.Fatal(err)
	}
	entries, err := b.ListTasks(ctx)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %d, err = %v", len(entries), err)
	}
	e := entries[0]
	if e.ParseErr != nil || e.Record == nil || e.Record.ID != r.ID || e.Description != pasted {
		t.Fatalf("record = %+v, description = %q, err = %v", e.Record, e.Description, e.ParseErr)
	}
}
