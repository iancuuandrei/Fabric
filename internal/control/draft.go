package control

import (
	"errors"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/draftpr"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/worktree"
)

// PreparedDraft binds the complete hosted proposal to its effect envelope.
type PreparedDraft struct {
	Plan   draftpr.Plan   `json:"plan"`
	Intent effects.Intent `json:"intent"`
}

// DraftIntent records operator authority before draft creation.
type DraftIntent struct {
	Prepared      PreparedDraft         `json:"prepared"`
	Authorization effects.Authorization `json:"authorization"`
}

// DraftObservation retains hosted evidence and local candidate freshness.
type DraftObservation struct {
	Remote    *draftpr.Observation `json:"remote"`
	Candidate *worktree.Candidate  `json:"candidate"`
	Error     string               `json:"error"`
}

// DraftReceipt binds classification to the observed hosted and local state.
type DraftReceipt struct {
	Receipt     effects.Receipt  `json:"receipt"`
	Observation DraftObservation `json:"observation"`
}

// DraftState retains the sole draft creation attempt and its reconciliation.
type DraftState struct {
	LeaseRecovery *DraftLeaseState `json:"lease_recovery,omitempty"`
	Intent        DraftIntent      `json:"intent"`
	Outcome       string           `json:"outcome"`
	Observation   *DraftReceipt    `json:"observation"`
}

func draftAllowed(s Snapshot) error {
	if s.State != "PUSHED" || s.Draft != nil || s.Push == nil || s.Push.Outcome != "CONFIRMED" || s.Workspace == nil || s.Candidate == nil || s.WorkspaceOutcome != "CONFIRMED" {
		return errors.New("draft requires confirmed push and no prior draft")
	}
	if s.Push.LeaseRecovery != nil && s.Push.LeaseRecovery.Outcome != "CONFIRMED" {
		return errors.New("push lease recovery unresolved")
	}
	return nil
}

func validatePreparedDraft(s Snapshot, p PreparedDraft) error {
	if err := draftAllowed(s); err != nil {
		return err
	}
	pushID, err := p.Plan.Push.ID()
	if err != nil {
		return err
	}
	expectedPushID, err := s.Push.Intent.Prepared.Plan.ID()
	if err != nil {
		return err
	}
	if pushID != expectedPushID || p.Plan.PushIntent != s.Push.Intent.Prepared.Intent || p.Plan.Push.Workspace != *s.Workspace || p.Plan.Push.Candidate != *s.Candidate {
		return errors.New("draft push or candidate substituted")
	}
	intent, err := p.Plan.Intent()
	if err != nil {
		return err
	}
	if intent != p.Intent || intent.RunID != s.RunID || intent.PlanID != s.PlanID {
		return errors.New("draft intent substituted")
	}
	return nil
}

func replayDraft(s *Snapshot, e journal.Event) error {
	if e.Kind == "draft.intent" {
		var intent DraftIntent
		if err := canonical.Decode(e.Payload, &intent); err != nil {
			return err
		}
		if err := validatePreparedDraft(*s, intent.Prepared); err != nil {
			return err
		}
		if err := intent.Authorization.Validate(intent.Prepared.Intent); err != nil {
			return err
		}
		s.Draft = &DraftState{Intent: intent, Outcome: "UNKNOWN"}
		s.State = "DRAFTING"
		return nil
	}
	if s.Draft == nil || s.Draft.Outcome != "UNKNOWN" || s.State != "DRAFTING" {
		return errors.New("pending draft required")
	}
	var receipt DraftReceipt
	if err := canonical.Decode(e.Payload, &receipt); err != nil {
		return err
	}
	o := receipt.Observation
	p := s.Draft.Intent.Prepared
	hash, err := canonical.Hash("harness.draft-observation.v1", o)
	if err != nil {
		return err
	}
	if hash != receipt.Receipt.ObservationHash {
		return errors.New("draft observation hash mismatch")
	}
	outcome, err := effects.Outcome(p.Intent, &receipt.Receipt)
	if err != nil {
		return err
	}
	if o.Remote != nil {
		if err := o.Remote.Validate(p.Plan); err != nil {
			return err
		}
	}
	if o.Candidate != nil && *o.Candidate != p.Plan.Push.Candidate {
		return errors.New("draft candidate observation mismatch")
	}
	if o.Remote != nil && o.Candidate != nil {
		if outcome != "CONFIRMED" || o.Error != "" {
			return errors.New("draft confirmation mismatch")
		}
		s.State = "HANDED_OFF"
	} else if outcome != "UNKNOWN" || o.Error != "draft final state could not be verified" {
		return errors.New("unresolved draft cannot prove non-execution")
	}
	s.Draft.Outcome = outcome
	s.Draft.Observation = &receipt
	return nil
}
