package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func fixtures(t *testing.T) Runner {
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		cmd := strings.Join(args, " ")
		for flag, file := range map[string]string{
			"--assignee=@me":                            "assigned",
			"--mentions=@me":                            "mentioned",
			"--review-requested=@me":                    "review_requested",
			"repos/ridezoomo/armada-backend/issues/912": "merged_pr",
			"issue view 825":                            "issue",
			"repo view":                                 "repo",
			"pr view 912":                               "pr",
			"--head agent/825-provider-retries":         "pr_list",
			"--head agent/1-none":                       "empty_list",
		} {
			if strings.Contains(cmd, flag) {
				if strings.HasPrefix(cmd, "search") && !strings.Contains(cmd, "--state=open") {
					t.Errorf("search without --state=open: %s", cmd)
				}
				return os.ReadFile("testdata/" + file + ".json")
			}
		}
		return nil, fmt.Errorf("unexpected gh call: %s", cmd)
	}
}

func TestInboxItemsMergesReasons(t *testing.T) {
	items, err := Client{Run: fixtures(t)}.InboxItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("want 3 merged items, got %d: %+v", len(items), items)
	}
	first := items[0]
	if first.Key != (task.Key{Repo: "ridezoomo/armada-backend", Number: 825}) ||
		!slices.Equal(first.Reasons, []task.InboxReason{task.Assigned, task.Mentioned}) {
		t.Errorf("825 = %+v", first)
	}
	if pr := items[2]; pr.Kind != task.PullRequest || pr.Reasons[0] != task.ReviewRequested {
		t.Errorf("review request = %+v", pr)
	}
}

func TestGetItemDetectsMergedPR(t *testing.T) {
	item, err := Client{Run: fixtures(t)}.GetItem(context.Background(), task.Key{Repo: "ridezoomo/armada-backend", Number: 912})
	if err != nil {
		t.Fatal(err)
	}
	if item.Kind != task.PullRequest || item.State != "merged" || item.UpdatedAt.IsZero() {
		t.Errorf("got %+v", item)
	}
}

func TestSearchErrorPropagates(t *testing.T) {
	failing := func(context.Context, string, ...string) ([]byte, error) { return nil, fmt.Errorf("gh: not logged in") }
	if _, err := (Client{Run: failing}).InboxItems(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

func TestGetIssueAndRepoAt(t *testing.T) {
	c := Client{Run: fixtures(t)}
	issue, err := c.GetIssue(context.Background(), task.Key{Repo: "ridezoomo/armada-backend", Number: 825})
	if err != nil || issue.Title != "Provider retries" || !strings.Contains(issue.Body, "5xx") || issue.State != "open" || len(issue.Labels) != 1 || issue.Labels[0] != "bug" {
		t.Fatalf("issue = %+v, %v", issue, err)
	}
	info, err := c.RepoAt(context.Background(), "/some/checkout")
	if err != nil || info.Repo != "ridezoomo/armada-backend" || info.DefaultBranch != "main" {
		t.Fatalf("repo = %+v, %v", info, err)
	}
}

func TestEnsureCheckoutClonesMissingRepository(t *testing.T) {
	var calls []string
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if strings.HasPrefix(strings.Join(args, " "), "repo clone") {
			return nil, nil
		}
		return fixtures(t)(context.Background(), "", args...)
	}
	path := filepath.Join(t.TempDir(), "checkout")
	info, err := (Client{Run: run}).Ensure(context.Background(), "ridezoomo/armada-backend", path)
	if err != nil || info.Repo != "ridezoomo/armada-backend" {
		t.Fatalf("info = %+v, err = %v", info, err)
	}
	if len(calls) == 0 || calls[0] != "repo clone ridezoomo/armada-backend "+path {
		t.Fatalf("clone calls = %v", calls)
	}
}

func TestPullRequests(t *testing.T) {
	c := Client{Run: fixtures(t)}
	pr, err := c.GetPullRequest(context.Background(), task.Key{Repo: "ridezoomo/armada-backend", Number: 912})
	if err != nil || pr.State != "open" || pr.HeadRefName != "agent/825-provider-retries" || pr.BaseRefName != "main" || pr.URL == "" {
		t.Fatalf("pr = %+v, %v", pr, err)
	}
	found, err := c.OpenPullRequestForBranch(context.Background(), "ridezoomo/armada-backend", "agent/825-provider-retries")
	if err != nil || found == nil || found.Key.Number != 912 {
		t.Fatalf("by branch = %+v, %v", found, err)
	}
	none, err := c.OpenPullRequestForBranch(context.Background(), "ridezoomo/armada-backend", "agent/1-none")
	if err != nil || none != nil {
		t.Fatalf("empty = %+v, %v", none, err)
	}
}

func TestCIStatus(t *testing.T) {
	key := task.Key{Repo: "ridezoomo/armada-backend", Number: 912}
	for _, tc := range []struct {
		name    string
		out     string
		err     error
		want    task.CIState
		failing []string
	}{
		{"all pass or skipped", `[{"name":"test","bucket":"pass"},{"name":"deploy","bucket":"skipping"}]`, nil, task.CIPassing, nil},
		{"pending exits 8", `[{"name":"test","bucket":"pass"},{"name":"lint","bucket":"pending"}]`, errors.New("exit status 8"), task.CIPending, nil},
		{"failure wins over pending", `[{"name":"test","bucket":"fail","link":"https://ci/1"},{"name":"lint","bucket":"pending"},{"name":"e2e","bucket":"cancel"}]`, errors.New("exit status 1"), task.CIFailing, []string{"test", "e2e"}},
		{"empty list", `[]`, nil, task.CINone, nil},
		{"no checks at all", ``, errors.New("gh pr checks: exit status 1: no checks reported on the 'agent/825' branch"), task.CINone, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Client{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if got := strings.Join(args, " "); got != "pr checks 912 --repo ridezoomo/armada-backend --json name,bucket,link" {
					t.Errorf("args = %s", got)
				}
				return []byte(tc.out), tc.err
			}}
			st, err := c.CIStatus(context.Background(), key)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, f := range st.Failing {
				names = append(names, f.Name)
			}
			if st.State != tc.want || !slices.Equal(names, tc.failing) {
				t.Errorf("got %s %v, want %s %v", st.State, names, tc.want, tc.failing)
			}
		})
	}
	c := Client{Run: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("HTTP 502") }}
	if _, err := c.CIStatus(context.Background(), key); err == nil {
		t.Error("a gh failure without output must be an error")
	}
}
