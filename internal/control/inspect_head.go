package control

import (
	"errors"

	"harness.local/engorch/internal/journal"
)

// InspectWithHead returns a validated snapshot and its exact journal prefix
// from one read. A later append cannot change the returned snapshot's identity.
func InspectWithHead(path string) (Snapshot, string, error) {
	events, err := journal.Read(path)
	if err != nil {
		return Snapshot{}, "", err
	}
	if len(events) == 0 {
		return Snapshot{}, "", errors.New("controller journal prefix unavailable")
	}
	snapshot, err := Replay(events)
	if err != nil {
		return Snapshot{}, "", err
	}
	if err := validateControllerJournalPath(path, snapshot.Creation, snapshot.RunID); err != nil {
		return Snapshot{}, "", err
	}
	return snapshot, events[len(events)-1].Hash, nil
}
