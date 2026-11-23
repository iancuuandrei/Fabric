package access

import (
	"errors"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/safepath"
)

// Policy binds one run's admission scope. Routes and profiles are selected by the
// controller, never by the requesting worker. The list order is identity-bearing.
type Policy struct {
	Version  int       `json:"version"`
	RunID    string    `json:"run_id"`
	Class    Class     `json:"class"`
	Limits   Limits    `json:"limits"`
	Routes   []Route   `json:"routes"`
	Profiles []Profile `json:"profiles"`
}

// ID validates the complete admission policy before hashing it.
func (p Policy) ID() (string, error) {
	if p.Version != 1 || safepath.RequireDigest(p.RunID) != nil || len(p.Routes) == 0 || len(p.Routes) > 5 || len(p.Profiles) == 0 || len(p.Profiles) > 32 {
		return "", errors.New("invalid admission policy")
	}
	if _, err := NewLedger(p.Limits); err != nil {
		return "", err
	}
	profiles := map[string]Profile{}
	names := map[string]bool{}
	for _, profile := range p.Profiles {
		id, err := profile.ID()
		if err != nil {
			return "", err
		}
		if names[profile.Name] {
			return "", errors.New("duplicate access profile")
		}
		names[profile.Name] = true
		profiles[id] = profile
	}
	roles := map[string]bool{}
	for _, route := range p.Routes {
		if roles[route.Role] {
			return "", errors.New("duplicate admission role")
		}
		roles[route.Role] = true
		if err := AdmitRoute(route, route, profiles[route.AccessID], p.Class); err != nil {
			return "", err
		}
	}
	return canonical.Hash("harness.access-policy.v1", p)
}

// Intent stores hashes and resource ceilings, not raw prompts or credentials.
type Intent struct {
	Attempt         int              `json:"attempt"`
	PolicyID        string           `json:"policy_id"`
	InputHash       string           `json:"input_hash"`
	Route           Route            `json:"route"`
	ModelChoice     *ModelChoice     `json:"model_choice,omitempty"`
	RoutingDecision *RoutingDecision `json:"routing_decision,omitempty"`
	Reservation     Reservation      `json:"reservation"`
}

// RoutingDecision is optional, non-authoritative evidence for an explicitly
// opted-in model policy. The selected model and effort remain authoritative in
// Route and ModelChoice; this projection records why that existing route was
// selected.
type RoutingDecision struct {
	Version          int    `json:"version"`
	ConfigID         string `json:"config_id"`
	ProfileName      string `json:"profile_name"`
	Model            string `json:"model"`
	Effort           string `json:"effort"`
	Reason           string `json:"reason"`
	ContextBytes     int64  `json:"context_bytes"`
	AcceptedFailures int    `json:"accepted_failures"`
	// ObjectiveHash is present only in evidence version 2 and binds the
	// immutable task objective without retaining its text.
	ObjectiveHash string `json:"objective_hash,omitempty"`
	// Calibration is present only for an explicitly calibrated fixer decision.
	Calibration *CalibrationDecision `json:"calibration,omitempty"`
}

// CalibrationOutcomeCounts is a compact projection of one arm's observed
// whole-task outcomes. It contains counts only, not task identifiers or output.
type CalibrationOutcomeCounts struct {
	Assigned     int `json:"assigned"`
	Accepted     int `json:"accepted"`
	Failed       int `json:"failed"`
	Unknown      int `json:"unknown"`
	NotExercised int `json:"not_exercised"`
	Drifted      int `json:"drifted"`
}

// CalibrationArmCounts distinguishes the predeclared baseline and candidate.
type CalibrationArmCounts struct {
	Baseline  CalibrationOutcomeCounts `json:"baseline"`
	Candidate CalibrationOutcomeCounts `json:"candidate"`
}

// CalibrationDecision binds an opt-in fixer selection to the exact calibration
// artifact and reports whether an empirically selected candidate was applied.
type CalibrationDecision struct {
	Digest            string               `json:"digest"`
	FamilyID          string               `json:"family_id"`
	BaselineProfile   string               `json:"baseline_profile"`
	CandidateProfile  string               `json:"candidate_profile"`
	SelectionReason   string               `json:"selection_reason"`
	ScopeMatched      bool                 `json:"scope_matched"`
	CandidateSelected bool                 `json:"candidate_selected"`
	CandidateApplied  bool                 `json:"candidate_applied"`
	Training          CalibrationArmCounts `json:"training"`
	Holdout           CalibrationArmCounts `json:"holdout"`
}

