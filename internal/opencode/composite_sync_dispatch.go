package opencode

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/toolreceipts"
)

// CompositeDispatchIntent binds a single POST to both authoritative context
// evidence and the exact composite transport journal. It grants no seal or
// process-lifecycle authority.
type CompositeDispatchIntent struct {
	Version      int                           `json:"version"`
	DispatchPath string                        `json:"dispatch_path"`
	Turn         SynchronousToolDispatchIntent `json:"turn"`
	BrokerPath   string                        `json:"broker_path"`
	ReceiptPath  string                        `json:"receipt_path"`
	Receipts     toolreceipts.Binding          `json:"receipts"`
}

type compositeDispatchRecord struct {
	Response          string `json:"response"`
	Transcript        string `json:"transcript"`
	ObservationSHA256 string `json:"observation_sha256"`
}

func validateCompositeDispatchIntent(intent CompositeDispatchIntent, fresh bool) error {
	if intent.Version != 1 || !filepath.IsAbs(intent.DispatchPath) || filepath.Clean(intent.DispatchPath) != intent.DispatchPath || strings.EqualFold(intent.DispatchPath, intent.BrokerPath) || strings.EqualFold(intent.DispatchPath, intent.ReceiptPath) || !filepath.IsAbs(intent.BrokerPath) || filepath.Clean(intent.BrokerPath) != intent.BrokerPath || !filepath.IsAbs(intent.ReceiptPath) || filepath.Clean(intent.ReceiptPath) != intent.ReceiptPath || strings.EqualFold(intent.BrokerPath, intent.ReceiptPath) {
		return errors.New("invalid composite dispatch paths or version")
	}
	id, err := intent.Receipts.ID()
	if err != nil || id != intent.Receipts.BindingID || intent.Receipts.InvocationID != intent.Turn.Invocation.ID {
		return errors.New("invalid composite dispatch receipt binding")
	}
	broker, err := contextbroker.Inspect(intent.BrokerPath)
	if err != nil {
		return err
	}
	if err := validateSynchronousToolIntent(intent.Turn, broker, fresh); err != nil {
		return err
	}
	state, err := toolreceipts.Inspect(intent.ReceiptPath)
	if err != nil || state.Binding == nil || !equalCanonical(*state.Binding, intent.Receipts) || (fresh && len(state.Calls) != 0) {
		return errors.Join(errors.New("composite dispatch receipt journal differs"), err)
	}
	return nil
}

func validateCompositeDispatchRecord(record compositeDispatchRecord, intent CompositeDispatchIntent, verify CompositeBackendVerifier) (CompositeToolTurnObservation, error) {
	var zero CompositeToolTurnObservation
	raw, err := canonical.Bytes(record)
	if err != nil || len(raw) > maxSynchronousEvidenceBytes || record.Response == "" || record.Transcript == "" {
		return zero, errors.New("invalid composite dispatch evidence bound")
	}
	observation, err := DecodeCompositeToolTurnWithRuntimeMetadata([]byte(record.Transcript), intent.Turn.Dispatch.Binding, intent.Turn.Dispatch.Text, intent.ReceiptPath, intent.Receipts, verify, intent.Turn.RuntimeMetadata)
	if err != nil {
		return zero, err
	}
	returned, text, err := decodeSynchronousResponse([]byte(record.Response), intent.Turn.Dispatch.Binding)
	if err != nil || returned != observation.Final || text != observation.Text {
		return zero, errors.New("composite response differs from transcript")
	}
	id, err := canonical.Hash("harness.opencode-composite-dispatch-observation.v1", observation)
	if err != nil || id != record.ObservationSHA256 {
		return zero, errors.New("composite dispatch observation changed")
	}
	return observation, nil
}

func replayCompositeDispatch(events []journal.Event, expected CompositeDispatchIntent, verify CompositeBackendVerifier) (*CompositeToolTurnObservation, error) {
	if len(events) < 1 || len(events) > 2 || events[0].Kind != "opencode.composite-dispatch-intent.v1" {
		return nil, errors.New("invalid composite dispatch journal")
	}
	var intent CompositeDispatchIntent
	if canonical.Decode(events[0].Payload, &intent) != nil || !equalCanonical(intent, expected) {
		return nil, errors.New("composite dispatch intent differs")
	}
	if len(events) == 1 {
		return nil, nil
	}
	if events[1].Kind != "opencode.composite-dispatch-observed.v1" {
		return nil, errors.New("invalid composite dispatch observation event")
	}
	var record compositeDispatchRecord
	if err := canonical.Decode(events[1].Payload, &record); err != nil {
		return nil, err
	}
	observation, err := validateCompositeDispatchRecord(record, intent, verify)
	if err != nil {
		return nil, err
	}
	return &observation, nil
}

