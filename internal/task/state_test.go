package task

import "testing"

func postedPlan() []PlanComment {
	var out []PlanComment
	for _, d := range PlanDocs {
		out = append(out, PlanComment{Doc: d, URL: "u"})
	}
	return out
}

// stageRecords are the record shapes a run can stop at, shared by the stage and legacy tests.
func stageRecords() map[string]*Record {
	desc := ProposalDigest("feat: x", "Closes #7")
	local := func(reviews ...Review) *Record {
		return &Record{Plan: postedPlan(), Reviews: reviews,
			Delegation: &Delegation{ReviewBeforePR: true, LocalHead: "h1", ProposedTitle: "feat: x", ProposedBody: "Closes #7"}}
	}
	onPR := func(head string, reviews ...Review) *Record {
		return &Record{Plan: postedPlan(), Reviews: reviews, Delegation: &Delegation{PR: 7, PRHead: head, PRDigest: desc}}
	}
	selfClean := Review{Kind: SelfReview, PR: 7, Head: "h1", Proposal: desc, Clean: true}
	return map[string]*Record{
		"no plan":                                 {Delegation: &Delegation{}},
		"a posted plan":                           {Delegation: &Delegation{}, Plan: postedPlan()},
		"a local commit with no review":           local(),
		"a local commit with self findings":       local(Review{Kind: SelfReview, Head: "h1", Proposal: desc}),
		"a local commit with a clean self-review": local(Review{Kind: SelfReview, Head: "h1", Proposal: desc, Clean: true}),
		"a local commit with a clean review": local(Review{Kind: SelfReview, Head: "h1", Proposal: desc, Clean: true},
			Review{Head: "h1", Proposal: desc, Clean: true}),
		"a PR head with no review":              onPR("h1"),
		"a PR head with a clean self-review":    onPR("h1", selfClean),
		"a PR head with adversarial findings":   onPR("h1", selfClean, Review{PR: 7, Head: "h1", Proposal: desc}),
		"a PR head with a clean review":         onPR("h1", selfClean, Review{PR: 7, Head: "h1", Proposal: desc, Clean: true}),
		"a PR head newer than its clean review": onPR("h2", selfClean, Review{PR: 7, Head: "h1", Proposal: desc, Clean: true}),
	}
}

func TestNextStageIsTheStepTheRecordStillNeeds(t *testing.T) {
	want := map[string]State{
		"no plan":                                 Planning,
		"a posted plan":                           Implementing,
		"a local commit with no review":           SelfReviewing,
		"a local commit with self findings":       Implementing,
		"a local commit with a clean self-review": AdversarialReviewing,
		"a local commit with a clean review":      OpeningPR,
		"a PR head with no review":                SelfReviewing,
		"a PR head with a clean self-review":      AdversarialReviewing,
		"a PR head with adversarial findings":     Implementing,
		"a PR head with a clean review":           Implementing,
		"a PR head newer than its clean review":   SelfReviewing,
	}
	for name, rec := range stageRecords() {
		if got := rec.NextStage(); got != want[name] {
			t.Errorf("%s: NextStage() = %s, want %s", name, got, want[name])
		}
	}
}

func TestNextStageImplementsAnswersAndConflictRepairsFirst(t *testing.T) {
	recs := stageRecords()
	answered := recs["a PR head with no review"]
	answered.Decisions = []Decision{{Answer: "A"}}
	if got := answered.NextStage(); got != Implementing {
		t.Errorf("an undelivered answer: NextStage() = %s, want IMPLEMENTING", got)
	}
	conflict := recs["a PR head with a clean review"]
	conflict.Delegation.ConflictPending = true
	if got := conflict.NextStage(); got != Implementing {
		t.Errorf("a pending conflict repair: NextStage() = %s, want IMPLEMENTING", got)
	}
}

func TestUpgradeStateMapsOldStatesToTheCurrentStage(t *testing.T) {
	want := map[string]map[State]State{
		"no plan":                                 {LegacyWorking: Planning, LegacyReviewing: Planning},
		"a posted plan":                           {LegacyWorking: Implementing},
		"a local commit with no review":           {LegacyWorking: SelfReviewing},
		"a local commit with self findings":       {LegacyWorking: Implementing},
		"a local commit with a clean self-review": {LegacyWorking: AdversarialReviewing},
		"a local commit with a clean review":      {LegacyWorking: OpeningPR},
		"a PR head with no review":                {LegacyWorking: SelfReviewing, LegacyReviewing: SelfReviewing},
		"a PR head with a clean self-review":      {LegacyWorking: AdversarialReviewing, LegacyReviewing: AdversarialReviewing},
		"a PR head with adversarial findings":     {LegacyWorking: Implementing},
		"a PR head with a clean review":         {LegacyReviewing: WaitingForCI},
		"a PR head newer than its clean review": {LegacyReviewing: SelfReviewing},
	}
	for name := range want {
		for old, stage := range want[name] {
			rec := stageRecords()[name]
			rec.State = old
			rec.UpgradeState()
			if rec.State != stage {
				t.Errorf("%s in %s: upgraded to %s, want %s", name, old, rec.State, stage)
			}
		}
	}
}

func TestUpgradeStateLeavesCurrentStatesAlone(t *testing.T) {
	for _, s := range []State{Discovered, Planning, Implementing, SelfReviewing, AdversarialReviewing, OpeningPR, WaitingForCI, NeedsDecision, ReadyForHuman, Reviewed, Done, Failed, PRClosed} {
		rec := stageRecords()["a PR head with a clean review"]
		rec.State = s
		rec.UpgradeState()
		if rec.State != s {
			t.Errorf("%s upgraded to %s", s, rec.State)
		}
	}
}

func TestActiveStatesAreTheStages(t *testing.T) {
	for _, s := range []State{Planning, Implementing, SelfReviewing, AdversarialReviewing, OpeningPR, WaitingForCI} {
		if !s.Active() {
			t.Errorf("%s is not active", s)
		}
	}
	for _, s := range []State{Discovered, NeedsDecision, ReadyForHuman, Reviewed, Done, Failed, PRClosed, LegacyWorking, LegacyReviewing} {
		if s.Active() {
			t.Errorf("%s is active", s)
		}
	}
}
