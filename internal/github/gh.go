// Package github reads GitHub state through the gh CLI, which is already authenticated.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

// Runner executes gh with args in dir ("" for the current directory) and returns
// stdout, also when gh exits non-zero. Tests substitute recorded output.
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
		return stdout.Bytes(), fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
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
	out, err := c.Run(ctx, "", "issue", "view", fmt.Sprint(key.Number), "--repo", string(key.Repo), "--json", "number,title,body,labels,url,state,updatedAt")
	if err != nil {
		return app.Issue{}, err
	}
	var r struct {
		Title  string `json:"title"`
		Body   string `json:"body"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		URL       string    `json:"url"`
		State     string    `json:"state"`
		UpdatedAt time.Time `json:"updatedAt"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return app.Issue{}, fmt.Errorf("parse issue %s#%d: %w", key.Repo, key.Number, err)
	}
	labels := make([]string, len(r.Labels))
	for i, l := range r.Labels {
		labels[i] = l.Name
	}
	return app.Issue{Key: key, Title: r.Title, Body: r.Body, Labels: labels, URL: r.URL, State: strings.ToLower(r.State), UpdatedAt: r.UpdatedAt}, nil
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

const prFields = "number,url,state,headRefName,baseRefName,isCrossRepository"

type prJSON struct {
	Number            int    `json:"number"`
	URL               string `json:"url"`
	State             string `json:"state"`
	HeadRefName       string `json:"headRefName"`
	BaseRefName       string `json:"baseRefName"`
	IsCrossRepository bool   `json:"isCrossRepository"`
}

func (p prJSON) toPR(repo task.Repo) app.PullRequest {
	return app.PullRequest{
		Key:               task.Key{Repo: repo, Number: p.Number},
		URL:               p.URL,
		State:             strings.ToLower(p.State),
		HeadRefName:       p.HeadRefName,
		BaseRefName:       p.BaseRefName,
		IsCrossRepository: p.IsCrossRepository,
	}
}

func (c Client) GetPullRequest(ctx context.Context, key task.Key) (app.PullRequest, error) {
	out, err := c.Run(ctx, "", "pr", "view", fmt.Sprint(key.Number), "--repo", string(key.Repo), "--json", prFields)
	if err != nil {
		return app.PullRequest{}, err
	}
	var p prJSON
	if err := json.Unmarshal(out, &p); err != nil {
		return app.PullRequest{}, fmt.Errorf("parse PR %s#%d: %w", key.Repo, key.Number, err)
	}
	return p.toPR(key.Repo), nil
}

func (c Client) OpenPullRequestForBranch(ctx context.Context, repo task.Repo, branch string) (*app.PullRequest, error) {
	out, err := c.Run(ctx, "", "pr", "list", "--repo", string(repo), "--head", branch, "--state", "open", "--limit", "2", "--json", prFields)
	if err != nil {
		return nil, err
	}
	var prs []prJSON
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("parse PR list for %s: %w", branch, err)
	}
	for _, p := range prs {
		if p.HeadRefName == branch && !p.IsCrossRepository {
			pr := p.toPR(repo)
			return &pr, nil
		}
	}
	return nil, nil
}

// CIStatus summarizes every check on the PR's head. gh exits 8 while checks are
// pending and 1 when one failed, so the exit code is ignored whenever JSON came
// back; a PR with no checks at all is an error from gh and becomes CINone.
func (c Client) CIStatus(ctx context.Context, key task.Key) (task.CIStatus, error) {
	out, err := c.Run(ctx, "", "pr", "checks", fmt.Sprint(key.Number), "--repo", string(key.Repo), "--json", "name,bucket,link")
	if err != nil && strings.Contains(err.Error(), "no checks reported") {
		return task.CIStatus{State: task.CINone}, nil
	}
	if err != nil && len(bytes.TrimSpace(out)) == 0 {
		return task.CIStatus{}, err
	}
	var checks []checkJSON
	if jsonErr := json.Unmarshal(out, &checks); jsonErr != nil {
		return task.CIStatus{}, fmt.Errorf("parse checks for %s#%d: %w", key.Repo, key.Number, errors.Join(jsonErr, err))
	}
	return summarizeChecks(checks), nil
}

type checkJSON struct {
	Name   string `json:"name"`
	Bucket string `json:"bucket"`
	Link   string `json:"link"`
}

// summarizeChecks ranks failing over pending over passing. Cancelled checks fail the
// gate; skipped ones pass it.
func summarizeChecks(checks []checkJSON) task.CIStatus {
	st := task.CIStatus{State: task.CINone}
	for _, ch := range checks {
		switch ch.Bucket {
		case "fail", "cancel":
			st.State = task.CIFailing
			st.Failing = append(st.Failing, task.CheckRun{Name: ch.Name, Link: ch.Link})
		case "pending":
			if st.State != task.CIFailing {
				st.State = task.CIPending
			}
		default:
			if st.State == task.CINone {
				st.State = task.CIPassing
			}
		}
	}
	return st
}
