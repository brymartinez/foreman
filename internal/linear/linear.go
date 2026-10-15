package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

const endpoint = "https://api.linear.app/graphql"

// Client uses a personal API key. No key is read until the caller selects Linear.
type Client struct {
	Key      string
	HTTP     *http.Client
	Endpoint string
}

var _ app.IssueTracker = Client{}

func New(key string) Client {
	return Client{Key: key, HTTP: http.DefaultClient, Endpoint: endpoint}
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type issueNode struct {
	ID          string    `json:"id"`
	Identifier  string    `json:"identifier"`
	BranchName  string    `json:"branchName"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	URL         string    `json:"url"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Assignee    *struct {
		ID string `json:"id"`
	} `json:"assignee"`
	Project *struct {
		ID string `json:"id"`
	} `json:"project"`
	State *struct {
		Type string `json:"type"`
	} `json:"state"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
		PageInfo pageInfo `json:"pageInfo"`
	} `json:"labels"`
}

func (n issueNode) labels() []string {
	out := make([]string, 0, len(n.Labels.Nodes))
	for _, label := range n.Labels.Nodes {
		out = append(out, label.Name)
	}
	return out
}

func (n issueNode) state() string {
	if n.State == nil {
		return ""
	}
	switch n.State.Type {
	case "completed", "canceled":
		return "closed"
	case "triage", "backlog", "unstarted", "started":
		return "open"
	default:
		return ""
	}
}

func (c Client) AssignedIssues(ctx context.Context, projects map[task.Repo]string) ([]app.InboxItem, error) {
	if len(projects) == 0 {
		return nil, nil
	}
	var viewer struct {
		Viewer struct {
			ID string `json:"id"`
		} `json:"viewer"`
	}
	if err := c.query(ctx, `query { viewer { id } }`, nil, &viewer); err != nil {
		return nil, err
	}
	if viewer.Viewer.ID == "" {
		return nil, errors.New("Linear viewer has no ID")
	}
	projectRepos := make(map[string]task.Repo, len(projects))
	for repo, projectURL := range projects {
		projectRepos[strings.TrimRight(projectURL, "/")] = repo
	}
	resolved := make(map[string]task.Repo, len(projects))
	cursor := ""
	for {
		var data struct {
			Projects struct {
				Nodes []struct {
					ID  string `json:"id"`
					URL string `json:"url"`
				} `json:"nodes"`
				PageInfo pageInfo `json:"pageInfo"`
			} `json:"projects"`
		}
		if err := c.query(ctx, `query($after:String){ projects(first:50,after:$after){ nodes{id url} pageInfo{hasNextPage endCursor} } }`, after(cursor), &data); err != nil {
			return nil, err
		}
		for _, p := range data.Projects.Nodes {
			if repo, ok := projectRepos[strings.TrimRight(p.URL, "/")]; ok {
				resolved[p.ID] = repo
			}
		}
		if len(resolved) == len(projects) || !data.Projects.PageInfo.HasNextPage {
			break
		}
		if data.Projects.PageInfo.EndCursor == "" || data.Projects.PageInfo.EndCursor == cursor {
			return nil, errors.New("Linear projects pagination did not advance")
		}
		cursor = data.Projects.PageInfo.EndCursor
	}
	if len(resolved) != len(projects) {
		return nil, fmt.Errorf("found %d of %d configured Linear projects", len(resolved), len(projects))
	}
	var items []app.InboxItem
	for projectID, repo := range resolved {
		cursor = ""
		for {
			var data struct {
				Issues struct {
					Nodes    []issueNode `json:"nodes"`
					PageInfo pageInfo    `json:"pageInfo"`
				} `json:"issues"`
			}
			vars := after(cursor)
			vars["filter"] = map[string]any{"assignee": map[string]any{"id": map[string]string{"eq": viewer.Viewer.ID}}, "project": map[string]any{"id": map[string]string{"eq": projectID}}}
			query := `query($filter:IssueFilter,$after:String){ issues(first:50,after:$after,filter:$filter){ nodes{id identifier title description url createdAt updatedAt assignee{id} project{id} state{type} labels(first:250){nodes{name}}} pageInfo{hasNextPage endCursor} } }`
			if err := c.query(ctx, query, vars, &data); err != nil {
				return nil, err
			}
			for _, n := range data.Issues.Nodes {
				if n.Assignee == nil || n.Assignee.ID != viewer.Viewer.ID || n.Project == nil || n.Project.ID != projectID || n.state() != "open" {
					continue
				}
				if n.ID == "" || n.Identifier == "" {
					return nil, errors.New("Linear issue missing ID or identifier")
				}
				items = append(items, app.InboxItem{Key: task.Key{Repo: repo, Source: task.Linear, ExternalID: n.ID}, Identifier: n.Identifier, Kind: task.Issue, Title: n.Title, URL: n.URL, State: "open", CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt, Reasons: []task.InboxReason{task.Assigned}, Labels: n.labels()})
			}
			if !data.Issues.PageInfo.HasNextPage {
				break
			}
			if data.Issues.PageInfo.EndCursor == "" || data.Issues.PageInfo.EndCursor == cursor {
				return nil, errors.New("Linear issues pagination did not advance")
			}
			cursor = data.Issues.PageInfo.EndCursor
		}
	}
	return items, nil
}

func (c Client) GetIssue(ctx context.Context, key task.Key) (app.Issue, error) {
	if err := validKey(key); err != nil {
		return app.Issue{}, err
	}
	var data struct {
		Issue *issueNode `json:"issue"`
	}
	err := c.query(ctx, `query($id:String!){ issue(id:$id){id identifier branchName title description url updatedAt state{type} labels(first:250){nodes{name}}} }`, map[string]any{"id": key.ExternalID}, &data)
	if err != nil {
		return app.Issue{}, err
	}
	if data.Issue == nil {
		return app.Issue{}, app.ErrGone
	}
	n := data.Issue
	if n.ID != key.ExternalID || n.state() == "" {
		return app.Issue{}, fmt.Errorf("Linear issue %s has invalid identity or state", key.ExternalID)
	}
	return app.Issue{Key: key, Identifier: n.Identifier, BranchName: n.BranchName, Title: n.Title, Body: n.Description, URL: n.URL, Labels: n.labels(), State: n.state(), UpdatedAt: n.UpdatedAt}, nil
}

type commentNode struct {
	ID   string `json:"id"`
	Body string `json:"body"`
	URL  string `json:"url"`
}

func (n commentNode) comment() app.TrackerComment {
	return app.TrackerComment{ID: n.ID, Body: n.Body, URL: n.URL}
}

func (c Client) IssueComments(ctx context.Context, key task.Key) ([]app.TrackerComment, error) {
	if err := validKey(key); err != nil {
		return nil, err
	}
	var out []app.TrackerComment
	cursor := ""
	for {
		var data struct {
			Issue *struct {
				Comments struct {
					Nodes    []commentNode `json:"nodes"`
					PageInfo pageInfo      `json:"pageInfo"`
				} `json:"comments"`
			} `json:"issue"`
		}
		vars := after(cursor)
		vars["id"] = key.ExternalID
		if err := c.query(ctx, `query($id:String!,$after:String){ issue(id:$id){comments(first:50,after:$after){nodes{id body url} pageInfo{hasNextPage endCursor}}} }`, vars, &data); err != nil {
			return nil, err
		}
		if data.Issue == nil {
			return nil, app.ErrGone
		}
		for _, n := range data.Issue.Comments.Nodes {
			out = append(out, n.comment())
		}
		page := data.Issue.Comments.PageInfo
		if !page.HasNextPage {
			return out, nil
		}
		if page.EndCursor == "" || page.EndCursor == cursor {
			return nil, errors.New("Linear comments pagination did not advance")
		}
		cursor = page.EndCursor
	}
}

func (c Client) GetIssueComment(ctx context.Context, id string) (app.TrackerComment, error) {
	if id == "" {
		return app.TrackerComment{}, errors.New("Linear comment ID is empty")
	}
	var data struct {
		Comment *commentNode `json:"comment"`
	}
	if err := c.query(ctx, `query($id:String!){ comment(id:$id){id body url} }`, map[string]any{"id": id}, &data); err != nil {
		return app.TrackerComment{}, err
	}
	if data.Comment == nil {
		return app.TrackerComment{}, app.ErrGone
	}
	return data.Comment.comment(), nil
}

func (c Client) PostIssueComment(ctx context.Context, key task.Key, id, body string) (app.TrackerComment, error) {
	if err := validKey(key); err != nil {
		return app.TrackerComment{}, err
	}
	var data struct {
		CommentCreate *struct {
			Success bool         `json:"success"`
			Comment *commentNode `json:"comment"`
		} `json:"commentCreate"`
		CommentUpdate *struct {
			Success bool         `json:"success"`
			Comment *commentNode `json:"comment"`
		} `json:"commentUpdate"`
	}
	var err error
	if id == "" {
		err = c.query(ctx, `mutation($issue: String!,$body:String!){ commentCreate(input:{issueId:$issue,body:$body}){success comment{id body url}} }`, map[string]any{"issue": key.ExternalID, "body": body}, &data)
	} else {
		err = c.query(ctx, `mutation($id:String!,$body:String!){ commentUpdate(id:$id,input:{body:$body}){success comment{id body url}} }`, map[string]any{"id": id, "body": body}, &data)
	}
	if err != nil {
		return app.TrackerComment{}, err
	}
	result := data.CommentCreate
	if id != "" {
		result = data.CommentUpdate
	}
	if result == nil || !result.Success || result.Comment == nil || result.Comment.ID == "" {
		return app.TrackerComment{}, errors.New("Linear comment mutation did not succeed")
	}
	return result.Comment.comment(), nil
}

func validKey(key task.Key) error {
	if key.Source != task.Linear || key.ExternalID == "" {
		return fmt.Errorf("invalid Linear issue key: %+v", key)
	}
	return nil
}

func after(cursor string) map[string]any {
	if cursor == "" {
		return map[string]any{"after": nil}
	}
	return map[string]any{"after": cursor}
}

func (c Client) query(ctx context.Context, query string, variables map[string]any, data any) error {
	if c.Key == "" {
		return errors.New("set LINEAR_API_KEY to use Linear issues")
	}
	payload, err := json.Marshal(struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}{query, variables})
	if err != nil {
		return err
	}
	target := c.Endpoint
	if target == "" {
		target = endpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.Key)
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Linear GraphQL request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Linear GraphQL HTTP %d", resp.StatusCode)
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode Linear GraphQL response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		message := envelope.Errors[0].Message
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(message)), "entity not found") {
			return fmt.Errorf("Linear GraphQL: %s: %w", message, app.ErrGone)
		}
		return fmt.Errorf("Linear GraphQL: %s", message)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return errors.New("Linear GraphQL returned no data")
	}
	if err := json.Unmarshal(envelope.Data, data); err != nil {
		return fmt.Errorf("decode Linear GraphQL data: %w", err)
	}
	return nil
}
