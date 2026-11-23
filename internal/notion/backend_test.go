package notion

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/portable"
	"github.com/brymartinez/foreman/internal/task"
	"github.com/brymartinez/foreman/internal/workspace"
	"github.com/brymartinez/foreman/internal/workspace/backendtest"
)

func newBackend(t *testing.T) (*Backend, *fake, string) {
	f := newFake(t)
	parent := f.addParentPage()
	return &Backend{C: f.client(), ParentID: parent, Now: func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) }}, f, parent
}

func rec(id, status, plan string) workspace.Record {
	r := workspace.Record{Format: workspace.Format, ID: id, Source: task.Local, Title: "T " + id, Status: status}
	return r
}

func TestNotionBackendMeetsTheContract(t *testing.T) {
	backendtest.Run(t, func(t *testing.T) backendtest.Env {
		b, _, _ := newBackend(t)
		ctx := context.Background()
		return backendtest.Env{
			Backend: b,
			UserCreate: func(e workspace.TaskEntry) string {
				if err := b.Ensure(ctx); err != nil {
					t.Fatal(err)
				}
				props := map[string]any{"Name": map[string]any{"title": richText(e.Name)}}
				if e.ID != "" {
					props["Foreman ID"] = map[string]any{"rich_text": richText(e.ID)}
				}
				body := map[string]any{"parent": map[string]any{"type": "data_source_id", "data_source_id": b.tasks.source}, "properties": props}
				if e.Description != "" {
					body["children"] = paragraphs(e.Description)
				}
				var out struct{ ID string }
				if err := b.C.do(ctx, "POST", "/v1/pages", body, &out); err != nil {
					t.Fatal(err)
				}
				return out.ID
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
					props := map[string]any{
						"Name":             map[string]any{"title": richText(e.Name)},
						"Repository":       map[string]any{"rich_text": richText(e.Repository)},
						"Confirmed triage": map[string]any{"rich_text": richText(e.ConfirmedTriage)},
						"Scope confirmed":  map[string]any{"checkbox": e.ScopeConfirmed},
						"Queue":            map[string]any{"checkbox": e.Queue},
						"Answer":           map[string]any{"rich_text": richText(e.Answer)},
					}
					if err := b.C.do(ctx, "PATCH", "/v1/pages/"+ref, map[string]any{"properties": props}, nil); err != nil {
						t.Fatal(err)
					}
				}
			},
		}
	})
}

func TestEnsureCreatesBothDatabasesWithEveryField(t *testing.T) {
	b, f, parent := newBackend(t)
	ctx := context.Background()
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	titles := map[string]*fdb{}
	for _, db := range f.dbs {
		if db.parent != parent {
			t.Errorf("database %s is not inside the parent page", db.title)
		}
		titles[db.title] = db
	}
	if len(titles) != 2 || titles["Tasks"] == nil || titles["Repository Knowledge"] == nil {
		t.Fatalf("databases = %v", titles)
	}
	tasks := f.sources[titles["Tasks"].source].props
	for name, typ := range map[string]string{"Name": "title", "Foreman ID": "rich_text", "Status": "select", "Source": "select", "Repository": "rich_text",
		"Source URL": "url", "Tags": "multi_select", "Confirmed triage": "rich_text", "Scope confirmed": "checkbox", "Queue": "checkbox",
		"Question": "rich_text", "Answer": "rich_text", "Branch": "rich_text", "PR": "url", "Commit": "rich_text", "Updated by Foreman": "date"} {
		if tasks[name] != typ {
			t.Errorf("Tasks.%s = %q, want %s", name, tasks[name], typ)
		}
	}
	know := f.sources[titles["Repository Knowledge"].source].props
	for name, typ := range map[string]string{"Repository": "title", "Active learnings": "number", "Updated by Foreman": "date"} {
		if know[name] != typ {
			t.Errorf("Repository Knowledge.%s = %q, want %s", name, know[name], typ)
		}
	}
	// A second backend finds them again by title and creates nothing.
	b2 := &Backend{C: b.C, ParentID: parent}
	if err := b2.Ensure(ctx); err != nil || len(f.dbs) != 2 {
		t.Fatalf("second ensure: %v, %d databases", err, len(f.dbs))
	}
}

