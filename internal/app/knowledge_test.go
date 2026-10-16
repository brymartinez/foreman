package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/task"
)

const kRepo = "acme/widgets"

var k0 = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

type stubMerger struct {
	calls  int
	agent  task.Agent
	refs   []string
	groups func(refs []string) []knowledge.Group
	err    error
	during func()
}

func (s *stubMerger) Merge(_ context.Context, in agent.EvaluateInput, refs []string) (agent.MergeResult, error) {
	s.calls++
	s.agent, s.refs = in.Agent, refs
	if s.during != nil {
		s.during()
	}
	if s.err != nil {
		return agent.MergeResult{}, s.err
	}
	return agent.MergeResult{Groups: s.groups(refs)}, nil
}

// oneGroup merges every learning into one with text.
func oneGroup(text string) func([]string) []knowledge.Group {
	return func(refs []string) []knowledge.Group {
		return []knowledge.Group{{Members: refs, Kind: knowledge.Convention, Text: text}}
	}
}

func kEval(id string, startDay int, text string, source knowledge.Source) knowledge.Evaluation {
	p := knowledge.Proposal{Kind: knowledge.Convention, Text: text, Scope: knowledge.Reusable, Source: source, Why: "seen"}
	if source == knowledge.Maintainer {
		p.Cites = []string{"D1"}
	}
	at := k0.Add(time.Duration(startDay) * 24 * time.Hour)
	return knowledge.Evaluation{Task: id, TaskAt: at, At: at.Add(time.Hour), Fingerprint: "fp-" + id, Assessment: "fine", MaintainerRefs: []string{"D1"}, Proposals: []knowledge.Proposal{p}}
}

