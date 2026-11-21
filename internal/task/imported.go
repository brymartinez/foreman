package task

import "time"

// ImportedWork is the durable progress a portable import restored for a local issue
// whose run happened elsewhere: the checked planning documents, the last checkpoint,
// and the branch, commit, and PR references. It describes prior work. It holds no
// session, worktree, or runner, so restoring it starts no agent; a later start
// reconnects to the branch and PR when they are reachable and plans again when the
// saved documents no longer match the issue.
type ImportedWork struct {
	Documents      map[PlanDoc]string `yaml:"documents,omitempty"`
	Fingerprint    string             `yaml:"description_fingerprint,omitempty"`
	CheckpointHead string             `yaml:"checkpoint_head,omitempty"`
	CheckpointNext string             `yaml:"checkpoint_next,omitempty"`
	Checkpoints    int                `yaml:"checkpoints,omitempty"`
	Branch         string             `yaml:"branch,omitempty"`
	Commits        []string           `yaml:"commits,omitempty"`
	PR             int                `yaml:"pr,omitempty"`
	PRURL          string             `yaml:"pr_url,omitempty"`
	At             time.Time          `yaml:"at"`
}
