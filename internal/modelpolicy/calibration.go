package modelpolicy

import (
	"errors"
	"fmt"
	"regexp"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/canonical"
)

const (
	// CalibrationVersion is the persisted calibration format.
	CalibrationVersion = 1
	// CalibrationVersion2 is the explicitly opted-in reliability-constrained
	// cheaper-fixer format. It reuses the exact v1 row domain and adds frozen
	// epsilon/floor parameters. See economics.go for semantics.
	CalibrationVersion2 = 2
	// MaxQualityPPM bounds integer parts-per-million quality parameters.
	MaxQualityPPM = 1000000
	// MaxCalibrationRows bounds retained task-policy observations.
	MaxCalibrationRows = 64
	// MaxCalibrationFixerInvocations bounds routes retained for one task.
	MaxCalibrationFixerInvocations = 32
	// MaxCalibrationMinimumPerArm bounds an operator-declared evidence threshold.
	MaxCalibrationMinimumPerArm = 32
	// MaxCalibrationObjectiveBytes bounds a raw objective accepted for hashing.
	MaxCalibrationObjectiveBytes = 32 << 10
)

const (
	// OutcomeAccepted records an accepted whole-task result.
	OutcomeAccepted = "ACCEPTED"
	// OutcomeFailed records a completed whole-task result that was not accepted.
	OutcomeFailed = "FAILED"
	// OutcomeUnknown records a task with no known semantic result.
	OutcomeUnknown = "UNKNOWN"
	// OutcomeNotExercised records a task that did not invoke its assigned fixer.
	OutcomeNotExercised = "NOT_EXERCISED"
)

var calibrationHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

// EmpiricalUsage retains typed provider accounting for one task. Nil means
// unavailable, never zero. This type does not derive a cost from estimates.
type EmpiricalUsage struct {
	InputTokens       *int64 `json:"input_tokens,omitempty"`
	CachedInputTokens *int64 `json:"cached_input_tokens,omitempty"`
	OutputTokens      *int64 `json:"output_tokens,omitempty"`
	ReasoningTokens   *int64 `json:"reasoning_tokens,omitempty"`
	CostMicroUSD      *int64 `json:"cost_micro_usd,omitempty"`
}

// TaskPolicyOutcome is a complete task observed under a predeclared fixer
// profile. Repairs are task-level evidence, never invocation semantic results.
type TaskPolicyOutcome struct {
	SourceID              string          `json:"source_id"`
	TaskID                string          `json:"task_id"`
	RunID                 string          `json:"run_id"`
	ObjectiveHash         string          `json:"objective_hash"`
	NonFixerPolicyID      string          `json:"non_fixer_policy_id"`
	AssignedProfile       Profile         `json:"assigned_profile"`
	ObservedFixerProfiles []Profile       `json:"observed_fixer_profiles,omitempty"`
	FixerInvocations      int             `json:"fixer_invocations"`
	Outcome               string          `json:"outcome"`
	Repairs               int             `json:"repairs"`
	Usage                 *EmpiricalUsage `json:"usage,omitempty"`
}

// Calibration separates predeclared training and holdout task outcomes. Its
// minimum is an operator threshold, not a confidence or generalization claim.
// Version 1 carries no economics parameters. Version 2 adds explicit frozen
// epsilon/floor parts-per-million parameters for reliability-constrained
// cheaper-fixer selection; both must be present for version 2, including an
// explicit zero epsilon.
type Calibration struct {
	Version         int                 `json:"version"`
	FamilyID        string              `json:"family_id"`
	Role            string              `json:"role"`
	Objectives      []string            `json:"objective_hashes"`
	Baseline        Profile             `json:"baseline_profile"`
	Candidate       Profile             `json:"candidate_profile"`
	MinimumPerArm   int                 `json:"minimum_per_arm"`
	Training        []TaskPolicyOutcome `json:"training"`
	Holdout         []TaskPolicyOutcome `json:"holdout"`
	EpsilonPPM      *int                `json:"epsilon_ppm,omitempty"`
	QualityFloorPPM *int                `json:"quality_floor_ppm,omitempty"`
}

