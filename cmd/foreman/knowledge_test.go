package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/cli"
	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/knowledge"
)

func TestKnowledgeExportThenIngestRestoresOnAnotherMachine(t *testing.T) {
	src := config.Config{KnowledgeDir: t.TempDir()}
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	err := knowledge.Store{Dir: src.KnowledgeDir}.Apply("acme/widgets", knowledge.Evaluation{
		Task: "1", TaskAt: at, At: at, Fingerprint: "fp", Assessment: "fine", MaintainerRefs: []string{"D1"},
		Proposals: []knowledge.Proposal{{Kind: knowledge.Convention, Text: "Wrap errors with the operation name.", Scope: knowledge.Reusable, Source: knowledge.Maintainer, Cites: []string{"D1"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "knowledge.md")
	var out, errs bytes.Buffer
	if code := runKnowledge(context.Background(), src, cli.Command{Name: cli.KnowledgeExport, Repo: "acme/widgets", File: file}, &out, &errs); code != 0 {
		t.Fatalf("export exit %d: %s", code, errs.String())
	}

	dst := config.Config{KnowledgeDir: t.TempDir()}
	out.Reset()
	if code := runKnowledge(context.Background(), dst, cli.Command{Name: cli.KnowledgeIngest, File: file, Repo: "acme/gadgets"}, &out, &errs); code != 1 {
		t.Fatalf("ingest into another repository exit %d, want 1", code)
	}
	if code := runKnowledge(context.Background(), dst, cli.Command{Name: cli.KnowledgeIngest, File: file}, &out, &errs); code != 0 {
		t.Fatalf("ingest exit %d: %s", code, errs.String())
	}
	if !strings.Contains(out.String(), "Restored acme/widgets: 1 learnings (1 active) and 1 evaluations.") {
		t.Fatalf("stdout = %q", out.String())
	}
	md, err := os.ReadFile(filepath.Join(dst.KnowledgeDir, "acme", "widgets", "knowledge.md"))
	if err != nil || !strings.Contains(string(md), "Wrap errors with the operation name.") {
		t.Fatalf("knowledge.md = %q, %v", md, err)
	}
}
