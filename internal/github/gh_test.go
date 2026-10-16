package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

func fixtures(t *testing.T) Runner {
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		cmd := strings.Join(args, " ")
		for flag, file := range map[string]string{
			"--assignee=@me":                            "assigned",
			"issues --mentions=@me":                     "mentioned",
			"prs --mentions=@me":                        "mentioned_prs",
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
	search, err := Client{Run: fixtures(t)}.InboxItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	items := search.Items
	if len(items) != 4 || len(search.Truncated) != 0 {
		t.Fatalf("want 3 merged items, got %d: %+v", len(items), items)
	}
	first := items[0]
	if first.Key != (task.Key{Repo: "ridezoomo/armada-backend", Number: 825}) ||
		!slices.Equal(first.Reasons, []task.InboxReason{task.Assigned, task.Mentioned}) ||
		!slices.Equal(first.Labels, []string{"bug", "triaged"}) {
		t.Errorf("825 = %+v", first)
	}
	if pr := items[2]; pr.Kind != task.PullRequest || pr.Key.Number != 930 || pr.Reasons[0] != task.Mentioned {
		t.Errorf("PR mention = %+v", pr)
	}
	if pr := items[3]; pr.Kind != task.PullRequest || pr.Reasons[0] != task.ReviewRequested {
		t.Errorf("review request = %+v", pr)
	}
}

func TestInboxItemsReportsSearchesAtTheLimit(t *testing.T) {
	full := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if !slices.Contains(args, "--assignee=@me") {
			return []byte("[]"), nil
		}
		hits := make([]string, searchLimit)
		for i := range hits {
			hits[i] = fmt.Sprintf(`{"number":%d,"title":"t","url":"u","state":"OPEN","updatedAt":"2026-09-24T10:00:00Z","repository":{"nameWithOwner":"a/b"}}`, i+1)
		}
		return []byte("[" + strings.Join(hits, ",") + "]"), nil
	}
	search, err := Client{Run: full}.InboxItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(search.Truncated, []string{"issues assigned to you"}) {
		t.Fatalf("truncated = %q", search.Truncated)
	}
}

func TestGetItemDetectsMergedPR(t *testing.T) {
	item, err := Client{Run: fixtures(t)}.GetItem(context.Background(), task.Key{Repo: "ridezoomo/armada-backend", Number: 912})
	if err != nil {
		t.Fatal(err)
	}
	if item.Kind != task.PullRequest || item.State != "merged" || item.UpdatedAt.IsZero() ||
		!slices.Equal(item.Labels, []string{"triaged"}) {
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
	if err != nil || pr.State != "open" || pr.HeadRefName != "agent/825-provider-retries" || pr.HeadSHA != "c0ffee1c0ffee1c0ffee1c0ffee1c0ffee1c0ffe" || pr.BaseRefName != "main" || pr.URL == "" {
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

func TestPullRequestMergeable(t *testing.T) {
	for raw, want := range map[string]string{"MERGEABLE": "mergeable", "CONFLICTING": "conflicting", "UNKNOWN": "unknown", "": "unknown", "OTHER": "unknown"} {
		c := Client{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if !strings.Contains(strings.Join(args, " "), "mergeable") {
				t.Errorf("gh call does not ask for mergeable: %v", args)
			}
			return []byte(fmt.Sprintf(`{"number":5,"state":"OPEN","mergeable":%q}`, raw)), nil
		}}
		pr, err := c.GetPullRequest(context.Background(), task.Key{Repo: "a/b", Number: 5})
		if err != nil || pr.Mergeable != want {
			t.Errorf("%q: mergeable = %q, %v; want %q", raw, pr.Mergeable, err, want)
		}
	}
}

func TestPullRequestBody(t *testing.T) {
	c := Client{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if !strings.Contains(strings.Join(args, " "), "body") {
			t.Errorf("gh call does not ask for body: %v", args)
		}
		return []byte(`{"number":5,"state":"OPEN","body":"Retry 5xx only.\n\nCloses #4"}`), nil
	}}
	pr, err := c.GetPullRequest(context.Background(), task.Key{Repo: "a/b", Number: 5})
	if err != nil || pr.Body != "Retry 5xx only.\n\nCloses #4" {
		t.Errorf("body = %q, %v", pr.Body, err)
	}
}

func TestPullRequestReadsTitleAndCloseTimes(t *testing.T) {
	c := Client{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if call := strings.Join(args, " "); !strings.Contains(call, "title") || !strings.Contains(call, "closedAt") || !strings.Contains(call, "mergedAt") {
			t.Errorf("gh call does not ask for the title and close times: %v", args)
		}
		return []byte(`{"number":5,"state":"MERGED","title":"Retry 5xx","closedAt":"2026-09-25T08:00:00Z","mergedAt":"2026-09-25T08:00:00Z"}`), nil
	}}
	pr, err := c.GetPullRequest(context.Background(), task.Key{Repo: "a/b", Number: 5})
	want := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)
	if err != nil || pr.Title != "Retry 5xx" || pr.State != "merged" || !pr.ClosedAt.Equal(want) || !pr.MergedAt.Equal(want) {
		t.Errorf("pr = %+v, %v", pr, err)
	}
}