// ObjectiveDigest returns the exact objective binding used by calibration rows.
// It is a content hash, not a semantic task-family classifier.
func ObjectiveDigest(objective string) (string, error) {
	if len(objective) == 0 || len(objective) > MaxCalibrationObjectiveBytes || !utf8.ValidString(objective) {
		return "", errors.New("invalid calibration objective")
	}
	return canonical.Hash("harness.model-policy-objective.v1", struct {
		Objective string `json:"objective"`
	}{Objective: objective})
}

// CalibrationCounts summarizes task-level evidence for a configured profile.
type CalibrationCounts struct {
	Assigned     int `json:"assigned"`
	Accepted     int `json:"accepted"`
	Failed       int `json:"failed"`
	Unknown      int `json:"unknown"`
	NotExercised int `json:"not_exercised"`
	Drifted      int `json:"drifted"`
}

// CalibrationSelection is a pure conservative recommendation. Profile remains
// the baseline whenever Selected is false. Economics carries optional version 2
// posterior/quality/cost diagnostics; it is never authority beyond the frozen
// policy decision.
type CalibrationSelection struct {
	Version   int                          `json:"version"`
	Digest    string                       `json:"digest"`
	FamilyID  string                       `json:"family_id"`
	Role      string                       `json:"role"`
	Profile   Profile                      `json:"profile"`
	Selected  bool                         `json:"selected"`
	Reason    string                       `json:"reason"`
	Training  map[string]CalibrationCounts `json:"training"`
	Holdout   map[string]CalibrationCounts `json:"holdout"`
	Economics *EconomicsDiagnostics        `json:"economics,omitempty"`
}

// Digest returns the stable identity of a validated calibration artifact.
func (c Calibration) Digest() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	return canonical.Hash("harness.model-policy-calibration.v1", c)
}

// Validate checks bounded task-policy evidence. UNKNOWN and NOT_EXERCISED are
// retained as disqualifying observations rather than discarded.
func (c Calibration) Validate() error {
	if (c.Version != CalibrationVersion && c.Version != CalibrationVersion2) || !validIdentifier(c.FamilyID) || c.Role != "fixer" || c.MinimumPerArm < 1 || c.MinimumPerArm > MaxCalibrationMinimumPerArm {
		return errors.New("invalid model policy calibration")
	}
	if c.Version == CalibrationVersion {
		if c.EpsilonPPM != nil || c.QualityFloorPPM != nil {
			return errors.New("version 1 calibration carries no economics parameters")
		}
	} else {
		if c.EpsilonPPM == nil || c.QualityFloorPPM == nil || *c.EpsilonPPM < 0 || *c.EpsilonPPM > MaxQualityPPM || *c.QualityFloorPPM < 0 || *c.QualityFloorPPM > MaxQualityPPM {
			return errors.New("version 2 calibration requires bounded epsilon and floor")
		}
	}
	if err := c.Baseline.validate(); err != nil {
		return fmt.Errorf("invalid calibration baseline: %w", err)
	}
	if err := c.Candidate.validate(); err != nil || sameProfile(c.Baseline, c.Candidate) || c.Baseline.Name == c.Candidate.Name {
		return errors.New("invalid calibration candidate")
	}
	if len(c.Training) == 0 || len(c.Holdout) == 0 || len(c.Training)+len(c.Holdout) > MaxCalibrationRows || len(c.Objectives) == 0 || len(c.Objectives) > MaxCalibrationRows {
		return errors.New("invalid calibration row count")
	}
	seenRuns := map[string]struct{}{}
	trainingObjectives, err := c.validateCohort(c.Training, seenRuns)
	if err != nil {
		return err
	}
	holdoutObjectives, err := c.validateCohort(c.Holdout, seenRuns)
	if err != nil {
		return err
	}
	for objective := range holdoutObjectives {
		if _, exists := trainingObjectives[objective]; exists {
			return errors.New("calibration training and holdout overlap")
		}
	}
	declared := make(map[string]struct{}, len(c.Objectives))
	for index, objective := range c.Objectives {
		if !calibrationHash.MatchString(objective) || (index > 0 && c.Objectives[index-1] >= objective) {
			return errors.New("invalid calibration objective scope")
		}
		declared[objective] = struct{}{}
	}
	if len(declared) != len(trainingObjectives)+len(holdoutObjectives) {
		return errors.New("calibration objective scope does not match evidence")
	}
	for objective := range trainingObjectives {
		if _, exists := declared[objective]; !exists {
			return errors.New("calibration objective is outside declared scope")
		}
	}
	for objective := range holdoutObjectives {
		if _, exists := declared[objective]; !exists {
			return errors.New("calibration objective is outside declared scope")
		}
	}
	return nil
}

