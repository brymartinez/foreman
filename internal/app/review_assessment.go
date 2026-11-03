package app

import (
	"errors"
	"fmt"

	"github.com/brymartinez/foreman/internal/task"
)

func validateReviewAssessment(rec *task.Record, res turnResult) error {
	assessment := res.turn.Outcome.ReviewAssessment
	if assessment == nil {
		return nil
	}
	review := rec.LatestReview()
	if review == nil || review.Kind != task.SelfReview || review.Clean || review.Round != assessment.Round || review.Head == "" {
		return errors.New("reviewAssessment must address the latest self-review round with a recorded commit")
	}
	for _, rejection := range assessment.Rejected {
		if rejection.Finding > len(review.Findings) {
			return fmt.Errorf("reviewAssessment references unknown finding %d", rejection.Finding)
		}
	}
	if res.head() == "" {
		return errors.New("reviewAssessment requires a verified change")
	}
	return nil
}

func applyReviewAssessment(rec *task.Record, res turnResult) {
	if res.err != nil || res.turn.Outcome.ReviewAssessment == nil {
		return
	}
	assessment := res.turn.Outcome.ReviewAssessment
	review := rec.LatestReview()
	if review != nil && review.Round == assessment.Round {
		review.RejectedFindings = assessment.Rejected
	}
}