func TestBranchHead(t *testing.T) {
	var got string
	c := Client{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		got = strings.Join(args, " ")
		return []byte("abc123\n"), nil
	}}
	sha, err := c.BranchHead(context.Background(), "a/b", "release/1.2")
	if err != nil || sha != "abc123" {
		t.Fatalf("sha = %q, %v", sha, err)
	}
	if got != "api repos/a/b/git/ref/heads/release/1.2 --jq .object.sha" {
		t.Errorf("gh call = %q", got)
	}
	empty := Client{Run: func(context.Context, string, ...string) ([]byte, error) { return []byte("\n"), nil }}
	if _, err := empty.BranchHead(context.Background(), "a/b", "main"); err == nil {
		t.Error("an empty answer must be an error")
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

func TestGetItemReportsMissingItemsAsGone(t *testing.T) {
	for _, tc := range []struct {
		stderr string
		gone   bool
	}{
		{"gh: Not Found (HTTP 404)", true},
		{"gh: This issue was deleted (HTTP 410)", true},
		{"gh: Server Error (HTTP 500)", false},
	} {
		run := func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`{"message":"x"}`), fmt.Errorf("gh api repos/a/b/issues/5: exit status 1: %s", tc.stderr)
		}
		_, err := Client{Run: run}.GetItem(context.Background(), task.Key{Repo: "a/b", Number: 5})
		if errors.Is(err, app.ErrGone) != tc.gone {
			t.Errorf("%s: gone = %t, want %t (%v)", tc.stderr, !tc.gone, tc.gone, err)
		}
	}
}

func TestPostIssueCommentCreatesOrEdits(t *testing.T) {
	var calls [][]string
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, args)
		if strings.Contains(strings.Join(args, " "), "comments/77") {
			return []byte(`{"message":"Not Found"}`), errors.New("gh: Not Found (HTTP 404)")
		}
		return []byte(`{"id":3301,"html_url":"https://github.com/a/b/issues/7#issuecomment-3301","body":"@team ## PRD"}`), nil
	}
	c := Client{Run: run}
	key := task.Key{Repo: "a/b", Number: 7}
	got, err := c.PostIssueComment(context.Background(), key, 0, "@team ## PRD")
	if err != nil || got.ID != 3301 || got.URL != "https://github.com/a/b/issues/7#issuecomment-3301" {
		t.Fatalf("create = %+v, %v", got, err)
	}
	if want := []string{"api", "--method", "POST", "repos/a/b/issues/7/comments", "-f", "body=@team ## PRD"}; !slices.Equal(calls[0], want) {
		t.Errorf("create argv = %q, want %q", calls[0], want)
	}
	if _, err := c.PostIssueComment(context.Background(), key, 3301, "new"); err != nil || !slices.Equal(calls[1][:4], []string{"api", "--method", "PATCH", "repos/a/b/issues/comments/3301"}) {
		t.Errorf("edit argv = %q, %v", calls[1], err)
	}
	if _, err := c.PostIssueComment(context.Background(), key, 77, "x"); !errors.Is(err, app.ErrGone) {
		t.Errorf("editing a deleted comment err = %v", err)
	}
	if got, err := c.GetIssueComment(context.Background(), "a/b", 3301); err != nil || got.Body != "@team ## PRD" {
		t.Errorf("get = %+v, %v", got, err)
	}
}