func TestEnsureAddsMissingFieldsAndKeepsUserFields(t *testing.T) {
	b, f, _ := newBackend(t)
	ctx := context.Background()
	b.Ensure(ctx)
	src := f.sources[b.tasks.source]
	delete(src.props, "Queue")
	src.props["My own column"] = "rich_text"
	if err := (&Backend{C: b.C, ParentID: b.ParentID}).Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if src.props["Queue"] != "checkbox" {
		t.Error("the missing Queue field was not added")
	}
	if src.props["My own column"] != "rich_text" {
		t.Error("a user's column must survive")
	}
}

func TestEveryRequestUsesTheDocumentedVersionAndTheToken(t *testing.T) {
	b, f, _ := newBackend(t)
	b.Ensure(context.Background())
	for _, v := range f.Versions {
		if v != "2025-09-03" {
			t.Fatalf("version header = %q", v)
		}
	}
}

func TestClientWaitsAsLongAsRetryAfterAndThenSucceeds(t *testing.T) {
	f := newFake(t)
	calls := 0
	f.Fail = func(m, p string) (int, string) {
		calls++
		if calls <= 2 {
			return http.StatusTooManyRequests, "3"
		}
		return 0, ""
	}
	c := f.client()
	var slept []time.Duration
	c.Sleep = func(d time.Duration) { slept = append(slept, d) }
	if err := c.do(context.Background(), "GET", "/v1/users/me", nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 2 || slept[0] != 3*time.Second || slept[1] != 3*time.Second {
		t.Fatalf("slept = %v", slept)
	}
}

func TestClientRetriesServerErrorsFiveTimesThenFailsWithoutTheToken(t *testing.T) {
	f := newFake(t)
	f.Fail = func(m, p string) (int, string) { return http.StatusBadGateway, "" }
	c := f.client()
	var slept []time.Duration
	c.Sleep = func(d time.Duration) { slept = append(slept, d) }
	err := c.do(context.Background(), "GET", "/v1/users/me", nil, nil)
	if err == nil {
		t.Fatal("must fail")
	}
	if len(f.Requests) != 6 || len(slept) != 5 {
		t.Fatalf("requests = %d, sleeps = %d; want 1 try and 5 retries", len(f.Requests), len(slept))
	}
	if !(slept[1] > slept[0] && slept[4] > slept[3]) {
		t.Errorf("backoff must grow: %v", slept)
	}
	if strings.Contains(err.Error(), f.token) {
		t.Fatalf("the error leaks the token: %v", err)
	}
}

func TestClientDoesNotRetryAClientError(t *testing.T) {
	f := newFake(t)
	c := f.client()
	c.Token = "wrong"
	var e *Error
	if err := c.do(context.Background(), "GET", "/v1/users/me", nil, nil); !errors.As(err, &e) || e.Status != 401 || len(f.Requests) != 1 {
		t.Fatalf("err = %v, requests = %d", err, len(f.Requests))
	}
}

func TestLongTextIsSplitIntoRichTextItemsOfAtMost2000Characters(t *testing.T) {
	items := richText(strings.Repeat("é", 4500))
	if len(items) != 3 {
		t.Fatalf("items = %d", len(items))
	}
	for _, it := range items {
		if n := len([]rune(it.(map[string]any)["text"].(map[string]any)["content"].(string))); n > 2000 {
			t.Fatalf("item has %d characters", n)
		}
	}
	if len(richText("")) != 0 {
		t.Error("empty text has no items")
	}
}

func TestAPublishWithMoreThan100BlocksAppendsInBatchesOfAtMost100(t *testing.T) {
	b, f, _ := newBackend(t)
	ctx := context.Background()
	r := rec("L-0A1B2C3D", "IMPLEMENTING", "")
	r.Work = nil
	long := strings.Repeat("line\n", 250)
	desc := strings.Repeat("x", 4500)
	_, err := b.UpsertTask(ctx, workspace.TaskPublish{Record: r, Reason: long, Create: workspace.TaskSeed{Name: "N", Description: desc}})
	if err != nil {
		t.Fatal(err) // the fake rejects any request with more than 100 children
	}
	entries, _ := b.ListTasks(ctx)
	if len(entries) != 1 || entries[0].Description != desc || entries[0].Record == nil {
		t.Fatalf("entries = %+v", entries)
	}
	for _, blk := range f.blocks {
		if rts, ok := blk.content["rich_text"].([]any); ok {
			for _, rt := range rts {
				if n := len(rt.(map[string]any)["text"].(map[string]any)["content"].(string)); n > 2000 {
					t.Fatalf("a rich-text item has %d characters", n)
				}
			}
		}
	}
}

func TestSectionIsReplacedByAppendingFirstThenDeletingTheOld(t *testing.T) {
	b, f, _ := newBackend(t)
	ctx := context.Background()
	b.UpsertTask(ctx, workspace.TaskPublish{Record: rec("L-0A1B2C3D", "PLANNING", ""), Create: workspace.TaskSeed{Name: "N"}})
	f.mu.Lock()
	f.Requests = nil
	f.mu.Unlock()
	b.UpsertTask(ctx, workspace.TaskPublish{Record: rec("L-0A1B2C3D", "IMPLEMENTING", "")})
	appendAt, deleteAt := -1, -1
	for i, r := range f.Requests {
		if strings.HasPrefix(r, "PATCH /v1/blocks/") && appendAt < 0 {
			appendAt = i
		}
		if strings.HasPrefix(r, "DELETE /v1/blocks/") && deleteAt < 0 {
			deleteAt = i
		}
	}
	if appendAt < 0 || deleteAt < 0 || appendAt > deleteAt {
		t.Fatalf("append at %d, delete at %d; the new section must be appended before the old is deleted: %v", appendAt, deleteAt, f.Requests)
	}
	entries, _ := b.ListTasks(ctx)
	if entries[0].Record.Status != "IMPLEMENTING" {
		t.Fatalf("status = %s", entries[0].Record.Status)
	}
}

func TestACrashBetweenAppendAndDeleteStillReadsTheLastValidSection(t *testing.T) {
	b, f, _ := newBackend(t)
	ctx := context.Background()
	b.UpsertTask(ctx, workspace.TaskPublish{Record: rec("L-0A1B2C3D", "PLANNING", ""), Create: workspace.TaskSeed{Name: "N"}})
	f.Fail = func(m, p string) (int, string) {
		if m == "DELETE" {
			return 400, ""
		}
		return 0, ""
	}
	if _, err := b.UpsertTask(ctx, workspace.TaskPublish{Record: rec("L-0A1B2C3D", "IMPLEMENTING", "")}); err == nil {
		t.Fatal("the failed delete must be reported")
	}
	f.Fail = nil
	b.forget() // a fresh process has no cache
	entries, _ := b.ListTasks(ctx)
	if got := entries[0].Record; got == nil || got.Status != "IMPLEMENTING" {
		t.Fatalf("record = %+v; two sections exist and the last valid one must win", got)
	}
}

func TestASectionWhoseAppendDidNotFinishIsIgnored(t *testing.T) {
	b, f, _ := newBackend(t)
	ctx := context.Background()
	b.UpsertTask(ctx, workspace.TaskPublish{Record: rec("L-0A1B2C3D", "PLANNING", ""), Create: workspace.TaskSeed{Name: "N"}})
	// Break the new section's payload the way a crash mid-append would.
	f.Fail = func(m, p string) (int, string) {
		if m == "PATCH" && strings.HasPrefix(p, "/v1/blocks/b") {
			return 400, "" // children of the new heading never arrive
		}
		return 0, ""
	}
	_, err := b.UpsertTask(ctx, workspace.TaskPublish{Record: rec("L-0A1B2C3D", "IMPLEMENTING", ""), Reason: strings.Repeat("why\n", 150)})
	f.Fail = nil
	b.forget()
	entries, _ := b.ListTasks(ctx)
	if err == nil {
		t.Fatal("the failed append must be reported")
	}
	if got := entries[0].Record; got == nil || got.Status != "PLANNING" {
		t.Fatalf("record = %+v; the incomplete section must lose to the older valid one", got)
	}
}

func TestListTasksFollowsPagination(t *testing.T) {
	b, f, _ := newBackend(t)
	ctx := context.Background()
	f.PageSize = 2
	for i := 0; i < 5; i++ {
		id := "L-0000000" + string(rune('A'+i))
		b.UpsertTask(ctx, workspace.TaskPublish{Record: rec(id, "INBOX", ""), Create: workspace.TaskSeed{Name: id}})
	}
	entries, err := b.ListTasks(ctx)
	if err != nil || len(entries) != 5 {
		t.Fatalf("entries = %d, %v", len(entries), err)
	}
}

func TestListTasksReusesParsedBodiesForUnchangedPages(t *testing.T) {
	b, f, _ := newBackend(t)
	ctx := context.Background()
	b.UpsertTask(ctx, workspace.TaskPublish{Record: rec("L-0A1B2C3D", "INBOX", ""), Create: workspace.TaskSeed{Name: "N"}})
	b.ListTasks(ctx)
	before := f.count("GET", "/v1/blocks/")
	b.ListTasks(ctx)
	if after := f.count("GET", "/v1/blocks/"); after != before {
		t.Fatalf("an unchanged page was read again: %d -> %d block requests", before, after)
	}
}

func TestUpsertPublishesTheForemanOwnedFields(t *testing.T) {
	b, f, _ := newBackend(t)
	ctx := context.Background()
	r := rec("L-0A1B2C3D", "NEEDS_DECISION", "")
	r.Work = nil
	r.Repo = "o/r"
	r.Work = nil
	b.UpsertTask(ctx, workspace.TaskPublish{Record: r, Create: workspace.TaskSeed{Name: "N", Repository: "o/r", SourceURL: "https://x.test/1", Tags: []string{"a"}}})
	for _, pg := range f.pages {
		if pg.source != b.tasks.source {
			continue
		}
		if sel := pg.props["Status"].(map[string]any)["select"].(map[string]any)["name"]; sel != "Needs decision" {
			t.Errorf("Status = %v", sel)
		}
		if sel := pg.props["Source"].(map[string]any)["select"].(map[string]any)["name"]; sel != "Local" {
			t.Errorf("Source = %v", sel)
		}
		if pg.props["Updated by Foreman"] == nil {
			t.Error("Updated by Foreman is not set")
		}
	}
}

func TestCheckPassesAndLeavesNoTestTask(t *testing.T) {
	b, f, _ := newBackend(t)
	var steps []workspace.Step
	if err := b.Check(context.Background(), func(s workspace.Step) { steps = append(steps, s) }); err != nil {
		t.Fatal(err)
	}
	names := []string{"authenticate", "ensure databases", "read Tasks data source", "create test task", "update test task", "read test task back", "confirm update is visible", "trash test task"}
	if len(steps) != len(names) {
		t.Fatalf("steps = %+v", steps)
	}
	for i, s := range steps {
		if s.Name != names[i] || s.Err != nil {
			t.Errorf("step %d = %+v, want %s", i, s, names[i])
		}
	}
	trashed := 0
	for _, pg := range f.pages {
		if pg.source == b.tasks.source {
			if !pg.trashed || !strings.HasPrefix(plain(pg.props["Name"].(map[string]any)["title"]), "Foreman connectivity check ") {
				t.Errorf("test task not trashed or misnamed: %+v", pg.props["Name"])
			}
			trashed++
		}
	}
	if trashed != 1 {
		t.Errorf("test tasks = %d", trashed)
	}
}

func TestCheckStopsAtTheFirstFailingStep(t *testing.T) {
	// Count the requests of a passing run, then fail each one in turn.
	b, f, _ := newBackend(t)
	b.Check(context.Background(), func(workspace.Step) {})
	total := len(f.Requests)
	for n := 1; n <= total; n++ {
		b, f, _ := newBackend(t)
		seen := 0
		f.Fail = func(m, p string) (int, string) {
			seen++
			if seen == n {
				return http.StatusForbidden, ""
			}
			return 0, ""
		}
		var steps []workspace.Step
		err := b.Check(context.Background(), func(s workspace.Step) { steps = append(steps, s) })
		if err == nil {
			t.Errorf("request %d failing: the check passed", n)
			continue
		}
		last := steps[len(steps)-1]
		if last.Err == nil {
			t.Errorf("request %d failing: the last step reported no failure: %+v", n, steps)
		}
		for _, s := range steps[:len(steps)-1] {
			if s.Err != nil {
				t.Errorf("request %d failing: a step before the failure also failed: %+v", n, steps)
			}
		}
		if strings.Contains(err.Error(), f.token) {
			t.Errorf("the error leaks the token")
		}
	}
}

func TestNewReadsTheTestOnlyBaseURLOverride(t *testing.T) {
	t.Setenv(BaseEnv, "http://127.0.0.1:9/")
	if c := New("tok"); c.Base != "http://127.0.0.1:9" {
		t.Fatalf("base = %q", c.Base)
	}
	t.Setenv(BaseEnv, "")
	if c := New("tok"); c.Base != DefaultBase {
		t.Fatalf("base = %q", c.Base)
	}
}

func TestClientDoesNotRepeatACreateThatAnsweredWithAServerError(t *testing.T) {
	f := newFake(t)
	f.Fail = func(m, p string) (int, string) {
		if m == "POST" && p == "/v1/pages" {
			return http.StatusServiceUnavailable, ""
		}
		return 0, ""
	}
	c := f.client()
	if err := c.do(context.Background(), "POST", "/v1/pages", map[string]any{}, nil); err == nil {
		t.Fatal("must fail")
	}
	if n := f.count("POST", "/v1/pages"); n != 1 {
		t.Fatalf("create requests = %d, want 1: Notion may have applied the first one", n)
	}
}

func TestClientStillRetriesAThrottledCreate(t *testing.T) {
	f := newFake(t)
	calls := 0
	f.Fail = func(m, p string) (int, string) {
		if m == "POST" && p == "/v1/pages" && calls == 0 {
			calls++
			return http.StatusTooManyRequests, "0"
		}
		return 0, ""
	}
	c := f.client()
	if err := c.do(context.Background(), "POST", "/v1/pages", map[string]any{"parent": map[string]any{"page_id": "x"}}, nil); err != nil && IsNotFound(err) == false {
		t.Fatalf("a refused request is safe to repeat: %v", err)
	}
	if n := f.count("POST", "/v1/pages"); n != 2 {
		t.Fatalf("create requests = %d, want 2", n)
	}
}

func TestAPlanLargerThanOneRequestIsSplitByBytesAndStaysReadable(t *testing.T) {
	b, _, _ := newBackend(t)
	ctx := context.Background()
	r := rec("L-0A1B2C3D", "IMPLEMENTING", "")
	// Fewer than 100 blocks, but the rendered text and the payload together exceed
	// 500 KB; the fake rejects any larger request.
	big := strings.Repeat("0123456789", 30_000) // 300 KB, one line
	r.Work = &portable.Work{Repo: "o/r", State: "open", Plan: big}
	if _, err := b.UpsertTask(ctx, workspace.TaskPublish{Record: r, Create: workspace.TaskSeed{Name: "N"}}); err != nil {
		t.Fatal(err)
	}
	entries, err := b.ListTasks(ctx)
	if err != nil || len(entries) != 1 || entries[0].Record == nil || entries[0].Record.Work == nil || entries[0].Record.Work.Plan != big {
		t.Fatalf("the plan did not round trip: %v", err)
	}
}

func TestConfirmedTriageLongerThan2000CharactersIsStoredWhole(t *testing.T) {
	b, _, _ := newBackend(t)
	ctx := context.Background()
	text := strings.Repeat("scope ", 700) // 4200 characters
	text = strings.TrimSpace(text)
	if _, err := b.UpsertTask(ctx, workspace.TaskPublish{Record: rec("L-0A1B2C3D", "INBOX", ""), Create: workspace.TaskSeed{Name: "N", ConfirmedTriage: text, ScopeConfirmed: true}}); err != nil {
		t.Fatal(err)
	}
	entries, _ := b.ListTasks(ctx)
	if len(entries) != 1 || entries[0].ConfirmedTriage != text {
		t.Fatalf("triage = %d characters, want %d", len(entries[0].ConfirmedTriage), len(text))
	}
}

func TestAKnowledgePageWithNoPayloadIsReportedAsNoPayloadNotAsBroken(t *testing.T) {
	b, f, _ := newBackend(t)
	ctx := context.Background()
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"parent": map[string]any{"type": "data_source_id", "data_source_id": b.knowledge.source},
		"properties": map[string]any{"Repository": map[string]any{"title": richText("o/r")}}}
	if err := b.C.do(ctx, "POST", "/v1/pages", body, nil); err != nil {
		t.Fatal(err)
	}
	es, err := b.ListKnowledge(ctx)
	if err != nil || len(es) != 1 || !errors.Is(es[0].ParseErr, workspace.ErrNoPayload) {
		t.Fatalf("entries = %+v, err = %v", es, err)
	}
	_ = f
}