// Validate binds a compact routing observation to the route already admitted
// by this intent. It does not authorize or select a model by itself.
func (d RoutingDecision) Validate(route Route, choice *ModelChoice) error {
	if (d.Version != 1 && d.Version != 2) || safepath.RequireDigest(d.ConfigID) != nil || !identifier(d.ProfileName) || !validModelName(d.Model) || !identifier(d.Effort) || d.ContextBytes < 0 || d.ContextBytes > 256<<10 || d.AcceptedFailures < 0 || d.AcceptedFailures > 1_000_000 {
		return errors.New("invalid routing decision evidence")
	}
	switch d.Reason {
	case "default-configured-profile", "architect-role", "high-risk", "high-complexity", "high-uncertainty", "large-context", "large-input-bytes", "prior-failures", "bounded-read-only-input", "calibrated-candidate", "calibration-objective-out-of-scope":
	default:
		return errors.New("invalid routing decision reason")
	}
	if d.Version == 1 {
		if d.ObjectiveHash != "" || d.Calibration != nil {
			return errors.New("legacy routing evidence contains calibration fields")
		}
	} else {
		if safepath.RequireDigest(d.ObjectiveHash) != nil {
			return errors.New("version 2 routing evidence lacks an objective digest")
		}
		if route.Role == "fixer" {
			if d.Calibration == nil || d.Calibration.Validate() != nil {
				return errors.New("version 2 fixer evidence lacks valid calibration projection")
			}
			if (d.Reason == "calibrated-candidate") != d.Calibration.CandidateApplied {
				return errors.New("calibrated route reason differs from applied candidate")
			}
		} else if d.Calibration != nil || d.Reason == "calibrated-candidate" || d.Reason == "calibration-objective-out-of-scope" {
			return errors.New("non-fixer routing evidence contains calibration selection")
		}
	}
	model, effort := route.Model, route.Effort
	if choice != nil {
		model, effort = choice.Model, choice.Effort
	}
	if d.Model != model || d.Effort != effort {
		return errors.New("routing decision differs from admitted model choice")
	}
	return nil
}

// Validate checks that the compact selection projection is internally consistent.
func (d CalibrationDecision) Validate() error {
	if safepath.RequireDigest(d.Digest) != nil || !identifier(d.FamilyID) || !identifier(d.BaselineProfile) || !identifier(d.CandidateProfile) || d.BaselineProfile == d.CandidateProfile {
		return errors.New("invalid calibration decision identity")
	}
	switch d.SelectionReason {
	case "candidate_quality_improved_train_and_holdout", "candidate_cheaper_reliable_train_and_holdout", "fallback_insufficient_quality_evidence", "fallback_unmatched_task_policy", "fallback_unknown_outcome", "fallback_fixer_not_exercised", "fallback_fixer_route_drift", "fallback_no_quality_improvement", "fallback_missing_cost", "fallback_cost_overflow", "fallback_cost_not_lower", "fallback_quality_below_required", "fallback_quality_below_floor", "fallback_objective_out_of_scope":
	default:
		return errors.New("invalid calibration selection reason")
	}
	selectedByReason := d.SelectionReason == "candidate_quality_improved_train_and_holdout" || d.SelectionReason == "candidate_cheaper_reliable_train_and_holdout"
	objectiveOutOfScope := d.SelectionReason == "fallback_objective_out_of_scope"
	if d.CandidateSelected != selectedByReason || d.CandidateApplied && (!d.CandidateSelected || !d.ScopeMatched) || objectiveOutOfScope == d.ScopeMatched {
		return errors.New("inconsistent calibration selection flags")
	}
	if err := d.Training.Validate(); err != nil {
		return err
	}
	return d.Holdout.Validate()
}

// Validate checks each calibration arm's bounded outcome totals.
func (c CalibrationArmCounts) Validate() error {
	if err := c.Baseline.Validate(); err != nil {
		return err
	}
	return c.Candidate.Validate()
}