func TestPullRequestCommentsReadsEveryThread(t *testing.T) {
	var mu sync.Mutex
	var calls [][]string
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		mu.Lock()
		calls = append(calls, args)
		mu.Unlock()
		switch path := args[1]; {
		case strings.HasSuffix(path, "/issues/912/comments"):
			return os.ReadFile("testdata/pr_issue_comments.json")
		case strings.HasSuffix(path, "/pulls/912/comments"):
			return os.ReadFile("testdata/pr_review_comments.json")
		case strings.HasSuffix(path, "/pulls/912/reviews"):
			return os.ReadFile("testdata/pr_reviews.json")
		}
		return nil, fmt.Errorf("unexpected gh call: %q", args)
	}
	got, err := Client{Run: run}.PullRequestComments(context.Background(), task.Key{Repo: "ridezoomo/armada-backend", Number: 912})
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range calls {
		if !slices.Contains(args, "--paginate") || !slices.Contains(args, "--slurp") {
			t.Errorf("argv %q reads one page only", args)
		}
	}
	// The approval with no body is dropped; it has nothing to answer.
	if len(got) != 5 {
		t.Fatalf("got %d comments, want 5: %+v", len(got), got)
	}
	want := []app.PRComment{
		{Kind: task.IssueComment, ID: 4401, Author: "brymartinez", Body: "Looks good, ship it.",
			URL: "https://github.com/ridezoomo/armada-backend/pull/912#issuecomment-4401", UpdatedAt: time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)},
		{Kind: task.IssueComment, ID: 4402, Author: "brymartinez", Body: "<!-- foreman:reply -->\nAnswered: the retry is capped.",
			URL: "https://github.com/ridezoomo/armada-backend/pull/912#issuecomment-4402", UpdatedAt: time.Date(2026, 9, 25, 9, 10, 0, 0, time.UTC)},
		{Kind: task.ReviewComment, ID: 5501, Author: "brymartinez", Body: "This retries 4xx too.",
			URL: "https://github.com/ridezoomo/armada-backend/pull/912#discussion_r5501", UpdatedAt: time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC),
			Path: "internal/retry/retry.go", Line: 42},
		{Kind: task.ReviewComment, ID: 5502, Author: "dependabot[bot]", Body: "Same here.",
			URL: "https://github.com/ridezoomo/armada-backend/pull/912#discussion_r5502", UpdatedAt: time.Date(2026, 9, 25, 10, 5, 0, 0, time.UTC),
			Path: "internal/retry/retry.go", Line: 50, InReplyTo: 5501},
		{Kind: task.ReviewSummary, ID: 6601, Author: "brymartinez", Body: "Two things to fix before this goes in.",
			URL: "https://github.com/ridezoomo/armada-backend/pull/912#pullrequestreview-6601", UpdatedAt: time.Date(2026, 9, 25, 10, 10, 0, 0, time.UTC)},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("comment %d =\n %+v\nwant %+v", i, got[i], w)
		}
	}
}

func TestPullRequestCommentsReportsAFailedThread(t *testing.T) {
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.HasSuffix(args[1], "/reviews") {
			return nil, errors.New("gh: Server Error (HTTP 500)")
		}
		return []byte("[[]]"), nil
	}
	if _, err := (Client{Run: run}).PullRequestComments(context.Background(), task.Key{Repo: "a/b", Number: 1}); err == nil {
		t.Fatal("a failed thread must not look like an empty conversation")
	}
}

func TestSearchParsesCreatedAt(t *testing.T) {
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if !slices.Contains(args, "--assignee=@me") {
			return []byte("[]"), nil
		}
		if !slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "--json=") && strings.Contains(a, "createdAt") }) {
			t.Errorf("search does not request createdAt: %q", args)
		}
		return []byte(`[{"number":7,"title":"t","url":"u","state":"OPEN","createdAt":"2026-09-01T08:00:00Z","updatedAt":"2026-09-24T10:00:00Z","repository":{"nameWithOwner":"a/b"}}]`), nil
	}
	search, err := Client{Run: run}.InboxItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	if len(search.Items) != 1 || !search.Items[0].CreatedAt.Equal(want) {
		t.Fatalf("items = %+v, want created at %s", search.Items, want)
	}
}

