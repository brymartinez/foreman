package obsidian

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/knowledge"
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
