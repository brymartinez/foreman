package linear

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

const issueID = "b2e1f213-2798-4bc3-abcd-0a8ab99fc211"
const projectID = "6ef8f87d-9792-42b1-912a-e5b158e3c8c9"

func testClient(t *testing.T, handle func(string, map[string]json.RawMessage) string) Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "personal-key" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		var req struct {
			Query     string                     `json:"query"`
			Variables map[string]json.RawMessage `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(handle(req.Query, req.Variables)))
	}))
	t.Cleanup(srv.Close)
	c := New("personal-key")
	c.Endpoint = srv.URL
	c.HTTP = srv.Client()
	return c
}

func TestEntityNotFoundGraphQLErrorsReturnErrGoneForReadsAndCommentEdits(t *testing.T) {
	c := testClient(t, func(string, map[string]json.RawMessage) string {
		return `{"errors":[{"message":"Entity not found"}]}`
	})
	key := task.Key{Repo: "acme/web", Source: task.Linear, ExternalID: issueID}
	checks := []struct {
		name string
		read func() error
	}{
		{"missing issue read returns ErrGone", func() error { _, err := c.GetIssue(context.Background(), key); return err }},
		{"missing issue comments return ErrGone", func() error { _, err := c.IssueComments(context.Background(), key); return err }},
		{"missing comment read returns ErrGone", func() error { _, err := c.GetIssueComment(context.Background(), "deleted-comment"); return err }},
		{"deleted planning comment edit returns ErrGone", func() error {
			_, err := c.PostIssueComment(context.Background(), key, "deleted-comment", "plan")
			return err
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.read(); !errors.Is(err, app.ErrGone) {
				t.Fatalf("error = %v, want ErrGone", err)
			}
		})
	}
}

func TestAssignedIssuesReturnsOnlyCurrentUsersOpenProjectIssues(t *testing.T) {
	var calls []string
	c := testClient(t, func(q string, v map[string]json.RawMessage) string {
		switch {
		case strings.Contains(q, "viewer"):
			calls = append(calls, "viewer")
			return `{"data":{"viewer":{"id":"user-1"}}}`
		case strings.Contains(q, "projects("):
			calls = append(calls, "projects")
			return `{"data":{"projects":{"nodes":[{"id":"` + projectID + `","url":"https://linear.app/acme/project/web-site-123"}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}`
		case strings.Contains(q, "issues("):
			calls = append(calls, "issues")
			if !strings.Contains(q, "filter:$filter") || !strings.Contains(string(v["filter"]), `"eq":"user-1"`) || !strings.Contains(string(v["filter"]), `"eq":"`+projectID+`"`) {
				t.Errorf("issue filter = %s, variables = %v", q, v)
			}
			return `{"data":{"issues":{"nodes":[{"id":"` + issueID + `","identifier":"ACME-42","title":"Fix web","url":"https://linear.app/acme/issue/ACME-42/fix-web","description":"Body","createdAt":"2026-10-10T10:00:00Z","updatedAt":"2026-10-11T10:00:00Z","assignee":{"id":"user-1"},"project":{"id":"` + projectID + `"},"state":{"type":"started"},"labels":{"nodes":[{"name":"triaged"}] }},{"id":"other","identifier":"ACME-43","title":"Closed","url":"url","assignee":{"id":"user-1"},"project":{"id":"` + projectID + `"},"state":{"type":"completed"},"labels":{"nodes":[]}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}`
		}
		t.Fatalf("unexpected query: %s", q)
		return ""
	})
	items, err := c.AssignedIssues(context.Background(), map[task.Repo]string{"acme/web": "https://linear.app/acme/project/web-site-123"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Key.Source != task.Linear || items[0].Key.ExternalID != issueID || items[0].Identifier != "ACME-42" || items[0].State != "open" || len(items[0].Labels) != 1 || items[0].Labels[0] != "triaged" {
		t.Fatalf("items = %+v", items)
	}
	if strings.Join(calls, ",") != "viewer,projects,issues" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestIssueReadMapsLabelsAndTerminalStates(t *testing.T) {
	for _, state := range []struct{ kind, want string }{{"started", "open"}, {"completed", "closed"}, {"canceled", "closed"}} {
		t.Run(state.kind, func(t *testing.T) {
			c := testClient(t, func(q string, v map[string]json.RawMessage) string {
				if !strings.Contains(q, "issue(") {
					t.Fatalf("query = %s", q)
				}
				return `{"data":{"issue":{"id":"` + issueID + `","identifier":"ACME-42","title":"Fix web","description":"Issue body","url":"https://linear.app/acme/issue/ACME-42/fix-web","updatedAt":"2026-10-11T10:00:00Z","state":{"type":"` + state.kind + `"},"labels":{"nodes":[{"name":"triaged"}]}}}}`
			})
			issue, err := c.GetIssue(context.Background(), task.Key{Repo: "acme/web", Source: task.Linear, ExternalID: issueID})
			if err != nil {
				t.Fatal(err)
			}
			if issue.State != state.want || issue.Body != "Issue body" || issue.Identifier != "ACME-42" || issue.Key.ExternalID != issueID || len(issue.Labels) != 1 || issue.Labels[0] != "triaged" {
				t.Fatalf("issue = %+v", issue)
			}
		})
	}
}

func TestCommentsReadCreateAndEditKeepStringIDs(t *testing.T) {
	var operations []string
	c := testClient(t, func(q string, v map[string]json.RawMessage) string {
		switch {
		case strings.Contains(q, "comments("):
			operations = append(operations, "list")
			return `{"data":{"issue":{"comments":{"nodes":[{"id":"comment-uuid","body":"first","url":"https://linear.app/comment/1"}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}`
		case strings.Contains(q, "comment(id:"):
			operations = append(operations, "get")
			return `{"data":{"comment":{"id":"comment-uuid","body":"first","url":"https://linear.app/comment/1"}}}`
		case strings.Contains(q, "commentCreate("):
			operations = append(operations, "create")
			return `{"data":{"commentCreate":{"success":true,"comment":{"id":"new-uuid","body":"posted","url":"https://linear.app/comment/2"}}}}`
		case strings.Contains(q, "commentUpdate("):
			operations = append(operations, "edit")
			return `{"data":{"commentUpdate":{"success":true,"comment":{"id":"comment-uuid","body":"edited","url":"https://linear.app/comment/1"}}}}`
		}
		t.Fatalf("unexpected query: %s", q)
		return ""
	})
	key := task.Key{Repo: "acme/web", Source: task.Linear, ExternalID: issueID}
	comments, err := c.IssueComments(context.Background(), key)
	if err != nil || len(comments) != 1 || comments[0].ID != "comment-uuid" {
		t.Fatalf("comments = %+v, %v", comments, err)
	}
	got, err := c.GetIssueComment(context.Background(), "comment-uuid")
	if err != nil || got.Body != "first" {
		t.Fatalf("comment = %+v, %v", got, err)
	}
	got, err = c.PostIssueComment(context.Background(), key, "", "posted")
	if err != nil || got.ID != "new-uuid" {
		t.Fatalf("created = %+v, %v", got, err)
	}
	got, err = c.PostIssueComment(context.Background(), key, "comment-uuid", "edited")
	if err != nil || got.Body != "edited" {
		t.Fatalf("edited = %+v, %v", got, err)
	}
	if strings.Join(operations, ",") != "list,get,create,edit" {
		t.Fatalf("operations = %v", operations)
	}
}

func TestGraphQLErrorsFailEvenWithHTTP200(t *testing.T) {
	c := testClient(t, func(string, map[string]json.RawMessage) string {
		return `{"data":{"issue":null},"errors":[{"message":"access denied"}]}`
	})
	_, err := c.GetIssue(context.Background(), task.Key{Repo: "acme/web", Source: task.Linear, ExternalID: issueID})
	if err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("error = %v", err)
	}
}

func TestMissingKeyReturnsSetupError(t *testing.T) {
	c := New("")
	_, err := c.GetIssue(context.Background(), task.Key{Repo: "acme/web", Source: task.Linear, ExternalID: issueID})
	if err == nil || !strings.Contains(err.Error(), "LINEAR_API_KEY") {
		t.Fatalf("error = %v", err)
	}
}

func TestAssignedIssuesReadsEveryPage(t *testing.T) {
	projectPages, issuePages := 0, 0
	c := testClient(t, func(q string, v map[string]json.RawMessage) string {
		switch {
		case strings.Contains(q, "viewer"):
			return `{"data":{"viewer":{"id":"user-1"}}}`
		case strings.Contains(q, "projects("):
			projectPages++
			if projectPages == 1 {
				return `{"data":{"projects":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"p2"}}}}`
			}
			if string(v["after"]) != `"p2"` {
				t.Errorf("project cursor = %s", v["after"])
			}
			return `{"data":{"projects":{"nodes":[{"id":"` + projectID + `","url":"https://linear.app/acme/project/web-site-123"}],"pageInfo":{"hasNextPage":false}}}}`
		case strings.Contains(q, "issues("):
			issuePages++
			if issuePages == 1 {
				return `{"data":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"i2"}}}}`
			}
			if string(v["after"]) != `"i2"` {
				t.Errorf("issue cursor = %s", v["after"])
			}
			return `{"data":{"issues":{"nodes":[{"id":"` + issueID + `","identifier":"ACME-42","title":"Fix web","url":"url","assignee":{"id":"user-1"},"project":{"id":"` + projectID + `"},"state":{"type":"started"}}],"pageInfo":{"hasNextPage":false}}}}`
		}
		t.Fatalf("query = %s", q)
		return ""
	})
	items, err := c.AssignedIssues(context.Background(), map[task.Repo]string{"acme/web": "https://linear.app/acme/project/web-site-123"})
	if err != nil || len(items) != 1 || projectPages != 2 || issuePages != 2 {
		t.Fatalf("items = %+v, pages = %d/%d, error = %v", items, projectPages, issuePages, err)
	}
}
