// Package github reads GitHub state through the gh CLI, which is already authenticated.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

// Runner executes gh with args in dir ("" for the current directory) and returns
// stdout. Tests substitute recorded output.
type Runner func(ctx context.Context, dir string, args ...string) ([]byte, error)

type Client struct {
	Run Runner
}

func New() Client { return Client{Run: execGH} }

func execGH(ctx context.Context, dir string, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

const searchFields = "number,title,url,state,updatedAt,repository"

type search struct {
	reason task.InboxReason
	kind   task.Kind
	args   []string
}

var inboxSearches = []search{
	{task.Assigned, task.Issue, []string{"search", "issues", "--assignee=@me"}},
	{task.Mentioned, task.Issue, []string{"search", "issues", "--mentions=@me"}},
	{task.ReviewRequested, task.PullRequest, []string{"search", "prs", "--review-requested=@me"}},
}

type searchHit struct {
	Number     int       `json:"number"`
	Title      string    `json:"title"`
	URL        string    `json:"url"`
	State      string    `json:"state"`
	UpdatedAt  time.Time `json:"updatedAt"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
}

// InboxItems runs the inbox searches concurrently and merges hits for the same item.
func (c Client) InboxItems(ctx context.Context) ([]app.InboxItem, error) {
	results := make([][]app.InboxItem, len(inboxSearches))
	errs := make([]error, len(inboxSearches))
	var wg sync.WaitGroup
	for i, s := range inboxSearches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = c.search(ctx, s)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	var merged []app.InboxItem
	index := map[task.Key]int{}
	for _, items := range results {
		for _, it := range items {
			if i, ok := index[it.Key]; ok {
				merged[i].Reasons = append(merged[i].Reasons, it.Reasons...)
				continue
			}
			index[it.Key] = len(merged)
			merged = append(merged, it)
		}
	}
	return merged, nil
}

func (c Client) search(ctx context.Context, s search) ([]app.InboxItem, error) {
	args := append(append([]string{}, s.args...), "--state=open", "--limit=100", "--json="+searchFields)
	out, err := c.Run(ctx, "", args...)
	if err != nil {
		return nil, err
	}
	var hits []searchHit
	if err := json.Unmarshal(out, &hits); err != nil {
		return nil, fmt.Errorf("parse gh %s output: %w", strings.Join(s.args, " "), err)
	}
	items := make([]app.InboxItem, 0, len(hits))
	for _, h := range hits {
		repo, err := task.ParseRepo(h.Repository.NameWithOwner)
		if err != nil {
			return nil, err
		}
		items = append(items, app.InboxItem{
			Key:       task.Key{Repo: repo, Number: h.Number},
			Kind:      s.kind,
			Title:     h.Title,
			URL:       h.URL,
			State:     strings.ToLower(h.State),
			UpdatedAt: h.UpdatedAt,
			Reasons:   []task.InboxReason{s.reason},
		})
	}
	return items, nil
}

type restIssue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	HTMLURL     string    `json:"html_url"`
	State       string    `json:"state"`
	UpdatedAt   time.Time `json:"updated_at"`
	PullRequest *struct {
		MergedAt *time.Time `json:"merged_at"`
	} `json:"pull_request"`
}

// GetItem uses the REST issues endpoint, which serves both issues and pull requests.
func (c Client) GetItem(ctx context.Context, key task.Key) (app.InboxItem, error) {
	out, err := c.Run(ctx, "", "api", fmt.Sprintf("repos/%s/issues/%d", key.Repo, key.Number))
	if err != nil {
		return app.InboxItem{}, err
	}
	var r restIssue
	if err := json.Unmarshal(out, &r); err != nil {
		return app.InboxItem{}, fmt.Errorf("parse %s#%d: %w", key.Repo, key.Number, err)
	}
	item := app.InboxItem{
		Key:       key,
		Kind:      task.Issue,
		Title:     r.Title,
		URL:       r.HTMLURL,
		State:     r.State,
		UpdatedAt: r.UpdatedAt,
		Reasons:   []task.InboxReason{task.Tracked},
	}
	if r.PullRequest != nil {
		item.Kind = task.PullRequest
		if r.PullRequest.MergedAt != nil {
			item.State = "merged"
		}
	}
	return item, nil
}

func (c Client) GetIssue(ctx context.Context, key task.Key) (app.Issue, error) {
	out, err := c.Run(ctx, "", "issue", "view", fmt.Sprint(key.Number), "--repo", string(key.Repo), "--json", "number,title,body,url,state,updatedAt")
	if err != nil {
		return app.Issue{}, err
	}
	var r struct {
		Title     string    `json:"title"`
		Body      string    `json:"body"`
		URL       string    `json:"url"`
		State     string    `json:"state"`
		UpdatedAt time.Time `json:"updatedAt"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return app.Issue{}, fmt.Errorf("parse issue %s#%d: %w", key.Repo, key.Number, err)
	}
	return app.Issue{Key: key, Title: r.Title, Body: r.Body, URL: r.URL, State: strings.ToLower(r.State), UpdatedAt: r.UpdatedAt}, nil
}

func (c Client) RepoAt(ctx context.Context, dir string) (app.RepoInfo, error) {
	out, err := c.Run(ctx, dir, "repo", "view", "--json", "nameWithOwner,defaultBranchRef")
	if err != nil {
		return app.RepoInfo{}, err
	}
	var r struct {
		NameWithOwner    string `json:"nameWithOwner"`
		DefaultBranchRef struct {
			Name string `json:"name"`
		} `json:"defaultBranchRef"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return app.RepoInfo{}, fmt.Errorf("parse gh repo view: %w", err)
	}
	repo, err := task.ParseRepo(r.NameWithOwner)
	if err != nil {
		return app.RepoInfo{}, err
	}
	if r.DefaultBranchRef.Name == "" {
		return app.RepoInfo{}, fmt.Errorf("%s has no default branch", repo)
	}
	return app.RepoInfo{Repo: repo, DefaultBranch: r.DefaultBranchRef.Name}, nil
}
