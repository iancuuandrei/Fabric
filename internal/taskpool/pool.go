// Package taskpool provides durable all-applicable concurrency admission.
// The capacity algorithm is adapted from harms-haus/pi-subagent-tasks pools.ts
// at 2bae805c1a0bdd97e699e2c9601fb4e6624f6f53 (MIT). See third_party/pi-subagent-tasks.
// It grants scheduler capacity, never model, repository or effect authority.
package taskpool

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
)

// ModelKey keeps exact provider and model identities separate, avoiding slash collisions.
type ModelKey struct {
	Provider string `toml:"provider" json:"provider"`
	Model    string `toml:"model" json:"model"`
}

// ModelLimit applies to one exact provider/model pair.
type ModelLimit struct {
	Key ModelKey `toml:"key" json:"key"`
	Cap int      `toml:"cap" json:"cap"`
}

// Limits fixes total and optional profile/provider/model ceilings for one shared pool.
type Limits struct {
	Total     int            `toml:"total" json:"total"`
	Profiles  map[string]int `toml:"profiles" json:"profiles,omitempty"`
	Providers map[string]int `toml:"providers" json:"providers,omitempty"`
	Models    []ModelLimit   `toml:"models" json:"models,omitempty"`
}

// Request identifies one immutable attempt; IDs cannot be reused after release.
type Request struct {
	ID              string `json:"id"`
	RunID           string `json:"run_id"`
	AccessProfileID string `json:"access_profile_id"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
}

// Release records capacity settlement linked to controller-validated terminal evidence.
type Release struct {
	ID            string `json:"id"`
	ReceiptSHA256 string `json:"receipt_sha256"`
}

// Snapshot is reconstructed accounting, not independent dispatch authority.
type Snapshot struct {
	Limits  *Limits
	Active  map[string]Request
	Settled map[string]Release
}

// ErrCapacity means at least one applicable ceiling denied acquisition without mutation.
var ErrCapacity = errors.New("task pool capacity unavailable")

func validCap(n int) bool { return n > 0 && n <= 100000 }
func validName(s string) bool {
	return len(s) > 0 && len(s) <= 512 && utf8.ValidString(s) && strings.TrimSpace(s) == s && strings.IndexFunc(s, unicode.IsControl) < 0
}

// Validate rejects malformed or unbounded pool configuration before admission.
func (l Limits) Validate() error { return l.validate() }

func (l Limits) validate() error {
	if !validCap(l.Total) || len(l.Profiles)+len(l.Providers)+len(l.Models) > 4096 {
		return errors.New("invalid pool limits")
	}
	for _, values := range []map[string]int{l.Profiles, l.Providers} {
		for name, cap := range values {
			if !validName(name) || !validCap(cap) {
				return errors.New("invalid scoped limit")
			}
		}
	}
	seen := map[ModelKey]bool{}
	for _, limit := range l.Models {
		if !validName(limit.Key.Provider) || !validName(limit.Key.Model) || !validCap(limit.Cap) || seen[limit.Key] {
			return errors.New("invalid model limit")
		}
		seen[limit.Key] = true
	}
	return nil
}
func (r Request) validate() error {
	for _, id := range []string{r.ID, r.RunID, r.AccessProfileID} {
		if safepath.RequireDigest(id) != nil {
			return errors.New("invalid pool request identity")
		}
	}
	if !validName(r.Provider) || !validName(r.Model) {
		return errors.New("exact provider/model required")
	}
	return nil
}
func room(s Snapshot, r Request) bool {
	l := s.Limits
	if len(s.Active) >= l.Total {
		return false
	}
	profile, provider, model := 0, 0, 0
	for _, active := range s.Active {
		if active.AccessProfileID == r.AccessProfileID {
			profile++
		}
		if active.Provider == r.Provider {
			provider++
			if active.Model == r.Model {
				model++
			}
		}
	}
	if cap, ok := l.Profiles[r.AccessProfileID]; ok && profile >= cap {
		return false
	}
	if cap, ok := l.Providers[r.Provider]; ok && provider >= cap {
		return false
	}
	for _, limit := range l.Models {
		if limit.Key == (ModelKey{r.Provider, r.Model}) && model >= limit.Cap {
			return false
		}
	}
	return true
}

// Replay validates the pool history; unresolved attempts retain all their slots.
func Replay(events []journal.Event) (Snapshot, error) {
	s := Snapshot{Active: map[string]Request{}, Settled: map[string]Release{}}
	for i, event := range events {
		switch event.Kind {
		case "pool.bound":
			var l Limits
			if i != 0 || canonical.Decode(event.Payload, &l) != nil || l.validate() != nil {
				return Snapshot{}, errors.New("invalid pool binding")
			}
			s.Limits = &l
		case "pool.acquired":
			var r Request
			if s.Limits == nil || canonical.Decode(event.Payload, &r) != nil || r.validate() != nil {
				return Snapshot{}, errors.New("invalid pool acquisition")
			}
			if _, ok := s.Active[r.ID]; ok {
				return Snapshot{}, errors.New("duplicate pool attempt")
			}
			if _, ok := s.Settled[r.ID]; ok {
				return Snapshot{}, errors.New("settled pool attempt reused")
			}
			if !room(s, r) {
				return Snapshot{}, ErrCapacity
			}
			s.Active[r.ID] = r
		case "pool.released":
			var r Release
			if canonical.Decode(event.Payload, &r) != nil || safepath.RequireDigest(r.ReceiptSHA256) != nil {
				return Snapshot{}, errors.New("invalid pool release")
			}
			if _, ok := s.Active[r.ID]; !ok {
				return Snapshot{}, errors.New("release lacks active acquisition")
			}
			delete(s.Active, r.ID)
			s.Settled[r.ID] = r
		default:
			return Snapshot{}, errors.New("unknown pool event")
		}
	}
	return s, nil
}

// Inspect replays a durable shared pool without changing it.
func Inspect(path string) (Snapshot, error) {
	return InspectContext(context.Background(), path)
}

// InspectContext replays the shared pool under the caller's cancellation budget.
// It does not create a missing journal or change capacity.
func InspectContext(ctx context.Context, path string) (Snapshot, error) {
	events, err := journal.ReadContext(ctx, path)
	if err != nil {
		return Snapshot{}, err
	}
	return Replay(events)
}
func appendEvent(path, kind string, payload any) error {
	_, err := journal.Append(path, kind, payload, func(events []journal.Event) error { _, err := Replay(events); return err })
	return err
}

// Bind creates a fixed pool or verifies its existing exact limits.
func Bind(path string, limits Limits) error {
	if err := limits.validate(); err != nil {
		return err
	}
	s, err := Inspect(path)
	if err != nil {
		return err
	}
	if s.Limits != nil {
		observed, observedErr := canonical.Hash("harness.taskpool-limits.v1", *s.Limits)
		wanted, wantedErr := canonical.Hash("harness.taskpool-limits.v1", limits)
		if observedErr != nil || wantedErr != nil || observed != wanted {
			return errors.New("pool limits changed")
		}
		return nil
	}
	return appendEvent(path, "pool.bound", limits)
}

// Acquire reserves all applicable slots atomically across processes. A write error
// requires inspection before retry; it does not establish whether capacity was reserved.
func Acquire(path string, request Request) error {
	if err := request.validate(); err != nil {
		return err
	}
	return appendEvent(path, "pool.acquired", request)
}

// Settle releases an exact attempt once. The controller must validate the receipt
// and absence of running work before calling; this layer cannot attest external effects.
func Settle(path string, release Release) error { return appendEvent(path, "pool.released", release) }
