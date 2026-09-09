package access

import (
	"errors"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
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
	Attempt     int         `json:"attempt"`
	PolicyID    string      `json:"policy_id"`
	InputHash   string      `json:"input_hash"`
	Route       Route       `json:"route"`
	Reservation Reservation `json:"reservation"`
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
		for _, route := range policy.Routes {
			if route == intent.Route {
				allowed = true
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