// Validate checks bounded count totals without interpreting them as provider
// calls or independent outcomes.
func (c CalibrationOutcomeCounts) Validate() error {
	if c.Assigned < 0 || c.Assigned > 64 || c.Accepted < 0 || c.Failed < 0 || c.Unknown < 0 || c.NotExercised < 0 || c.Drifted < 0 || c.Drifted > c.Assigned {
		return errors.New("invalid calibration outcome counts")
	}
	if c.Accepted+c.Failed+c.Unknown+c.NotExercised != c.Assigned {
		return errors.New("calibration outcome counts do not sum to assigned tasks")
	}
	return nil
}

// ID derives invocation identity from all intent fields except the derived
// reservation ID itself. Attempts distinguish explicitly authorized repetitions;
// changing the attempt does not by itself authorize another dispatch.
func (i Intent) ID() (string, error) {
	if i.Attempt < 1 || i.Attempt > 1000000 || safepath.RequireDigest(i.PolicyID) != nil || safepath.RequireDigest(i.InputHash) != nil {
		return "", errors.New("invalid invocation intent identity")
	}
	if err := i.Route.Validate(); err != nil {
		return "", err
	}
	if !i.Route.AllowsModelChoice(i.ModelChoice) {
		return "", errors.New("adaptive model choice is not admitted by route")
	}
	if i.RoutingDecision != nil {
		if err := i.RoutingDecision.Validate(i.Route, i.ModelChoice); err != nil {
			return "", err
		}
	}
	i.Reservation.InvocationID = ""
	return canonical.Hash("harness.access-invocation.v1", i)
}

func replayAdmissions(events []journal.Event, policy Policy) error {
	id, err := policy.ID()
	if err != nil {
		return err
	}
	ledger, _ := NewLedger(policy.Limits)
	intents := map[string]Intent{}
	profileActive := map[string]int{}
	profileLimits := map[string]int{}
	for _, profile := range policy.Profiles {
		profileID, _ := profile.ID()
		profileLimits[profileID] = profile.MaxConcurrentInvocations
	}
	for _, event := range events {
		if event.Kind == "access.receipt" {
			var receipt Receipt
			if err := canonical.Decode(event.Payload, &receipt); err != nil {
				return err
			}
			intent, ok := intents[receipt.InvocationID]
			if !ok {
				return errors.New("receipt without admission")
			}
			if err := receipt.Validate(intent); err != nil {
				return err
			}
			if err := ledger.Complete(receipt.InvocationID); err != nil {
				return err
			}
			profileActive[intent.Route.AccessID]--
			continue
		}
		if event.Kind != "access.intent" {
			return errors.New("unknown admission event")
		}
		var intent Intent
		if err := canonical.Decode(event.Payload, &intent); err != nil {
			return err
		}
		if intent.PolicyID != id || safepath.RequireDigest(intent.InputHash) != nil {
			return errors.New("admission identity mismatch")
		}
		expected, err := intent.ID()
		if err != nil || expected != intent.Reservation.InvocationID {
			return errors.New("invocation reservation identity mismatch")
		}
		allowed := false
		intentRouteID, routeErr := intent.Route.ID()
		for _, route := range policy.Routes {
			routeID, err := route.ID()
			if err == nil && routeErr == nil && routeID == intentRouteID {
				allowed = route.AllowsModelChoice(intent.ModelChoice)
			}
		}
		if !allowed {
			return errors.New("route not authorized by policy")
		}
		for _, profile := range policy.Profiles {
			profileID, _ := profile.ID()
			if profileID == intent.Route.AccessID && profile.Kind != intent.Reservation.BillingMode {
				return errors.New("reservation billing mismatch")
			}
		}
		if limit := profileLimits[intent.Route.AccessID]; limit > 0 && profileActive[intent.Route.AccessID] >= limit {
			return errors.New("access profile concurrency limit reached")
		}
		if err := ledger.Reserve(intent.Reservation); err != nil {
			return err
		}
		profileActive[intent.Route.AccessID]++
		intents[intent.Reservation.InvocationID] = intent
	}
	return nil
}

// ReserveDurable validates full history and appends a synced intent under the
// journal's exclusive lock. Any returned error forbids dispatch; a write error
// may leave a reservation and requires inspection rather than blind retry.
// Intents stay active until a valid terminal receipt is recorded.
func ReserveDurable(path string, policy Policy, intent Intent) error {
	_, err := journal.Append(path, "access.intent", intent, func(events []journal.Event) error {
		return replayAdmissions(events, policy)
	})
	return err
}