func (c Calibration) validateCohort(rows []TaskPolicyOutcome, runs map[string]struct{}) (map[string]struct{}, error) {
	arms := map[string]map[string]struct{}{c.Baseline.Name: {}, c.Candidate.Name: {}}
	objectives := map[string]struct{}{}
	for _, row := range rows {
		if !validIdentifier(row.SourceID) || !validIdentifier(row.TaskID) || !validIdentifier(row.RunID) || !calibrationHash.MatchString(row.ObjectiveHash) || !calibrationHash.MatchString(row.NonFixerPolicyID) || row.FixerInvocations < 0 || row.FixerInvocations > MaxCalibrationFixerInvocations || row.Repairs < 0 || row.Repairs > MaxFailures || len(row.ObservedFixerProfiles) != row.FixerInvocations {
			return nil, errors.New("invalid calibration task outcome")
		}
		if _, ok := runs[row.RunID]; ok {
			return nil, errors.New("duplicate calibration run")
		}
		runs[row.RunID] = struct{}{}
		if !sameProfile(row.AssignedProfile, c.Baseline) && !sameProfile(row.AssignedProfile, c.Candidate) {
			return nil, errors.New("calibration row has an unassigned profile")
		}
		key := calibrationTaskKey(row)
		if _, ok := arms[row.AssignedProfile.Name][key]; ok {
			return nil, errors.New("duplicate calibration task policy")
		}
		arms[row.AssignedProfile.Name][key] = struct{}{}
		if err := row.AssignedProfile.validate(); err != nil {
			return nil, errors.New("invalid calibration row profile")
		}
		for _, profile := range row.ObservedFixerProfiles {
			if err := profile.validate(); err != nil {
				return nil, errors.New("invalid observed fixer profile")
			}
		}
		switch row.Outcome {
		case OutcomeAccepted, OutcomeFailed:
			if row.FixerInvocations == 0 {
				return nil, errors.New("terminal calibration outcome did not exercise fixer")
			}
		case OutcomeNotExercised:
			if row.FixerInvocations != 0 {
				return nil, errors.New("not-exercised calibration outcome has fixer work")
			}
		case OutcomeUnknown:
		default:
			return nil, errors.New("invalid calibration outcome")
		}
		if err := validateEmpiricalUsage(row.Usage); err != nil {
			return nil, err
		}
		objectives[row.ObjectiveHash] = struct{}{}
	}
	return objectives, nil
}

func validateEmpiricalUsage(usage *EmpiricalUsage) error {
	if usage == nil {
		return nil
	}
	for _, value := range []*int64{usage.InputTokens, usage.CachedInputTokens, usage.OutputTokens, usage.ReasoningTokens, usage.CostMicroUSD} {
		if value != nil && *value < 0 {
			return errors.New("invalid empirical usage")
		}
	}
	if usage.InputTokens != nil && usage.CachedInputTokens != nil && *usage.CachedInputTokens > *usage.InputTokens {
		return errors.New("cached input exceeds input")
	}
	if usage.OutputTokens != nil && usage.ReasoningTokens != nil && *usage.ReasoningTokens > *usage.OutputTokens {
		return errors.New("reasoning exceeds output")
	}
	return nil
}

