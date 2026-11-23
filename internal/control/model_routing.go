package control

import (
	"errors"

	"github.com/iancuuandrei/fabric/internal/access"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/modelpolicy"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

// modelProfileForInput selects only among profiles frozen in configuration.
// ContextBytes is the exact final UTF-8 input size; it is not token usage.
func modelProfileForInput(c config.Config, role, input string, failures int) (runtime.Profile, error) {
	profile, _, err := modelSelectionForInput(c, role, input, failures)
	return profile, err
}

func modelSelectionForInput(c config.Config, role, input string, failures int) (runtime.Profile, *access.RoutingDecision, error) {
	// Keep the pre-policy builder path independent of full config validation.
	// Older planner callers intentionally construct only the configured role
	// profile, and nil policy must preserve their exact invocation identity.
	base, err := configuredRoleProfile(c, role)
	if err != nil {
		return runtime.Profile{}, nil, err
	}
	if c.ModelPolicy == nil {
		return base, nil, nil
	}
	if _, err := c.Route(role); err != nil {
		return runtime.Profile{}, nil, err
	}
	if _, configured := c.ModelPolicy.Rules[role]; !configured {
		return base, nil, nil
	}
	decision, err := modelpolicy.Select(*c.ModelPolicy, modelpolicy.Request{
		Role: role, ReadOnly: role == "explorer", Complexity: modelpolicy.LevelMedium, Risk: modelpolicy.LevelMedium,
		Uncertainty: modelpolicy.LevelMedium, ContextBytes: int64(len(input)), Failures: failures,
	})
	if err != nil {
		return runtime.Profile{}, nil, err
	}
	selected := runtime.Profile{Runtime: decision.Profile.Runtime, Provider: decision.Profile.Provider, Model: decision.Profile.Model, Effort: decision.Profile.Effort, Role: role}
	if !c.AllowsProfile(selected) {
		return runtime.Profile{}, nil, errors.New("model policy selected an unconfigured profile")
	}
	if c.ModelPolicy.DecisionEvidenceVersion == 0 || c.ModelPolicy.DecisionEvidenceVersion == 2 {
		return selected, nil, nil
	}
	configID, err := c.ID()
	if err != nil {
		return runtime.Profile{}, nil, err
	}
	evidence := &access.RoutingDecision{
		Version:          1,
		ConfigID:         configID,
		ProfileName:      decision.Profile.Name,
		Model:            decision.Profile.Model,
		Effort:           decision.Profile.Effort,
		Reason:           decision.Reason,
		ContextBytes:     int64(len(input)),
		AcceptedFailures: failures,
	}
	return selected, evidence, nil
}

func configuredRoleProfile(c config.Config, role string) (runtime.Profile, error) {
	var profile *runtime.Profile
	switch role {
	case "planner":
		profile = &c.Planner
	case "writer":
		profile = c.Writer
	case "fixer":
		profile = c.Fixer
	case "explorer":
		profile = c.Explorer
	case "reviewer":
		profile = c.Reviewer
	default:
		return runtime.Profile{}, errors.New("unknown routing role")
	}
	if profile == nil {
		return runtime.Profile{}, errors.New("role has no explicit runtime configuration")
	}
	if err := profile.Validate(); err != nil {
		return runtime.Profile{}, err
	}
	return *profile, nil
}

func modelProfileForSnapshot(s Snapshot, role, input string) (runtime.Profile, error) {
	selected, _, err := modelSelectionForSnapshot(s, role, input, modelFailureCount(s, role, input))
	return selected, err
}

// modelRoutingDecisionForInvocation rederives opted-in evidence from immutable
// controller configuration, the exact invocation input, and accepted state.
// Explorers recover their original task question from the controller-owned host
// intent because their runtime input is a serialized prompt rather than that
// question.
func modelRoutingDecisionForInvocation(s Snapshot, invocation runtime.Invocation) (*access.RoutingDecision, error) {
	policy := s.Creation.Config.ModelPolicy
	if policy == nil || policy.DecisionEvidenceVersion == 0 {
		return nil, nil
	}
	if _, configured := policy.Rules[invocation.Profile.Role]; !configured {
		return nil, nil
	}
	failures := 0
	switch invocation.Profile.Role {
	case "planner":
	case "explorer":
		host, ok := explorerRunForInvocation(s, invocation.ID)
		if !ok || host.Intent.Invocation.ID != invocation.ID {
			return nil, errors.New("routing evidence lacks the exact explorer question binding")
		}
		failures = modelFailureCount(s, "explorer", host.Intent.Question)
	case "writer", "fixer", "reviewer":
		failures = modelFailureCount(s, invocation.Profile.Role, invocation.Input)
	default:
		return nil, errors.New("routing evidence role is unsupported")
	}
	if policy.DecisionEvidenceVersion == 2 {
		selected, evidence, err := modelSelectionForSnapshot(s, invocation.Profile.Role, invocation.Input, failures)
		if err != nil {
			return nil, err
		}
		if selected != invocation.Profile || evidence == nil {
			return nil, errors.New("runtime profile differs from the rederived model policy decision")
		}
		return evidence, nil
	}
	selected, evidence, err := modelSelectionForInput(s.Creation.Config, invocation.Profile.Role, invocation.Input, failures)
	if err != nil {
		return nil, err
	}
	if selected != invocation.Profile || evidence == nil {
		return nil, errors.New("runtime profile differs from the rederived model policy decision")
	}
	return evidence, nil
}

func modelSelectionForSnapshot(s Snapshot, role, input string, failures int) (runtime.Profile, *access.RoutingDecision, error) {
	c := s.Creation.Config
	if c.ModelPolicy == nil || c.ModelPolicy.DecisionEvidenceVersion != 2 {
		return modelSelectionForInput(c, role, input, failures)
	}
	if _, configured := c.ModelPolicy.Rules[role]; !configured {
		selected, err := modelProfileForInput(c, role, input, failures)
		return selected, nil, err
	}
	objectiveHash, err := modelpolicy.ObjectiveDigest(s.Creation.Objective)
	if err != nil {
		return runtime.Profile{}, nil, err
	}
	return modelSelectionForObjectiveHash(c, role, int64(len(input)), failures, objectiveHash)
}

func modelSelectionForObjectiveHash(c config.Config, role string, inputBytes int64, failures int, objectiveHash string) (runtime.Profile, *access.RoutingDecision, error) {
	policy := c.ModelPolicy
	if policy == nil || policy.DecisionEvidenceVersion != 2 {
		return runtime.Profile{}, nil, errors.New("version 2 routing policy is required")
	}
	if _, configured := policy.Rules[role]; !configured {
		selected, err := modelProfileForInput(c, role, "", failures)
		return selected, nil, err
	}
	decision, err := modelpolicy.Select(*policy, modelpolicy.Request{
		Role: role, ReadOnly: role == "explorer", Complexity: modelpolicy.LevelMedium, Risk: modelpolicy.LevelMedium,
		Uncertainty: modelpolicy.LevelMedium, ContextBytes: inputBytes, Failures: failures,
	})
	if err != nil {
		return runtime.Profile{}, nil, err
	}
	configID, err := c.ID()
	if err != nil {
		return runtime.Profile{}, nil, err
	}
	selectedProfile := decision.Profile
	selectedName, reason := selectedProfile.Name, decision.Reason
	var calibrationEvidence *access.CalibrationDecision
	if role == "fixer" {
		if policy.Calibration == nil {
			return runtime.Profile{}, nil, errors.New("version 2 fixer routing requires calibration")
		}
		selection, err := modelpolicy.EvaluateCalibration(*policy.Calibration)
		if err != nil {
			return runtime.Profile{}, nil, err
		}
		scopeMatched := containsObjective(policy.Calibration.Objectives, objectiveHash)
		selectionReason := selection.Reason
		candidateSelected := selection.Selected
		if !scopeMatched {
			selectionReason = "fallback_objective_out_of_scope"
			candidateSelected = false
		}
		candidateApplied := scopeMatched && selection.Selected && decision.Reason == "default-configured-profile"
		if candidateApplied {
			selectedProfile = selection.Profile
			selectedName = policy.Calibration.Candidate.Name
			reason = "calibrated-candidate"
		} else if !scopeMatched && decision.Reason == "default-configured-profile" {
			reason = "calibration-objective-out-of-scope"
		}
		calibrationEvidence = &access.CalibrationDecision{
			Digest: selection.Digest, FamilyID: selection.FamilyID,
			BaselineProfile: policy.Calibration.Baseline.Name, CandidateProfile: policy.Calibration.Candidate.Name,
			SelectionReason: selectionReason, ScopeMatched: scopeMatched,
			CandidateSelected: candidateSelected, CandidateApplied: candidateApplied,
			Training: calibrationArmCounts(selection.Training, *policy.Calibration),
			Holdout:  calibrationArmCounts(selection.Holdout, *policy.Calibration),
		}
	}
	selected := runtime.Profile{Runtime: selectedProfile.Runtime, Provider: selectedProfile.Provider, Model: selectedProfile.Model, Effort: selectedProfile.Effort, Role: role}
	if !c.AllowsProfile(selected) {
		return runtime.Profile{}, nil, errors.New("model policy selected an unconfigured profile")
	}
	return selected, &access.RoutingDecision{
		Version: 2, ConfigID: configID, ProfileName: selectedName,
		Model: selected.Model, Effort: selected.Effort, Reason: reason,
		ContextBytes: inputBytes, AcceptedFailures: failures,
		ObjectiveHash: objectiveHash, Calibration: calibrationEvidence,
	}, nil
}

func containsObjective(objectives []string, objectiveHash string) bool {
	for _, declared := range objectives {
		if declared == objectiveHash {
			return true
		}
	}
	return false
}

func calibrationArmCounts(counts map[string]modelpolicy.CalibrationCounts, calibration modelpolicy.Calibration) access.CalibrationArmCounts {
	convert := func(value modelpolicy.CalibrationCounts) access.CalibrationOutcomeCounts {
		return access.CalibrationOutcomeCounts{Assigned: value.Assigned, Accepted: value.Accepted, Failed: value.Failed, Unknown: value.Unknown, NotExercised: value.NotExercised, Drifted: value.Drifted}
	}
	return access.CalibrationArmCounts{Baseline: convert(counts[calibration.Baseline.Name]), Candidate: convert(counts[calibration.Candidate.Name])}
}

func modelFailureCount(s Snapshot, role, input string) int {
	failures := 0
	if s.Graph != nil {
		var taskID string
		switch role {
		case "explorer":
			for _, task := range s.Graph.Graph.Tasks {
				if task.Kind != "research" && task.Kind != "design" {
					continue
				}
				question, err := explorerQuestionForTask(s, task)
				if err == nil && question == input {
					taskID = task.ID
					break
				}
			}
		case "writer", "fixer":
			if task, ok := graphImplementationTask(s); ok {
				taskID = task.ID
			}
		case "reviewer":
			for _, task := range s.Graph.Graph.Tasks {
				if task.Kind == "review" && !task.Completed {
					taskID = task.ID
					break
				}
			}
		}
		if taskID != "" {
			if task, ok := s.Graph.Graph.Task(taskID); ok {
				for _, attempt := range task.Attempts {
					if attempt.Outcome == "failed" {
						failures++
					}
				}
			}
		}
	}
	if role == "writer" || role == "fixer" {
		candidateID := ""
		if s.Candidate != nil {
			candidateID, _ = s.Candidate.ID()
		}
		if candidateID != "" && s.Verification != nil && !s.Verification.Pending && s.Verification.Plan.CandidateID == candidateID {
			for _, observation := range s.Verification.Observations {
				if observation.Result.Status == "FAIL" && observation.After.Candidate != nil && *observation.After.Candidate == *s.Candidate {
					failures++
				}
			}
		}
		if candidateID != "" && s.Review != nil {
			var verdict ReviewVerdict
			if err := canonical.Decode([]byte(s.Review.Result.Output), &verdict); err == nil && verdict.Decision == "changes_requested" && verdict.CandidateID == candidateID {
				failures++
			}
		}
	}
	if failures > modelpolicy.MaxFailures {
		return modelpolicy.MaxFailures
	}
	return failures
}

func modelChoiceForProfile(c config.Config, role string, profile runtime.Profile) (*access.ModelChoice, error) {
	base, err := c.Route(role)
	if err != nil {
		return nil, err
	}
	if profile == base {
		return nil, nil
	}
	if !c.AllowsProfile(profile) {
		return nil, errors.New("selected model profile is not allowed")
	}
	return &access.ModelChoice{Model: profile.Model, Effort: profile.Effort}, nil
}
