package task

import (
	"encoding/json"
	"errors"
	"strings"
)

// Claim is one behavior the implementer says its change delivers, tied to the PRD
// acceptance criteria it covers. Reviewers test claims; Foreman never publishes them.
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

func (c Claim) Validate() error {
	criteria := 0
	for _, s := range c.Criteria {
		if strings.TrimSpace(s) != "" {
			criteria++
		}
	}
	switch {
	case criteria == 0:
		return errors.New("claim names no acceptance criterion")
	case strings.TrimSpace(c.Behavior) == "":
		return errors.New("claim has no behavior")
	case strings.TrimSpace(c.Evidence) == "":
		return errors.New("claim has no evidence")
	case strings.TrimSpace(c.Check) == "":
		return errors.New("claim has no check or reason it was not performed")
	}
	return nil
}

// ClaimsDigest identifies a claim set, so a review records which claims it read.
// Surrounding space is ignored. No claims digest to the empty string.
func ClaimsDigest(claims []Claim) string {
	if len(claims) == 0 {
		return ""
	}
	norm := make([]Claim, len(claims))
	for i, c := range claims {
		norm[i] = Claim{Behavior: strings.TrimSpace(c.Behavior), Evidence: strings.TrimSpace(c.Evidence), Check: strings.TrimSpace(c.Check)}
		for _, s := range c.Criteria {
			norm[i].Criteria = append(norm[i].Criteria, strings.TrimSpace(s))
		}
	}
	raw, _ := json.Marshal(norm)
	return Digest(string(raw))
}

// Limitation is a behavior a reviewer could not check, and why. A blocking one keeps
// the review from being clean: it prevents judging an acceptance criterion or hides a
// plausible serious defect.
type Limitation struct {
	Behavior string `json:"behavior" yaml:"behavior"`
	Reason   string `json:"reason" yaml:"reason"`
	Blocking bool   `json:"blocking" yaml:"blocking,omitempty"`
}

// ClaimsCovered reports whether review read the claim set and PRD the task holds now.
// A task started without claim verification ignores both, so it keeps the coverage
// rules it started with.
func (r *Record) ClaimsCovered(review *Review) bool {
	d := r.Delegation
	if review == nil || d == nil || !d.ClaimVerification {
		return review != nil
	}
	return review.Claims == ClaimsDigest(d.Claims) && review.PRD == d.PRDDigest
}