// EvaluateCalibration selects a fixer profile from bounded matched whole-task
// evidence. Version 1 selects the candidate only after strict observed
// whole-task quality improvement in both cohorts; cost never affects its
// selection. Version 2 delegates to the reliability-constrained cheaper-fixer
// rule in economics.go. Both versions share the exact row domain, disjoint
// matched task-policy bindings, and conservative UNKNOWN/no-exercise/drift
// fallbacks.
func EvaluateCalibration(c Calibration) (CalibrationSelection, error) {
	digest, err := c.Digest()
	if err != nil {
		return CalibrationSelection{}, err
	}
	result := CalibrationSelection{Version: c.Version, Digest: digest, FamilyID: c.FamilyID, Role: c.Role, Profile: copyProfile(c.Baseline), Reason: "fallback_insufficient_quality_evidence", Training: countsFor(c.Training, c), Holdout: countsFor(c.Holdout, c)}
	if c.Version == CalibrationVersion2 {
		return evaluateCheaperFixer(c, result), nil
	}
	if !matchedTaskPolicies(c.Training, c) || !matchedTaskPolicies(c.Holdout, c) {
		result.Reason = "fallback_unmatched_task_policy"
		return result, nil
	}
	for _, cohort := range []map[string]CalibrationCounts{result.Training, result.Holdout} {
		for _, profile := range []Profile{c.Baseline, c.Candidate} {
			value := cohort[profile.Name]
			if value.Unknown > 0 {
				result.Reason = "fallback_unknown_outcome"
				return result, nil
			}
			if value.NotExercised > 0 {
				result.Reason = "fallback_fixer_not_exercised"
				return result, nil
			}
			if value.Drifted > 0 {
				result.Reason = "fallback_fixer_route_drift"
				return result, nil
			}
			if value.Accepted+value.Failed < c.MinimumPerArm {
				return result, nil
			}
		}
	}
	if !strictlyBetter(result.Training[c.Candidate.Name], result.Training[c.Baseline.Name]) || !strictlyBetter(result.Holdout[c.Candidate.Name], result.Holdout[c.Baseline.Name]) {
		result.Reason = "fallback_no_quality_improvement"
		return result, nil
	}
	result.Profile, result.Selected, result.Reason = copyProfile(c.Candidate), true, "candidate_quality_improved_train_and_holdout"
	return result, nil
}

func matchedTaskPolicies(rows []TaskPolicyOutcome, c Calibration) bool {
	baseline := make(map[string]string)
	candidate := make(map[string]string)
	for _, row := range rows {
		key := calibrationTaskKey(row)
		if sameProfile(row.AssignedProfile, c.Baseline) {
			baseline[key] = row.NonFixerPolicyID
		} else {
			candidate[key] = row.NonFixerPolicyID
		}
	}
	if len(baseline) != len(candidate) {
		return false
	}
	for key, policy := range baseline {
		if candidate[key] != policy {
			return false
		}
	}
	return true
}

func calibrationTaskKey(row TaskPolicyOutcome) string {
	return row.SourceID + "\x00" + row.TaskID + "\x00" + row.ObjectiveHash
}

func countsFor(rows []TaskPolicyOutcome, c Calibration) map[string]CalibrationCounts {
	counts := map[string]CalibrationCounts{c.Baseline.Name: {}, c.Candidate.Name: {}}
	for _, row := range rows {
		value := counts[row.AssignedProfile.Name]
		value.Assigned++
		for _, observed := range row.ObservedFixerProfiles {
			if !sameProfile(observed, row.AssignedProfile) {
				value.Drifted++
				break
			}
		}
		switch row.Outcome {
		case OutcomeAccepted:
			value.Accepted++
		case OutcomeFailed:
			value.Failed++
		case OutcomeUnknown:
			value.Unknown++
		case OutcomeNotExercised:
			value.NotExercised++
		}
		counts[row.AssignedProfile.Name] = value
	}
	return counts
}

func strictlyBetter(candidate, baseline CalibrationCounts) bool {
	return int64(candidate.Accepted)*int64(baseline.Accepted+baseline.Failed) > int64(baseline.Accepted)*int64(candidate.Accepted+candidate.Failed)
}
func sameProfile(left, right Profile) bool {
	return left.Name == right.Name && left.Runtime == right.Runtime && left.Provider == right.Provider && left.Model == right.Model && left.Effort == right.Effort && sameInt64(left.ExpectedCostMicroUSD, right.ExpectedCostMicroUSD) && sameInt64(left.ExpectedLatencyMillis, right.ExpectedLatencyMillis)
}
func sameInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}