func machine(t *testing.T, evs ...knowledge.Evaluation) knowledge.Store {
	t.Helper()
	s := knowledge.Store{Dir: t.TempDir()}
	for _, ev := range evs {
		if err := s.Apply(kRepo, ev); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func knowledgeApp(s knowledge.Store, m Merger) App {
	return App{Knowledge: s, Merger: m, Now: func() time.Time { return k0.Add(100 * 24 * time.Hour) }}
}

func exportOf(t *testing.T, s knowledge.Store) []byte {
	t.Helper()
	data, err := knowledgeApp(s, nil).ExportKnowledge(kRepo)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// files reads every file of the store but the empty lock file, which taking the
// lock creates, so a test can check what changed.
func files(t *testing.T, s knowledge.Store) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(s.Dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == "learning.lock" {
			return err
		}
		raw, err := os.ReadFile(path)
		rel, _ := filepath.Rel(s.Dir, path)
		out[rel] = string(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestExportKnowledgeNeedsKnowledge(t *testing.T) {
	_, err := knowledgeApp(machine(t), nil).ExportKnowledge(kRepo)
	if !errors.Is(err, knowledge.ErrNoKnowledge) {
		t.Fatalf("err = %v", err)
	}
}

func TestIngestIntoARepositoryWithoutKnowledgeRestoresWithoutTheAgent(t *testing.T) {
	src := machine(t, kEval("1", 0, "Wrap errors with the operation name.", knowledge.Maintainer), kEval("2", 2, "Run the linter first.", knowledge.Observed))
	dst := machine(t)
	m := &stubMerger{}
	res, err := knowledgeApp(dst, m).IngestKnowledge(context.Background(), IngestInput{Data: exportOf(t, src), Agent: task.Claude})
	if err != nil || res.Mode != IngestRestored || !res.Saved || m.calls != 0 {
		t.Fatalf("res = %+v, err = %v, agent calls = %d", res, err, m.calls)
	}
	want, got := files(t, src), files(t, dst)
	for _, name := range []string{"acme/widgets/learnings.yaml", "acme/widgets/knowledge.md", "acme/widgets/evaluations/1.yaml", "acme/widgets/evaluations/2.yaml"} {
		if got[name] != want[name] {
			t.Errorf("%s differs:\n%s\nwant\n%s", name, got[name], want[name])
		}
	}
	if len(got) != 4 {
		t.Errorf("ingest wrote %d files, want 4: %v", len(got), reflect.ValueOf(got).MapKeys())
	}
}

func TestIngestMergesWithOneAgentCallAndARepeatChangesNothing(t *testing.T) {
	src := machine(t, kEval("2", 3, "Errors carry the operation name.", knowledge.Observed))
	dst := machine(t, kEval("1", 0, "Wrap errors with the operation name.", knowledge.Observed))
	data := exportOf(t, src)
	m := &stubMerger{groups: oneGroup("Wrap returned errors with the operation name.")}
	a := knowledgeApp(dst, m)
	res, err := a.IngestKnowledge(context.Background(), IngestInput{Data: data, Agent: task.Codex})
	if err != nil || res.Mode != IngestMerged || !res.Saved || m.calls != 1 || m.agent != task.Codex {
		t.Fatalf("res = %+v, err = %v, calls = %d, agent = %s", res, err, m.calls, m.agent)
	}
	ls, _ := dst.Learnings(kRepo)
	if len(ls) != 1 || ls[0].Status != knowledge.Active || ls[0].Tasks() != 2 || ls[0].Text != "Wrap returned errors with the operation name." {
		t.Fatalf("learnings = %+v", ls)
	}
	md, _ := os.ReadFile(filepath.Join(dst.Dir, kRepo, "knowledge.md"))
	if !strings.Contains(string(md), "Wrap returned errors with the operation name.") {
		t.Fatalf("knowledge.md not regenerated:\n%s", md)
	}

	before := files(t, dst)
	res, err = a.IngestKnowledge(context.Background(), IngestInput{Data: data, Agent: task.Codex})
	if err != nil || res.Mode != IngestUnchanged || res.Saved || m.calls != 1 {
		t.Fatalf("repeat: res = %+v, err = %v, calls = %d", res, err, m.calls)
	}
	if !reflect.DeepEqual(files(t, dst), before) {
		t.Fatal("a repeat ingest changed the store")
	}
}

func TestIngestChecksTheRepositoryIdentity(t *testing.T) {
	src := machine(t, kEval("1", 0, "Wrap errors with the operation name.", knowledge.Observed))
	data := exportOf(t, src)

	dst := machine(t)
	_, err := knowledgeApp(dst, nil).IngestKnowledge(context.Background(), IngestInput{Data: data, Repo: "acme/gadgets"})
	if err == nil || !strings.Contains(err.Error(), "acme/widgets") {
		t.Fatalf("err = %v", err)
	}
	if len(files(t, dst)) != 0 {
		t.Fatal("a rejected destination wrote files")
	}
	if _, err := knowledgeApp(dst, nil).IngestKnowledge(context.Background(), IngestInput{Data: data, Rebind: true}); err == nil {
		t.Fatal("rebind without a destination was accepted")
	}

	res, err := knowledgeApp(dst, nil).IngestKnowledge(context.Background(), IngestInput{Data: data, Repo: "acme/gadgets", Rebind: true})
	if err != nil || res.Repo != "acme/gadgets" || res.Source != kRepo {
		t.Fatalf("rebind: res = %+v, err = %v", res, err)
	}
	if ls, _ := dst.Learnings("acme/gadgets"); len(ls) != 1 {
		t.Fatalf("rebind stored %+v", ls)
	}
	if ls, _ := dst.Learnings(kRepo); len(ls) != 0 {
		t.Fatal("rebind also stored under the source repository")
	}
}

func TestIngestFailuresLeaveTheStoreUnchanged(t *testing.T) {
	text := "Wrap errors with the operation name."
	for name, tc := range map[string]struct {
		data   func([]byte) []byte
		merger *stubMerger
		dryRun bool
		locked bool
		want   error
	}{
		"invalid file": {data: func([]byte) []byte { return []byte("# notes\n") }},
		"unsupported version": {data: func(d []byte) []byte {
			return []byte(strings.Replace(string(d), "foreman-knowledge/1", "foreman-knowledge/9", 1))
		}},
		"agent failure": {merger: &stubMerger{err: errors.New("quota exhausted")}},
		"invalid grouping": {merger: &stubMerger{groups: func(refs []string) []knowledge.Group {
			return []knowledge.Group{{Members: refs[:1], Kind: knowledge.Convention, Text: text}}
		}}},
		"lock held": {locked: true, want: knowledge.ErrLocked},
		"dry run":   {dryRun: true},
	} {
		t.Run(name, func(t *testing.T) {
			src := machine(t, kEval("2", 3, "Errors carry the operation name.", knowledge.Observed))
			dst := machine(t, kEval("1", 0, text, knowledge.Observed))
			data := exportOf(t, src)
			if tc.data != nil {
				data = tc.data(data)
			}
			m := tc.merger
			if m == nil {
				m = &stubMerger{groups: oneGroup(text)}
			}
			if tc.locked {
				unlock, err := dst.Lock(kRepo)
				if err != nil {
					t.Fatal(err)
				}
				defer unlock()
			}
			before := files(t, dst)
			res, err := knowledgeApp(dst, m).IngestKnowledge(context.Background(), IngestInput{Data: data, Agent: task.Claude, DryRun: tc.dryRun})
			switch {
			case tc.dryRun && (err != nil || res.Saved || res.Mode != IngestMerged || len(res.Result.Learnings) != 1):
				t.Fatalf("dry run: res = %+v, err = %v", res, err)
			case !tc.dryRun && err == nil:
				t.Fatal("no error")
			case tc.want != nil && !errors.Is(err, tc.want):
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if !reflect.DeepEqual(files(t, dst), before) {
				t.Fatal("the store changed")
			}
		})
	}
}

func TestIngestDoesNotSaveOverAConcurrentWrite(t *testing.T) {
	src := machine(t, kEval("2", 3, "Errors carry the operation name.", knowledge.Observed))
	dst := machine(t, kEval("1", 0, "Wrap errors with the operation name.", knowledge.Observed))
	var during map[string]string
	m := &stubMerger{groups: oneGroup("Wrap errors with the operation name.")}
	m.during = func() {
		if err := dst.Apply(kRepo, kEval("3", 5, "Keep functions short.", knowledge.Observed)); err != nil {
			t.Fatal(err)
		}
		during = files(t, dst)
	}
	_, err := knowledgeApp(dst, m).IngestKnowledge(context.Background(), IngestInput{Data: exportOf(t, src), Agent: task.Claude})
	if !errors.Is(err, ErrKnowledgeChanged) {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(files(t, dst), during) {
		t.Fatal("the ingest wrote over the concurrent change")
	}
}

func TestIngestStopsOnAnUnresolvedMaintainerConflict(t *testing.T) {
	src := machine(t, kEval("2", 0, "Indent YAML with four spaces.", knowledge.Maintainer))
	dst := machine(t, kEval("1", 0, "Indent YAML with two spaces.", knowledge.Maintainer))
	m := &stubMerger{groups: func(refs []string) []knowledge.Group {
		return []knowledge.Group{{Members: refs, Kind: knowledge.Convention, Text: "Indent YAML consistently.", Conflict: "different indentation"}}
	}}
	before := files(t, dst)
	_, err := knowledgeApp(dst, m).IngestKnowledge(context.Background(), IngestInput{Data: exportOf(t, src), Agent: task.Claude})
	var conflict *knowledge.ConflictError
	if !errors.As(err, &conflict) || !strings.Contains(err.Error(), "four spaces") || !strings.Contains(err.Error(), "two spaces") {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(files(t, dst), before) {
		t.Fatal("a conflict changed the store")
	}
}

func TestExportKnowledgeWaitsForTheRepositoryLock(t *testing.T) {
	s := machine(t, kEval("1", 0, "Wrap errors with the operation name.", knowledge.Observed))
	unlock, err := s.Lock(kRepo)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := knowledgeApp(s, nil).ExportKnowledge(kRepo); !errors.Is(err, knowledge.ErrLocked) {
		t.Fatalf("err = %v, want the lock held", err)
	}
}
