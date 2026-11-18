package app

import "github.com/brymartinez/foreman/internal/task"

func legacyModelPolicy(agent task.Agent, profile *task.Profile) task.ModelPolicy {
	if agent == "" && profile != nil {
		agent = profile.Agent
	}
	planning := task.ModelTarget{Agent: agent}
	implementation := task.ModelTarget{Agent: agent}
	if profile != nil {
		planning.Model = profile.PlanModel
		planning.ReasoningEffort = profile.PlanReasoningEffort
		implementation.Model = profile.Model
		implementation.ReasoningEffort = profile.ReasoningEffort
	}
	return task.ModelPolicy{
		Planning:          planning,
		Implementation:    implementation,
		AdversarialReview: task.ModelTarget{Agent: agent.Reviewer()},
	}
}

func (a App) modelPolicy(d *task.Delegation) task.ModelPolicy {
	if d == nil {
		return a.Models
	}
	if d.Models != nil {
		return *d.Models
	}
	return a.migrateModelPolicy(d)
}

func (a App) migrateModelPolicy(d *task.Delegation) task.ModelPolicy {
	if d == nil {
		return task.ModelPolicy{}
	}
	policy := legacyModelPolicy(d.Implementer.Agent, d.Profile)
	if d.Planner.Agent != "" {
		policy.Planning.Agent = d.Planner.Agent
	}
	if configured := a.Models.Planning; configured.Agent == policy.Planning.Agent && policy.Planning.Model == "" {
		policy.Planning = configured
	}
	if configured := a.Models.Implementation; configured.Agent == policy.Implementation.Agent && policy.Implementation.Model == "" {
		policy.Implementation = configured
	}
	if d.Reviewer != "" {
		policy.AdversarialReview.Agent = d.Reviewer
	}
	if configured := a.Models.AdversarialReview; configured.Agent == policy.AdversarialReview.Agent {
		policy.AdversarialReview = configured
	}
	return policy
}
