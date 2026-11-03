package task

import (
	"fmt"
	"strings"
)

type RejectedFinding struct {
	Finding int    `json:"finding" yaml:"finding"`
	Reason  string `json:"reason" yaml:"reason"`
}

type ReviewAssessment struct {
	Round    int               `json:"round"`
	Rejected []RejectedFinding `json:"rejected"`
}

// AllFindingsRejected records an implementer assessment without altering the verdict.
func (r *Review) AllFindingsRejected() bool {
	if r == nil || r.Kind != SelfReview || len(r.Findings) == 0 || len(r.RejectedFindings) != len(r.Findings) {
		return false
	}
	seen := make(map[int]bool)
	for _, f := range r.RejectedFindings {
		if f.Finding < 1 || f.Finding > len(r.Findings) || strings.TrimSpace(f.Reason) == "" || seen[f.Finding] {
			return false
		}
		seen[f.Finding] = true
	}
	return true
}

// DecisionsDigest binds adversarial coverage to the answers and their meaning.
func DecisionsDigest(decisions []Decision) string {
	var s strings.Builder
	for _, d := range decisions {
		if d.Answer == "" {
			continue
		}
		fmt.Fprintf(&s, "%q %q %q\n", d.Question.Issue, d.Question.Context, d.Answer)
		for _, o := range d.Question.Options {
			fmt.Fprintf(&s, "%q %q\n", o.Label, o.Description)
		}
	}
	if s.Len() == 0 {
		return ""
	}
	return Digest(s.String())
}
