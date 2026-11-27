package workspace

import (
	"context"
	"errors"
	"fmt"

	"github.com/brymartinez/foreman/internal/knowledge"
)

// ErrKnowledgeReviewStale means the shared or host knowledge changed after review.
var ErrKnowledgeReviewStale = errors.New("the shared or host knowledge changed since review; nothing was approved")

// KnowledgeReview is the exact shared snapshot and host baseline a maintainer saw.
type KnowledgeReview struct {
	Repo         string
	Host         knowledge.Snapshot
	Shared       knowledge.Snapshot
	HostDigest   string
	SharedDigest string
}

// ReviewKnowledge reads the shared snapshot and the current host snapshot for a
// maintainer to inspect. Approval re-reads both snapshots under their locks.
func (s *Sync) ReviewKnowledge(ctx context.Context, repo string) (KnowledgeReview, error) {
	if !knowledge.ValidRepo(repo) {
		return KnowledgeReview{}, fmt.Errorf("invalid repository %q", repo)
	}
	entry, err := s.readKnowledgeEntry(ctx, repo)
	if err != nil {
		return KnowledgeReview{}, err
	}
	if entry == nil || entry.ParseErr != nil || entry.Snapshot == nil {
		return KnowledgeReview{}, fmt.Errorf("shared knowledge for %s is not ready to review", repo)
	}
	host, err := s.Knowledge.Snapshot(repo)
	if err != nil {
		return KnowledgeReview{}, err
	}
	shared := *entry.Snapshot
	return KnowledgeReview{
		Repo:         repo,
		Host:         host,
		Shared:       shared,
		HostDigest:   SnapshotDigest(host),
		SharedDigest: SnapshotDigest(shared),
	}, nil
}

// ApproveKnowledge applies the exact shared snapshot reviewed against hostDigest.
// If the host already has that snapshot, it records the digest without replacing it.
func (s *Sync) ApproveKnowledge(ctx context.Context, repo, sharedDigest, hostDigest string) error {
	if !knowledge.ValidRepo(repo) {
		return fmt.Errorf("invalid repository %q", repo)
	}
	unlock, err := s.lock(ctx, "publish:knowledge:"+repo)
	if err != nil {
		return err
	}
	defer unlock()

	free, err := s.Knowledge.Lock(repo)
	if err != nil {
		return err
	}
	defer free()

	entry, err := s.readKnowledgeEntry(ctx, repo)
	if err != nil {
		return err
	}
	if entry == nil || entry.ParseErr != nil || entry.Snapshot == nil {
		return ErrKnowledgeReviewStale
	}
	shared := *entry.Snapshot
	currentSharedDigest := SnapshotDigest(shared)
	if currentSharedDigest != sharedDigest {
		return ErrKnowledgeReviewStale
	}
	host, err := s.Knowledge.Snapshot(repo)
	if err != nil {
		return err
	}
	currentHostDigest := SnapshotDigest(host)
	if currentHostDigest != currentSharedDigest {
		if currentHostDigest != hostDigest {
			return ErrKnowledgeReviewStale
		}
		if err := s.Knowledge.Replace(repo, shared); err != nil {
			return fmt.Errorf("apply approved knowledge for %s: %w", repo, err)
		}
	}
	return s.recordAppliedKnowledge(repo, currentSharedDigest)
}

func (s *Sync) readKnowledgeEntry(ctx context.Context, repo string) (*KnowledgeEntry, error) {
	var entries []KnowledgeEntry
	var err error
	if reader, ok := s.Backend.(interface {
		KnowledgeEntriesFor(context.Context, string) ([]KnowledgeEntry, error)
	}); ok {
		entries, err = reader.KnowledgeEntriesFor(ctx, repo)
	} else {
		entries, err = s.Backend.ListKnowledge(ctx)
	}
	if err != nil {
		return nil, err
	}
	var match *KnowledgeEntry
	for i := range entries {
		entry := &entries[i]
		if entry.Repo != repo {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("more than one shared knowledge entry for %s", repo)
		}
		if entry.Snapshot != nil && entry.Snapshot.Repo != repo {
			return nil, fmt.Errorf("shared knowledge under %s names repository %s", repo, entry.Snapshot.Repo)
		}
		match = entry
	}
	return match, nil
}

func (s *Sync) recordAppliedKnowledge(repo, digest string) error {
	prev, err := s.row("knowledge", repo)
	if err != nil {
		return err
	}
	prev.Digest = digest
	prev.AnswerDigest = ""
	return s.saveRow("knowledge", repo, prev)
}
