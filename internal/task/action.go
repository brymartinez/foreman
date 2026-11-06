package task

type Role string

const (
	PlannerRole     Role = "planner"
	ImplementerRole Role = "implementer"
	SelfReviewRole  Role = "self_review"
	AdversarialRole Role = "adversarial_review"
)

type ActionKind string

const (
	PlanAction        ActionKind = "plan"
	ImplementAction   ActionKind = "implement"
	FixSelfAction     ActionKind = "fix_self_review"
	FixAdverseAction  ActionKind = "fix_adversarial_review"
	SelfReviewAction  ActionKind = "self_review"
	AdversarialAction ActionKind = "adversarial_review"
	PublishAction     ActionKind = "publish"
	FinalCheckAction  ActionKind = "final_check"
	MergeAction       ActionKind = "merge"
	FeedbackAction    ActionKind = "feedback"
)

// Action is saved before an agent runs. Started with no session ID means the
// provider might have started a retained session that Foreman cannot locate.
type Action struct {
	ID          string     `yaml:"id"`
	Kind        ActionKind `yaml:"kind"`
	BaseRef     string     `yaml:"base_ref,omitempty"`
	BaseHead    string     `yaml:"base_head,omitempty"`
	Head        string     `yaml:"head,omitempty"`
	FromHead    string     `yaml:"from_head,omitempty"`
	ReviewRound int        `yaml:"review_round,omitempty"`
	Started     bool       `yaml:"started,omitempty"`
}

func (a Action) Role() Role {
	switch a.Kind {
	case PlanAction:
		return PlannerRole
	case SelfReviewAction:
		return SelfReviewRole
	case AdversarialAction:
		return AdversarialRole
	default:
		return ImplementerRole
	}
}

func (d *Delegation) RoleSession(role Role) *Session {
	switch role {
	case PlannerRole:
		return &d.Planner
	case ImplementerRole:
		return &d.Implementer
	case AdversarialRole:
		return &d.Adversary
	}
	return nil
}

func (a Action) State() State {
	switch a.Kind {
	case PlanAction:
		return Planning
	case SelfReviewAction:
		return SelfReviewing
	case AdversarialAction:
		return AdversarialReviewing
	case PublishAction:
		return OpeningPR
	default:
		return Implementing
	}
}
