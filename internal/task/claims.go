package task

// Claim is the historical evidence for an acceptance criterion, stored in old task records.
type Claim struct {
	// Criteria are the acceptance criteria the claim covers. Several may share one
	// claim when the same evidence supports them.
	Criteria []string `json:"criteria" yaml:"criteria"`
	Behavior string   `json:"behavior" yaml:"behavior"`
	// Evidence points to the code and tests that deliver and check the behavior.
	Evidence string `json:"evidence" yaml:"evidence"`
	// Check is the check the implementer performed, or why it performed none.
	Check string `json:"check" yaml:"check"`
}
