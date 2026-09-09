package access

import (
	"bytes"
	"errors"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
)

// RequireTerminal validates the complete durable admission history and checks
// that the exact expected intent has the exact expected terminal receipt. This
// read-only snapshot grants no dispatch authority and performs no reconciliation.
func RequireTerminal(path string, policy Policy, expectedIntent Intent, expectedReceipt Receipt) error {
	id, err := expectedIntent.ID()
	if err != nil {
		return err
	}
	if id != expectedIntent.Reservation.InvocationID {
		return errors.New("invalid expected reservation")
	}
	if err := expectedReceipt.Validate(expectedIntent); err != nil {
		return err
	}
	events, err := journal.Read(path)
	if err != nil {
		return err
	}
	if err := replayAdmissions(events, policy); err != nil {
		return err
	}
	expectedIntentBytes, err := canonical.Bytes(expectedIntent)
	if err != nil {
		return err
	}
	expectedReceiptBytes, err := canonical.Bytes(expectedReceipt)
	if err != nil {
		return err
	}
	intentFound, receiptFound := false, false
	for _, event := range events {
		switch event.Kind {
		case "access.intent":
			var recorded Intent
			if err := canonical.Decode(event.Payload, &recorded); err != nil {
				return err
			}
			if recorded.Reservation.InvocationID != id {
				continue
			}
			recordedBytes, err := canonical.Bytes(recorded)
			if err != nil {
				return err
			}
			if !bytes.Equal(recordedBytes, expectedIntentBytes) {
				return errors.New("terminal reservation differs from expected intent")
			}
			intentFound = true
		case "access.receipt":
			var recorded Receipt
			if err := canonical.Decode(event.Payload, &recorded); err != nil {
				return err
			}
			if recorded.InvocationID != id {
				continue
			}
			recordedBytes, err := canonical.Bytes(recorded)
			if err != nil {
				return err
			}
			if !bytes.Equal(recordedBytes, expectedReceiptBytes) {
				return errors.New("terminal receipt differs from expected receipt")
			}
			receiptFound = true
		default:
			return errors.New("unknown admission event")
		}
	}
	if !intentFound {
		return errors.New("terminal reservation missing")
	}
	if !receiptFound {
		return errors.New("terminal receipt missing")
	}
	return nil
}
