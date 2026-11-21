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
