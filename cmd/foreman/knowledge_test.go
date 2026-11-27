package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/cli"
	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/workspace"
)

// openTestState opens a fresh database under a temp directory.
func openTestState(t *testing.T) (state, config.Config) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{Database: filepath.Join(dir, "foreman.db"), TasksDir: filepath.Join(dir, "tasks"), TriageDir: filepath.Join(dir, "triage"), KnowledgeDir: filepath.Join(dir, "knowledge")}
	st, err := openState(cfg, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st, cfg
}

func TestKnowledgeExportThenIngestRestoresOnAnotherMachine(t *testing.T) {
	src, srcCfg := openTestState(t)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	err := src.Knowledge.Apply("acme/widgets", knowledge.Evaluation{
		Task: "1", TaskAt: at, At: at, Fingerprint: "fp", Assessment: "fine", MaintainerRefs: []string{"D1"},
		Proposals: []knowledge.Proposal{{Kind: knowledge.Convention, Text: "Wrap errors with the operation name.", Scope: knowledge.Reusable, Source: knowledge.Maintainer, Cites: []string{"D1"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "knowledge.md")
	var out, errs bytes.Buffer
	if code := runKnowledge(context.Background(), srcCfg, src, cli.Command{Name: cli.KnowledgeExport, Repo: "acme/widgets", File: file}, &out, &errs); code != 0 {
		t.Fatalf("export exit %d: %s", code, errs.String())
	}

	dst, dstCfg := openTestState(t)
	out.Reset()
	if code := runKnowledge(context.Background(), dstCfg, dst, cli.Command{Name: cli.KnowledgeIngest, File: file, Repo: "acme/gadgets"}, &out, &errs); code != 1 {
		t.Fatalf("ingest into another repository exit %d, want 1", code)
	}
	if code := runKnowledge(context.Background(), dstCfg, dst, cli.Command{Name: cli.KnowledgeIngest, File: file}, &out, &errs); code != 0 {
		t.Fatalf("ingest exit %d: %s", code, errs.String())
	}
	if !strings.Contains(out.String(), "Restored acme/widgets: 1 learnings (1 active) and 1 evaluations.") {
		t.Fatalf("stdout = %q", out.String())
	}
	ls, err := dst.Knowledge.Learnings("acme/widgets")
	if err != nil || len(ls) != 1 || !strings.Contains(ls[0].Text, "Wrap errors with the operation name.") {
		t.Fatalf("learnings = %+v, %v", ls, err)
	}
}

func TestKnowledgeReviewAndApproveCommandsUseTheExactSnapshot(t *testing.T) {
	st, cfg := openTestState(t)
	ctx := context.Background()
	host := workspace.KnowledgePublish{Repo: "acme/widgets", Snapshot: knowledge.Snapshot{
		Format: knowledge.FormatVersion, Repo: "acme/widgets", NextID: 2,
		Learnings: []knowledge.Learning{{ID: "K1", Kind: knowledge.Convention, Text: "Use tabs.", Status: knowledge.Active}},
	}}
	shared := host
	shared.Snapshot = knowledge.Snapshot{
		Format: knowledge.FormatVersion, Repo: "acme/widgets", NextID: 2,
		Learnings: []knowledge.Learning{{ID: "K1", Kind: knowledge.Convention, Text: "Use spaces.", Status: knowledge.Active}},
	}
	if err := st.Knowledge.Replace(host.Repo, host.Snapshot); err != nil {
		t.Fatal(err)
	}
	backend := &workspace.Memory{}
	if err := backend.WriteKnowledge(ctx, shared); err != nil {
		t.Fatal(err)
	}
	sync := &workspace.Sync{DB: st.DB, Backend: backend, Tasks: st.Tasks, Issues: st.Issues, Knowledge: st.Knowledge}
	st.Workspace = sync
	review, err := sync.ReviewKnowledge(ctx, "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := runKnowledge(ctx, cfg, st, cli.Command{Name: cli.KnowledgeReview, Repo: "acme/widgets"}, &out, &stderr); code != 0 {
		t.Fatalf("review exit %d: %s", code, stderr.String())
	}
	for _, want := range []string{"Use tabs.", "Use spaces.", review.SharedDigest, review.HostDigest,
		"foreman knowledge approve acme/widgets --digest " + review.SharedDigest + " --host-digest " + review.HostDigest} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("review output lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if code := runKnowledge(ctx, cfg, st, cli.Command{Name: cli.KnowledgeApprove, Repo: "acme/widgets", Digest: review.SharedDigest, HostDigest: review.HostDigest}, &out, &stderr); code != 0 {
		t.Fatalf("approve exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(out.String(), "Approved acme/widgets at shared digest "+review.SharedDigest) {
		t.Fatalf("approval output = %q", out.String())
	}
	got, err := st.Knowledge.Snapshot("acme/widgets")
	if err != nil || got.Learnings[0].Text != "Use spaces." {
		t.Fatalf("approved host knowledge = %+v, %v", got.Learnings, err)
	}
}

func seedKnowledge(t *testing.T, st state) {
	t.Helper()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	err := st.Knowledge.Apply("acme/widgets", knowledge.Evaluation{
		Task: "1", TaskAt: at, At: at, Fingerprint: "fp", Assessment: "fine", MaintainerRefs: []string{"D1"},
		Proposals: []knowledge.Proposal{{Kind: knowledge.Convention, Text: "Wrap errors with the operation name.", Scope: knowledge.Reusable, Source: knowledge.Maintainer, Cites: []string{"D1"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
}
