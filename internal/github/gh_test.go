package github

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

func fixtures(t *testing.T) Runner {
	return func(_ context.Context, args ...string) ([]byte, error) {
		cmd := strings.Join(args, " ")
		for flag, file := range map[string]string{
			"--assignee=@me":                            "assigned",
			"--mentions=@me":                            "mentioned",
			"--review-requested=@me":                    "review_requested",
			"repos/ridezoomo/armada-backend/issues/912": "merged_pr",
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
	failing := func(context.Context, ...string) ([]byte, error) { return nil, fmt.Errorf("gh: not logged in") }
	if _, err := (Client{Run: failing}).InboxItems(context.Background()); err == nil {
		t.Fatal("want error")
	}
}
