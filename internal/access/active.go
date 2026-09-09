package access

import (
	"bytes"
	"errors"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
)

// RequireActive validates the entire durable ledger and checks that the exact
// reservation exists without a terminal receipt. This is a snapshot, not a new
// reservation or dispatch lock. The controller must serialize completion and
// dispatch under its invocation ownership; this read alone grants no authority.
func RequireActive(path string, policy Policy, expected Intent) error {
	id, err := expected.ID()
	if err != nil {
		return err
	}
	if id != expected.Reservation.InvocationID {
		return errors.New("invalid expected reservation")
	}
	events, err := journal.Read(path)
	if err != nil {
		return err
	}
	if err := replayAdmissions(events, policy); err != nil {
		return err
	}
	expectedBytes, err := canonical.Bytes(expected)
	if err != nil {
		return err
	}
	found := false
	for _, e := range events {
		switch e.Kind {
		case "access.intent":
			var i Intent
			if err := canonical.Decode(e.Payload, &i); err != nil {
				return err
			}
			if i.Reservation.InvocationID == id {
				recordedBytes, err := canonical.Bytes(i)
				if err != nil {
					return err
				}
				if !bytes.Equal(recordedBytes, expectedBytes) {
					return errors.New("active reservation differs from expected intent")
				}
				found = true
			}
		case "access.receipt":
			var r Receipt
			if err := canonical.Decode(e.Payload, &r); err != nil {
				return err
			}
			if r.InvocationID == id {
				return errors.New("reservation is terminal")
			}
		default:
			return errors.New("unknown admission event")
		}
	}
	if !found {
		return errors.New("active reservation missing")
	}
	return nil
}
