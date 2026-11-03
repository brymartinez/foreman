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
const teamID = "6ef8f87d-9792-42b1-912a-e5b158e3c8c9"

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

func TestAssignedIssuesReturnsOnlyCurrentUsersOpenTeamIssues(t *testing.T) {
	var calls []string
	c := testClient(t, func(q string, v map[string]json.RawMessage) string {
		switch {
		case strings.Contains(q, "viewer"):
			calls = append(calls, "viewer")
			return `{"data":{"viewer":{"id":"user-1","organization":{"urlKey":"acme"}}}}`
		case strings.Contains(q, "teams("):
			calls = append(calls, "teams")
			return `{"data":{"teams":{"nodes":[{"id":"` + teamID + `","key":"WEB"}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}`
		case strings.Contains(q, "issues("):
			calls = append(calls, "issues")
			if !strings.Contains(q, "filter:$filter") || !strings.Contains(string(v["filter"]), `"eq":"user-1"`) || !strings.Contains(string(v["filter"]), `"eq":"`+teamID+`"`) {
				t.Errorf("issue filter = %s, variables = %v", q, v)
			}
			return `{"data":{"issues":{"nodes":[{"id":"` + issueID + `","identifier":"ACME-42","title":"Fix web","url":"https://linear.app/acme/issue/ACME-42/fix-web","description":"Body","createdAt":"2026-10-10T10:00:00Z","updatedAt":"2026-10-11T10:00:00Z","assignee":{"id":"user-1"},"team":{"id":"` + teamID + `"},"state":{"type":"started"},"labels":{"nodes":[{"name":"triaged"}] }},{"id":"other","identifier":"ACME-43","title":"Closed","url":"url","assignee":{"id":"user-1"},"team":{"id":"` + teamID + `"},"state":{"type":"completed"},"labels":{"nodes":[]}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}`
		}
		t.Fatalf("unexpected query: %s", q)
		return ""
	})
	items, err := c.AssignedIssues(context.Background(), "https://linear.app/acme/team/WEB")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Key.Source != task.Linear || items[0].Key.ExternalID != issueID || items[0].Identifier != "ACME-42" || items[0].State != "open" || len(items[0].Labels) != 1 || items[0].Labels[0] != "triaged" {
		t.Fatalf("items = %+v", items)
	}
	if strings.Join(calls, ",") != "viewer,teams,issues" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestIssueReadMapsLabelsAndTerminalStates(t *testing.T) {
	for _, state := range []struct{ kind, want string }{{"started", "open"}, {"completed", "closed"}, {"canceled", "closed"}} {
		t.Run(state.kind, func(t *testing.T) {
			c := testClient(t, func(q string, v map[string]json.RawMessage) string {
				if !strings.Contains(q, "issue(") || !strings.Contains(q, "branchName") {
					t.Fatalf("query = %s", q)
				}
				return `{"data":{"issue":{"id":"` + issueID + `","identifier":"ACME-42","branchName":"bryan/acme-42-fix-web","title":"Fix web","description":"Issue body","url":"https://linear.app/acme/issue/ACME-42/fix-web","updatedAt":"2026-10-11T10:00:00Z","state":{"type":"` + state.kind + `"},"labels":{"nodes":[{"name":"triaged"}]}}}}`
			})
			issue, err := c.GetIssue(context.Background(), task.Key{Repo: "acme/web", Source: task.Linear, ExternalID: issueID})
			if err != nil {
				t.Fatal(err)
			}
			if issue.State != state.want || issue.Body != "Issue body" || issue.Identifier != "ACME-42" || issue.BranchName != "bryan/acme-42-fix-web" || issue.Key.ExternalID != issueID || len(issue.Labels) != 1 || issue.Labels[0] != "triaged" {
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
	teamPages, issuePages := 0, 0
	c := testClient(t, func(q string, v map[string]json.RawMessage) string {
		switch {
		case strings.Contains(q, "viewer"):
			return `{"data":{"viewer":{"id":"user-1","organization":{"urlKey":"acme"}}}}`
		case strings.Contains(q, "teams("):
			teamPages++
			if teamPages == 1 {
				return `{"data":{"teams":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"p2"}}}}`
			}
			if string(v["after"]) != `"p2"` {
				t.Errorf("team cursor = %s", v["after"])
			}
			return `{"data":{"teams":{"nodes":[{"id":"` + teamID + `","key":"WEB"}],"pageInfo":{"hasNextPage":false}}}}`
		case strings.Contains(q, "issues("):
			issuePages++
			if issuePages == 1 {
				return `{"data":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"i2"}}}}`
			}
			if string(v["after"]) != `"i2"` {
				t.Errorf("issue cursor = %s", v["after"])
			}
			return `{"data":{"issues":{"nodes":[{"id":"` + issueID + `","identifier":"ACME-42","title":"Fix web","url":"url","assignee":{"id":"user-1"},"team":{"id":"` + teamID + `"},"state":{"type":"started"}}],"pageInfo":{"hasNextPage":false}}}}`
		}
		t.Fatalf("query = %s", q)
		return ""
	})
	items, err := c.AssignedIssues(context.Background(), "https://linear.app/acme/team/WEB")
	if err != nil || len(items) != 1 || teamPages != 2 || issuePages != 2 {
		t.Fatalf("items = %+v, pages = %d/%d, error = %v", items, teamPages, issuePages, err)
	}
}

func TestAssignedIssuesIncludesAllTeamProjectsAndUnprojectedIssues(t *testing.T) {
	c := testClient(t, func(q string, v map[string]json.RawMessage) string {
		switch {
		case strings.Contains(q, "viewer"):
			return `{"data":{"viewer":{"id":"user-1","organization":{"urlKey":"acme"}}}}`
		case strings.Contains(q, "teams("):
			return `{"data":{"teams":{"nodes":[{"id":"team-1","key":"WEB"}],"pageInfo":{"hasNextPage":false}}}}`
		case strings.Contains(q, "issues("):
			var filter map[string]json.RawMessage
			if err := json.Unmarshal(v["filter"], &filter); err != nil {
				t.Fatal(err)
			}
			if _, ok := filter["project"]; ok {
				t.Error("issue discovery must not filter by project")
			}
			if !strings.Contains(string(filter["team"]), `"eq":"team-1"`) {
				t.Errorf("team filter = %s", filter["team"])
			}
			return `{"data":{"issues":{"nodes":[
    {"id":"one","identifier":"WEB-1","assignee":{"id":"user-1"},"team":{"id":"team-1"},"project":{"id":"project-a"},"state":{"type":"started"}},
    {"id":"two","identifier":"WEB-2","assignee":{"id":"user-1"},"team":{"id":"team-1"},"project":{"id":"project-b"},"state":{"type":"unstarted"}},
    {"id":"three","identifier":"WEB-3","assignee":{"id":"user-1"},"team":{"id":"team-1"},"project":null,"state":{"type":"backlog"}},
    {"id":"foreign","identifier":"OTHER-1","assignee":{"id":"user-1"},"team":{"id":"team-2"},"state":{"type":"started"}},
    {"id":"unassigned","identifier":"WEB-4","assignee":null,"team":{"id":"team-1"},"state":{"type":"started"}},
    {"id":"closed","identifier":"WEB-5","assignee":{"id":"user-1"},"team":{"id":"team-1"},"state":{"type":"completed"}}
   ],"pageInfo":{"hasNextPage":false}}}}`
		default:
			return `{"errors":[{"message":"unexpected project-scoped query"}]}`
		}
	})
	items, err := c.AssignedIssues(context.Background(), "https://linear.app/acme/team/WEB")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d issues, want all 3 assigned open team issues", len(items))
	}
	for _, item := range items {
		if item.Key.Repo != "" {
			t.Errorf("repository = %s", item.Key.Repo)
		}
	}
}

func TestAssignedIssuesRejectsWrongWorkspaceOrMissingTeam(t *testing.T) {
	for _, tc := range []struct{ name, url, want string }{
		{"wrong workspace", "https://linear.app/other/team/WEB", "does not belong"},
		{"inaccessible team", "https://linear.app/acme/team/WEB", "inaccessible"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(q string, _ map[string]json.RawMessage) string {
				if strings.Contains(q, "viewer") {
					return `{"data":{"viewer":{"id":"user-1","organization":{"urlKey":"acme"}}}}`
				}
				if strings.Contains(q, "teams(") {
					return `{"data":{"teams":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}`
				}
				t.Errorf("unexpected issue query: %s", q)
				return `{"data":{}}`
			})
			_, err := c.AssignedIssues(context.Background(), tc.url)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
