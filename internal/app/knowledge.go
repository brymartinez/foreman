package app

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/task"
)

// ErrKnowledgeChanged means something wrote the repository's knowledge while an
// ingest was merging, so the merge is stale and nothing was saved.
var ErrKnowledgeChanged = errors.New("the repository's knowledge changed during the merge; nothing was saved, so run the ingest again")

// ExportKnowledge writes repo's learnings and evaluations as an export file.
func (a App) ExportKnowledge(repo string) ([]byte, error) {
	if a.Knowledge == nil {
		return nil, errors.New("knowledge is not configured")
	}
	x, err := a.Knowledge.Snapshot(repo)
	if err != nil {
		return nil, err
	}
	if x.Empty() {
		return nil, fmt.Errorf("%s: %w", repo, knowledge.ErrNoKnowledge)
	}
	x.ExportedAt = a.Now().UTC()
	return knowledge.Format(x)
}

// IngestInput is one ingest of an export file. Repo names the destination; empty
// uses the export's repository, and a different one needs Rebind.
type IngestInput struct {
	Data   []byte
	Repo   string
	Rebind bool
	// Agent runs the merge turn when both sides have learnings.
	Agent  task.Agent
	DryRun bool
	Log    io.Writer
}

type IngestMode string

const (
	// IngestRestored stored the export as it is into a repository with no knowledge.
	IngestRestored IngestMode = "restored"
	// IngestMerged combined the export with the repository's knowledge.
	IngestMerged IngestMode = "merged"
	// IngestUnchanged found nothing in the export the repository lacks.
	IngestUnchanged IngestMode = "unchanged"
)

type IngestResult struct {
	Source string
	Repo   string
	Mode   IngestMode
	// Result is the knowledge stored, or the proposed knowledge after a dry run.
	Result knowledge.Snapshot
	Saved  bool
	Usage  task.Usage
}

// IngestKnowledge stores an export under its repository. A repository with no
// knowledge takes the export as it is. Otherwise one merge turn groups the two
// sides' learnings and knowledge.Merge builds the result. It holds the
// repository's lock throughout and checks that the stored knowledge did not
// change before it saves. Any error leaves the stored knowledge as it was.
func (a App) IngestKnowledge(ctx context.Context, in IngestInput) (IngestResult, error) {
	if a.Knowledge == nil {
		return IngestResult{}, errors.New("knowledge is not configured")
	}
	x, err := knowledge.Parse(in.Data)
	if err != nil {
		return IngestResult{}, err
	}
	dest := x.Repo
	switch {
	case in.Rebind && in.Repo == "":
		return IngestResult{}, errors.New("rebind needs a destination repository")
	case in.Repo != "" && in.Repo != x.Repo && !in.Rebind:
		return IngestResult{}, fmt.Errorf("the export is for %s, not %s; rebind it to store it under %s", x.Repo, in.Repo, in.Repo)
	case in.Repo != "":
		dest = in.Repo
	}
	if !knowledge.ValidRepo(dest) {
		return IngestResult{}, fmt.Errorf("invalid repository %q", dest)
	}
	unlock, err := a.Knowledge.Lock(dest)
	if err != nil {
		return IngestResult{}, err
	}
	defer unlock()
	before, err := a.Knowledge.Digest(dest)
	if err != nil {
		return IngestResult{}, err
	}
	local, err := a.Knowledge.Snapshot(dest)
	if err != nil {
		return IngestResult{}, err
	}

	res := IngestResult{Source: x.Repo, Repo: dest, Mode: IngestRestored}
	if local.Empty() {
		res.Result = knowledge.Restore(x, dest)
	} else {
		m, err := knowledge.Prepare(local, x)
		if err != nil {
			return res, err
		}
		if m.Unchanged() {
			res.Mode, res.Result = IngestUnchanged, local
			return res, nil
		}
		groups := m.Singletons()
		if m.NeedsGrouping() {
			if a.Merger == nil {
				return res, errors.New("no merge agent is configured")
			}
			out, err := a.Merger.Merge(ctx, agent.EvaluateInput{Agent: in.Agent, Prompt: agent.MergePrompt(dest, m.Items), MaxBudgetUSD: a.LearningBudgetUSD, Log: in.Log}, m.Refs())
			res.Usage = out.Usage
			if err != nil {
				return res, fmt.Errorf("merge with %s: %w", in.Agent, err)
			}
			groups = out.Groups
		}
		merged, err := m.Apply(groups)
		if err != nil {
			return res, err
		}
		res.Mode, res.Result = IngestMerged, merged
	}
	if in.DryRun {
		return res, nil
	}
	after, err := a.Knowledge.Digest(dest)
	if err != nil {
		return res, err
	}
	if after != before {
		return res, ErrKnowledgeChanged
	}
	if err := a.Knowledge.Replace(dest, res.Result); err != nil {
		return res, err
	}
	res.Saved = true
	return res, nil
}