func TestGetItemParsesCreatedAt(t *testing.T) {
	run := func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"number":7,"title":"t","html_url":"u","state":"open","created_at":"2026-09-01T08:00:00Z","updated_at":"2026-09-24T10:00:00Z"}`), nil
	}
	item, err := Client{Run: run}.GetItem(context.Background(), task.Key{Repo: "a/b", Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC); !item.CreatedAt.Equal(want) {
		t.Fatalf("created at = %s, want %s", item.CreatedAt, want)
	}
}

func TestIssueCommentsReadsEveryPage(t *testing.T) {
	var calls [][]string
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, args)
		if len(args) > 1 && args[1] == "repos/ridezoomo/armada-backend/issues/912/comments" {
			return os.ReadFile("testdata/pr_issue_comments.json")
		}
		return nil, fmt.Errorf("unexpected gh call: %q", args)
	}
	got, err := Client{Run: run}.IssueComments(context.Background(), task.Key{Repo: "ridezoomo/armada-backend", Number: 912})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !slices.Contains(calls[0], "--paginate") || !slices.Contains(calls[0], "--slurp") {
		t.Errorf("argv = %q", calls)
	}
	if len(got) != 2 || got[0].ID != 4401 || got[0].Body != "Looks good, ship it." || got[1].ID != 4402 {
		t.Fatalf("got %+v", got)
	}
}

func TestActiveAccountReadsTheLoginAndID(t *testing.T) {
	var got string
	c := Client{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		got = strings.Join(args, " ")
		return []byte(`{"login":"octo-work","id":42}` + "\n"), nil
	}}
	acct, err := c.ActiveAccount(context.Background())
	if err != nil || acct != (app.Account{Login: "octo-work", ID: 42}) {
		t.Fatalf("account = %+v, %v", acct, err)
	}
	if got != "api user --jq {login,id}" {
		t.Errorf("gh call = %q", got)
	}
	if acct.Email() != "42+octo-work@users.noreply.github.com" {
		t.Errorf("email = %q", acct.Email())
	}
}

func TestActiveAccountRejectsAnEmptyLogin(t *testing.T) {
	c := Client{Run: func(context.Context, string, ...string) ([]byte, error) { return []byte(`{"login":"","id":0}`), nil }}
	if _, err := c.ActiveAccount(context.Background()); err == nil {
		t.Error("an empty login must be an error")
	}
}

func TestActiveAccountReportsAGHFailure(t *testing.T) {
	c := Client{Run: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("not logged in") }}
	if _, err := c.ActiveAccount(context.Background()); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("err = %v", err)
	}
}

func TestPullRequestsForBranchListsEveryStateFromThisRepository(t *testing.T) {
	var asked string
	c := Client{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		asked = strings.Join(args, " ")
		return []byte(`[
			{"number":5,"state":"MERGED","headRefName":"feat/3-x"},
			{"number":6,"state":"OPEN","headRefName":"feat/3-x","isCrossRepository":true},
			{"number":7,"state":"CLOSED","headRefName":"feat/3-x"},
			{"number":8,"state":"OPEN","headRefName":"feat/3-x-other"}
		]`), nil
	}}
	prs, err := c.PullRequestsForBranch(context.Background(), "a/b", "feat/3-x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(asked, "--state all") || !strings.Contains(asked, "--head feat/3-x") {
		t.Errorf("gh call %q does not list every state of the branch", asked)
	}
	if len(prs) != 2 || prs[0].Key.Number != 5 || prs[0].State != "merged" || prs[1].Key.Number != 7 {
		t.Fatalf("prs = %+v", prs)
	}
}

func TestPullRequestsForBranchReturnsTheGhError(t *testing.T) {
	c := Client{Run: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("HTTP 502") }}
	if _, err := c.PullRequestsForBranch(context.Background(), "a/b", "feat/3-x"); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v", err)
	}
}
