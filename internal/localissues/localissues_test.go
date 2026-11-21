package localissues

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/db"
	"github.com/brymartinez/foreman/internal/task"
)

var ctx = context.Background()

func newStore(t *testing.T) Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "foreman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return Store{DB: d, Now: func() time.Time { return time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC) }}
}

func TestCreateGivesAStableIDIndependentOfRepository(t *testing.T) {
	s := newStore(t)
	a, err := s.Create(ctx, Input{Title: "Fix cache", Description: "Use retries.", SourceURL: "https://linear.app/acme/issue/WEB-42", Notes: "ask Dana", Tags: []string{"backend", " backend ", ""}})
	if err != nil {
		t.Fatal(err)
	}
	if !IDPattern.MatchString(a.ID) || a.Status != Open || len(a.Tags) != 1 {
		t.Fatalf("issue = %+v", a)
	}
	b, _ := s.Create(ctx, Input{Title: "Other"})
	if a.ID == b.ID {
		t.Fatal("two issues share an ID")
	}
	got, err := s.Get(ctx, a.ID)
	if err != nil || got.Title != "Fix cache" || got.SourceURL != "https://linear.app/acme/issue/WEB-42" || got.Notes != "ask Dana" || !got.CreatedAt.Equal(a.CreatedAt) {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := s.Get(ctx, "L-DEADBEEF"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing issue = %v", err)
	}
}

func TestInputValidation(t *testing.T) {
	for name, in := range map[string]Input{
		"no title":        {Title: "  "},
		"file URL":        {Title: "x", SourceURL: "file:///etc/passwd"},
		"URL credentials": {Title: "x", SourceURL: "https://user:pw@example.com/x"},
		"not a URL":       {Title: "x", SourceURL: "linear"},
		"invalid repo":    {Title: "x", Repo: "nope"},
	} {
		if err := in.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (Input{Title: "x", SourceURL: "https://example.com/a", Repo: "acme/web"}).Validate(); err != nil {
		t.Errorf("valid input rejected: %v", err)
	}
}

func TestOpenIssuesFeedTheInboxWithoutARepositoryAndNeverLookTriaged(t *testing.T) {
	s := newStore(t)
	open, _ := s.Create(ctx, Input{Title: "Open one", Tags: []string{"triaged", "api"}})
	done, _ := s.Create(ctx, Input{Title: "Done one"})
	if err := s.SetStatus(ctx, done.ID, Done); err != nil {
		t.Fatal(err)
	}
	items, err := s.OpenIssues(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("open issues = %+v, %v", items, err)
	}
	it := items[0]
	if it.Key != (task.Key{Source: task.Local, ExternalID: open.ID}) || it.Identifier != open.ID || it.State != "open" || it.Kind != task.Issue {
		t.Fatalf("item = %+v", it)
	}
	for _, l := range it.Labels {
		if strings.EqualFold(l, "triaged") {
			t.Fatalf("a user tag spelled triaged would pass for GitHub's confirmed-triage label: %v", it.Labels)
		}
	}
}

func TestGetIssueAddsTheRepositoryHintAsALinkRepositorySelectionReads(t *testing.T) {
	s := newStore(t)
	i, _ := s.Create(ctx, Input{Title: "t", Description: "body", Repo: "acme/web", SourceURL: "https://tracker.example/1"})
	got, err := s.GetIssue(ctx, task.Key{Source: task.Local, ExternalID: i.ID})
	if err != nil || !strings.Contains(got.Body, "https://github.com/acme/web") || got.URL != "https://tracker.example/1" || got.Identifier != i.ID {
		t.Fatalf("issue = %+v, %v", got, err)
	}
	if _, err := s.GetIssue(ctx, task.Key{Source: task.Local, ExternalID: "L-DEADBEEF"}); !errors.Is(err, app.ErrGone) {
		t.Fatalf("missing = %v, want ErrGone", err)
	}
}

func TestCompleteClosesTheIssue(t *testing.T) {
	s := newStore(t)
	i, _ := s.Create(ctx, Input{Title: "t"})
	key := Key(i.ID, "acme/web")
	if err := s.Complete(ctx, key); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetIssue(ctx, key)
	if got.State != "closed" {
		t.Fatalf("state = %q", got.State)
	}
	if err := s.Complete(ctx, Key("L-DEADBEEF", "a/b")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("complete missing = %v", err)
	}
}

func TestCreateInRollsBackWithItsTransaction(t *testing.T) {
	s := newStore(t)
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateIn(ctx, tx, Input{Title: "t"}); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if issues, _ := s.List(ctx); len(issues) != 0 {
		t.Fatalf("a rolled-back intake left %d issues", len(issues))
	}
}