// SubmitCompositeToolTurn durably claims one synchronous POST before sending
// it. Any partial attempt is recovery-only; this method never retries a POST.
func (c *Client) SubmitCompositeToolTurn(ctx context.Context, path string, intent CompositeDispatchIntent, verify CompositeBackendVerifier) (CompositeToolTurnObservation, error) {
	var zero CompositeToolTurnObservation
	intent.Receipts.Tools = append([]toolreceipts.ToolOwner(nil), intent.Receipts.Tools...)
	if path != intent.DispatchPath {
		return zero, errors.New("invalid composite dispatch journal path")
	}
	if err := requireSynchronousContext(ctx, c); err != nil {
		return zero, err
	}
	if verify == nil {
		return zero, errors.New("composite backend verifier required")
	}
	if err := validateCompositeDispatchIntent(intent, true); err != nil {
		return zero, err
	}
	_, err := journal.Append(path, "opencode.composite-dispatch-intent.v1", intent, func(events []journal.Event) error {
		if len(events) != 1 {
			return errors.New("composite dispatch already attempted")
		}
		_, err := replayCompositeDispatch(events, intent, verify)
		return err
	})
	if err != nil {
		return zero, err
	}
	body, err := synchronousPromptBody(intent.Turn.Dispatch)
	if err != nil {
		return zero, err
	}
	response, err := c.request(ctx, http.MethodPost, "/session/"+intent.Turn.Dispatch.Binding.SessionID+"/message", body)
	if err != nil {
		return zero, err
	}
	transcript, err := c.read(ctx, "/session/"+intent.Turn.Dispatch.Binding.SessionID+"/message")
	if err != nil {
		return zero, err
	}
	observation, err := DecodeCompositeToolTurnWithRuntimeMetadata(transcript, intent.Turn.Dispatch.Binding, intent.Turn.Dispatch.Text, intent.ReceiptPath, intent.Receipts, verify, intent.Turn.RuntimeMetadata)
	if err != nil {
		return zero, err
	}
	id, err := canonical.Hash("harness.opencode-composite-dispatch-observation.v1", observation)
	if err != nil {
		return zero, err
	}
	record := compositeDispatchRecord{string(response), string(transcript), id}
	if _, err := validateCompositeDispatchRecord(record, intent, verify); err != nil {
		return zero, err
	}
	_, err = journal.Append(path, "opencode.composite-dispatch-observed.v1", record, func(events []journal.Event) error {
		// Backend verification has already completed outside the transaction.
		// The append validator checks only the exact captured bytes and order.
		if len(events) != 2 || events[0].Kind != "opencode.composite-dispatch-intent.v1" || events[1].Kind != "opencode.composite-dispatch-observed.v1" {
			return errors.New("invalid composite dispatch append order")
		}
		var capturedIntent CompositeDispatchIntent
		var capturedRecord compositeDispatchRecord
		if canonical.Decode(events[0].Payload, &capturedIntent) != nil || canonical.Decode(events[1].Payload, &capturedRecord) != nil || !equalCanonical(capturedIntent, intent) || !equalCanonical(capturedRecord, record) {
			return errors.New("composite dispatch captured evidence differs")
		}
		return nil
	})
	if err != nil {
		return zero, err
	}
	return observation, nil
}

// RecoverCompositeToolTurn performs journal-only recovery. An intent without
// a recorded response remains unresolved and cannot authorize another POST.
func RecoverCompositeToolTurn(path string, expected CompositeDispatchIntent, verify CompositeBackendVerifier) (CompositeToolTurnObservation, error) {
	var zero CompositeToolTurnObservation
	expected.Receipts.Tools = append([]toolreceipts.ToolOwner(nil), expected.Receipts.Tools...)
	if path != expected.DispatchPath {
		return zero, errors.New("invalid composite dispatch journal path")
	}
	if verify == nil {
		return zero, errors.New("composite backend verifier required")
	}
	if err := validateCompositeDispatchIntent(expected, false); err != nil {
		return zero, err
	}
	events, err := journal.Read(path)
	if err != nil {
		return zero, err
	}
	observation, err := replayCompositeDispatch(events, expected, verify)
	if err != nil {
		return zero, err
	}
	if observation == nil {
		return zero, errors.New("composite dispatch unresolved")
	}
	return *observation, nil
}
