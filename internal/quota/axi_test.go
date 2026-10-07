package quota

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

func printing(out string, err error) agent.Exec {
	return func(_ context.Context, c agent.Command) error {
		fmt.Fprint(c.Stdout, out)
		return err
	}
}

func axiReport(claude, codex string) string {
	return fmt.Sprintf(`{"schemaVersion":5,"providers":[%s,%s]}`, claude, codex)
}

func provider(name, scope, status string, percent int) string {
	return fmt.Sprintf(`{"provider":%q,"state":{"status":"fresh","stale":false},"quotaSemantics":{"status":%q,"effectiveAvailability":[{"scope":%q,"status":%q,"effectivePercentRemaining":%d}]}}`, name, status, scope, status, percent)
}

func TestAxiReadsTheRealReport(t *testing.T) {
	raw, err := os.ReadFile("testdata/available.json")
	if err != nil {
		t.Fatal(err)
	}
	var got agent.Command
	exec := func(_ context.Context, c agent.Command) error {
		got = c
		_, err := c.Stdout.Write(raw)
		return err
	}
	q, err := Axi{Command: "quota-axi", Exec: exec}.Quota(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "quota-axi" || !slices.Equal(got.Args, []string{"--provider", "claude,codex", "--json"}) {
		t.Errorf("command = %s %q", got.Name, got.Args)
	}
	if q[task.Claude] != (app.Quota{Status: app.QuotaAvailable, Percent: 54}) || q[task.Codex] != (app.Quota{Status: app.QuotaAvailable, Percent: 64}) {
		t.Errorf("quota = %+v", q)
	}
}

func TestAxiExhaustedWhenAllModelsZero(t *testing.T) {
	out := axiReport(provider("claude", "all_models", "known", 0), provider("codex", "all_models", "known", 30))
	q, err := Axi{Exec: printing(out, nil)}.Quota(context.Background())
	if err != nil || q[task.Claude].Status != app.QuotaExhausted || q[task.Codex].Status != app.QuotaAvailable {
		t.Fatalf("quota = %+v, %v", q, err)
	}
}

func TestAxiReportsConfirmedNotSetUpProvider(t *testing.T) {
	out := axiReport(`{"provider":"claude","notSetUp":true,"state":{"stale":false}}`, provider("codex", "all_models", "known", 30))
	q, err := Axi{Exec: printing(out, errors.New("exit status 1"))}.Quota(context.Background())
	if err != nil || q[task.Claude].Status != app.QuotaNotSetUp || q[task.Codex].Status != app.QuotaAvailable {
		t.Fatalf("quota = %+v, %v", q, err)
	}
}

func TestAxiKeepsMissingAndStaleProviderUnknown(t *testing.T) {
	out := axiReport(`{"provider":"claude","notSetUp":true,"state":{"stale":true}}`, `{"provider":"codex","state":{"stale":false,"status":"attention"}}`)
	q, err := Axi{Exec: printing(out, nil)}.Quota(context.Background())
	if err != nil || q[task.Claude].Status != app.QuotaUnknown || q[task.Codex].Status != app.QuotaUnknown {
		t.Fatalf("quota = %+v, %v", q, err)
	}
	q, err = Axi{Exec: printing(`{"providers":[{"provider":"claude","state":{"stale":false}}]}`, nil)}.Quota(context.Background())
	if err != nil || q[task.Codex].Status != app.QuotaUnknown {
		t.Fatalf("missing provider row: quota = %+v, %v", q, err)
	}
}

func TestAxiUnknownWhenScopeOrStatusUnknown(t *testing.T) {
	out := axiReport(provider("claude", "all_models", "unknown", 0), provider("codex", "model:x", "known", 0))
	q, err := Axi{Exec: printing(out, nil)}.Quota(context.Background())
	if err != nil || q[task.Claude].Status != app.QuotaUnknown || q[task.Codex].Status != app.QuotaUnknown {
		t.Fatalf("quota = %+v, %v; a model-only or unknown reading is not exhaustion", q, err)
	}
}

func TestAxiUnknownWhenReadingIsStale(t *testing.T) {
	stale := strings.Replace(provider("claude", "all_models", "known", 0), `"stale":false`, `"stale":true`, 1)
	q, _ := Axi{Exec: printing(axiReport(stale, provider("codex", "all_models", "known", 5)), nil)}.Quota(context.Background())
	if q[task.Claude].Status != app.QuotaUnknown {
		t.Fatalf("quota = %+v; a stale reading does not confirm exhaustion", q)
	}
}

func TestAxiParsesReportOnExitCodeOne(t *testing.T) {
	out := axiReport(provider("claude", "all_models", "known", 0), `{"provider":"codex","error":"not logged in"}`)
	q, err := Axi{Exec: printing(out, errors.New("exit status 1"))}.Quota(context.Background())
	if err != nil || q[task.Claude].Status != app.QuotaExhausted || q[task.Codex].Status != app.QuotaUnknown {
		t.Fatalf("quota = %+v, %v", q, err)
	}
}

func TestAxiMissingExecutableIsUnknown(t *testing.T) {
	q, err := Axi{Exec: printing("", &exec.Error{Name: "quota-axi", Err: exec.ErrNotFound})}.Quota(context.Background())
	if len(q) != 0 || err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("quota = %+v, %v", q, err)
	}
}

func TestAxiGarbageOutputIsUnknown(t *testing.T) {
	q, err := Axi{Exec: printing("usage: quota-axi", errors.New("exit status 2"))}.Quota(context.Background())
	if len(q) != 0 || err == nil {
		t.Fatalf("quota = %+v, %v", q, err)
	}
}

func TestAxiTimeoutIsUnknown(t *testing.T) {
	slow := func(ctx context.Context, _ agent.Command) error {
		<-ctx.Done()
		return ctx.Err()
	}
	start := time.Now()
	q, err := Axi{Exec: slow, Timeout: 10 * time.Millisecond}.Quota(context.Background())
	if len(q) != 0 || err == nil || time.Since(start) > time.Second {
		t.Fatalf("quota = %+v, %v", q, err)
	}
}
