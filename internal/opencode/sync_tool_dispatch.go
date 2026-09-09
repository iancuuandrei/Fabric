package opencode

import (
	"context"
	"errors"
	"net/http"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/safepath"
)

// SynchronousToolDispatchIntent binds one immutable runtime invocation and its
// exact OpenCode locators to a controller-owned context broker and catalog.
type SynchronousToolDispatchIntent struct {
	Invocation      runtime.Invocation          `json:"invocation"`
	Dispatch        DispatchIntent              `json:"dispatch"`
	BrokerBindingID string                      `json:"broker_binding_id"`
	BrokerCatalogID string                      `json:"broker_catalog_id"`
	RuntimeMetadata *RuntimeMetadataExpectation `json:"runtime_metadata,omitempty"`
}

type synchronousToolRecord struct {
	Response        string `json:"response,omitempty"`
	Transcript      string `json:"transcript"`
	BrokerBindingID string `json:"broker_binding_id"`
	BrokerCatalogID string `json:"broker_catalog_id"`
	BrokerStateID   string `json:"broker_state_id"`
}

type synchronousToolState struct {
	Intent      *SynchronousToolDispatchIntent
	Observation *ToolTurnObservation
}

func replaySynchronousToolDispatch(events []journal.Event, broker contextbroker.State) (synchronousToolState, error) {
	state := synchronousToolState{}
	for _, event := range events {
		switch event.Kind {
		case "opencode.sync-tool-intent":
			if state.Intent != nil {
				return synchronousToolState{}, errors.New("duplicate synchronous tool intent")
			}
			var intent SynchronousToolDispatchIntent
			if err := canonical.Decode(event.Payload, &intent); err != nil {
				return synchronousToolState{}, err
			}
			if err := validateSynchronousToolIntent(intent, broker, false); err != nil {
				return synchronousToolState{}, err
			}
			state.Intent = &intent
		case "opencode.sync-tool-observed":
			if state.Intent == nil || state.Observation != nil {
				return synchronousToolState{}, errors.New("invalid synchronous tool observation")
			}
			var record synchronousToolRecord
			if err := canonical.Decode(event.Payload, &record); err != nil {
				return synchronousToolState{}, err
			}
			observation, err := validateSynchronousToolRecord(record, *state.Intent, broker)
			if err != nil {
				return synchronousToolState{}, err
			}
			state.Observation = &observation
		default:
			return synchronousToolState{}, errors.New("unknown synchronous tool event")
		}
	}
	return state, nil
}

func appendSynchronousToolDispatch(path, kind string, payload any, broker contextbroker.State) error {
	_, err := journal.Append(path, kind, payload, func(events []journal.Event) error {
		_, err := replaySynchronousToolDispatch(events, broker)
		return err
	})
	return err
}

// SubmitSynchronousToolTurn records the exact invocation, dispatch and broker
// identities before one synchronous POST. It independently validates the final
// response and a full transcript against the post-call broker journal. Errors
// are unresolved and must never trigger an automatic resubmission.
func (c *Client) SubmitSynchronousToolTurn(ctx context.Context, path, brokerPath string, intent SynchronousToolDispatchIntent) (ToolTurnObservation, error) {
	if err := requireSynchronousContext(ctx, c); err != nil {
		return ToolTurnObservation{}, err
	}
	broker, err := contextbroker.Inspect(brokerPath)
	if err != nil {
		return ToolTurnObservation{}, err
	}
	if err := validateSynchronousToolIntent(intent, broker, true); err != nil {
		return ToolTurnObservation{}, err
	}
	if err := appendSynchronousToolDispatch(path, "opencode.sync-tool-intent", intent, broker); err != nil {
		return ToolTurnObservation{}, err
	}
	body, err := synchronousPromptBody(intent.Dispatch)
	if err != nil {
		return ToolTurnObservation{}, err
	}
	response, err := c.request(ctx, http.MethodPost, "/session/"+intent.Dispatch.Binding.SessionID+"/message", body)
	if err != nil {
		return ToolTurnObservation{}, err
	}
	var returned synchronousResponseObservation
	if intent.Dispatch.StructuredOutput != nil {
		returned, err = decodeSynchronousResponseWithStructuredOutput(response, intent.Dispatch.Binding, *intent.Dispatch.StructuredOutput)
	} else {
		var text string
		returned.Assistant, text, err = decodeSynchronousResponse(response, intent.Dispatch.Binding)
		returned.Text = text
	}
	if err != nil {
		return ToolTurnObservation{}, err
	}
	broker, err = contextbroker.Inspect(brokerPath)
	if err != nil {
		return ToolTurnObservation{}, err
	}
	if err := validateSynchronousToolIntent(intent, broker, false); err != nil {
		return ToolTurnObservation{}, err
	}
	transcript, err := c.read(ctx, "/session/"+intent.Dispatch.Binding.SessionID+"/message")
	if err != nil {
		return ToolTurnObservation{}, err
	}
	var observation ToolTurnObservation
	if intent.Dispatch.StructuredOutput != nil {
		observation, err = decodeToolTurnWithStructuredOutputAndRuntimeMetadata(transcript, intent.Dispatch.Binding, intent.Dispatch.Text, broker, intent.Dispatch.StructuredOutput, intent.RuntimeMetadata)
	} else {
		observation, err = decodeToolTurnWithRuntimeMetadata(transcript, intent.Dispatch.Binding, intent.Dispatch.Text, broker, intent.RuntimeMetadata)
	}
	if err != nil {
		return ToolTurnObservation{}, err
	}
	if !synchronousResponseMatchesToolObservation(returned, observation) {
		return ToolTurnObservation{}, errors.New("synchronous tool response and transcript differ")
	}
	record := synchronousToolRecord{
		Response: string(response), Transcript: string(transcript),
		BrokerBindingID: observation.BrokerBindingID, BrokerCatalogID: intent.BrokerCatalogID,
		BrokerStateID: observation.BrokerStateID,
	}
	if err := validateSynchronousToolEvidenceBound(record); err != nil {
		return ToolTurnObservation{}, err
	}
	if err := appendSynchronousToolDispatch(path, "opencode.sync-tool-observed", record, broker); err != nil {
		return ToolTurnObservation{}, err
	}
	return observation, nil
}

// RecoverSynchronousToolTurn never posts. It validates cached raw evidence
// against the current broker journal, or performs one GET-only observation for
// an unresolved exact intent. The result is not owned-host terminal evidence.
func (c *Client) RecoverSynchronousToolTurn(ctx context.Context, path, brokerPath string, expected SynchronousToolDispatchIntent) (ToolTurnObservation, error) {
	broker, err := contextbroker.Inspect(brokerPath)
	if err != nil {
		return ToolTurnObservation{}, err
	}
	if err := validateSynchronousToolIntent(expected, broker, false); err != nil {
		return ToolTurnObservation{}, err
	}
	events, err := journal.Read(path)
	if err != nil {
		return ToolTurnObservation{}, err
	}
	state, err := replaySynchronousToolDispatch(events, broker)
	if err != nil {
		return ToolTurnObservation{}, err
	}
	if state.Intent == nil || !equalSynchronousToolIntent(*state.Intent, expected) {
		return ToolTurnObservation{}, errors.New("synchronous tool dispatch intent mismatch")
	}
	if state.Observation != nil {
		return *state.Observation, nil
	}
	if err := requireSynchronousContext(ctx, c); err != nil {
		return ToolTurnObservation{}, err
	}
	transcript, err := c.read(ctx, "/session/"+expected.Dispatch.Binding.SessionID+"/message")
	if err != nil {
		return ToolTurnObservation{}, err
	}
	var observation ToolTurnObservation
	if expected.Dispatch.StructuredOutput != nil {
		observation, err = decodeToolTurnWithStructuredOutputAndRuntimeMetadata(transcript, expected.Dispatch.Binding, expected.Dispatch.Text, broker, expected.Dispatch.StructuredOutput, expected.RuntimeMetadata)
	} else {
		observation, err = decodeToolTurnWithRuntimeMetadata(transcript, expected.Dispatch.Binding, expected.Dispatch.Text, broker, expected.RuntimeMetadata)
	}
	if err != nil {
		return ToolTurnObservation{}, err
	}
	record := synchronousToolRecord{
		Transcript: string(transcript), BrokerBindingID: observation.BrokerBindingID,
		BrokerCatalogID: expected.BrokerCatalogID, BrokerStateID: observation.BrokerStateID,
	}
	if err := validateSynchronousToolEvidenceBound(record); err != nil {
		return ToolTurnObservation{}, err
	}
	if err := appendSynchronousToolDispatch(path, "opencode.sync-tool-observed", record, broker); err != nil {
		return ToolTurnObservation{}, err
	}
	return observation, nil
}

func validateSynchronousToolIntent(intent SynchronousToolDispatchIntent, broker contextbroker.State, requireUnused bool) error {
	if safepath.RequireDigest(intent.BrokerBindingID) != nil || safepath.RequireDigest(intent.BrokerCatalogID) != nil {
		return errors.New("invalid synchronous tool broker identities")
	}
	if intent.RuntimeMetadata != nil && (intent.RuntimeMetadata.validate() != nil || intent.RuntimeMetadata.WorkspaceRoot != intent.Dispatch.Binding.Directory) {
		return errors.New("invalid synchronous tool runtime metadata policy")
	}
	expected, err := DispatchForInvocationWithStructuredOutput(
		intent.Invocation, intent.Dispatch.Binding.SessionID, intent.Dispatch.Binding.ParentID,
		intent.Dispatch.Binding.Agent, intent.Dispatch.Binding.Directory, intent.Dispatch.Binding.Root,
		intent.Dispatch.StructuredOutput,
	)
	if err != nil || !equalCanonical(expected, intent.Dispatch) {
		return errors.New("synchronous tool invocation and dispatch differ")
	}
	if broker.Binding == nil || broker.Pending != nil {
		return errors.New("context broker unavailable for synchronous tool turn")
	}
	bindingID, err := broker.Binding.ID()
	if err != nil || bindingID != intent.BrokerBindingID || broker.Binding.CatalogID != intent.BrokerCatalogID || broker.Binding.InvocationID != intent.Invocation.ID {
		return errors.New("synchronous tool broker binding mismatch")
	}
	if requireUnused && (broker.Closed || broker.Calls != 0 || len(broker.Requests) != 0 || len(broker.Responses) != 0 || broker.ResponseBytes != 0) {
		return errors.New("synchronous tool broker already used")
	}
	return nil
}

func equalSynchronousToolIntent(left, right SynchronousToolDispatchIntent) bool {
	leftBytes, leftErr := canonical.Bytes(left)
	rightBytes, rightErr := canonical.Bytes(right)
	return leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes)
}

func validateSynchronousToolRecord(record synchronousToolRecord, intent SynchronousToolDispatchIntent, broker contextbroker.State) (ToolTurnObservation, error) {
	if err := validateSynchronousToolEvidenceBound(record); err != nil {
		return ToolTurnObservation{}, err
	}
	var err error
	var observation ToolTurnObservation
	if intent.Dispatch.StructuredOutput != nil {
		observation, err = decodeToolTurnWithStructuredOutputAndRuntimeMetadata([]byte(record.Transcript), intent.Dispatch.Binding, intent.Dispatch.Text, broker, intent.Dispatch.StructuredOutput, intent.RuntimeMetadata)
	} else {
		observation, err = decodeToolTurnWithRuntimeMetadata([]byte(record.Transcript), intent.Dispatch.Binding, intent.Dispatch.Text, broker, intent.RuntimeMetadata)
	}
	if err != nil {
		return ToolTurnObservation{}, err
	}
	if record.BrokerBindingID != intent.BrokerBindingID || record.BrokerBindingID != observation.BrokerBindingID || record.BrokerCatalogID != intent.BrokerCatalogID || record.BrokerStateID != observation.BrokerStateID {
		return ToolTurnObservation{}, errors.New("recorded synchronous tool broker evidence mismatch")
	}
	if record.Response != "" {
		var returned synchronousResponseObservation
		if intent.Dispatch.StructuredOutput != nil {
			returned, err = decodeSynchronousResponseWithStructuredOutput([]byte(record.Response), intent.Dispatch.Binding, *intent.Dispatch.StructuredOutput)
		} else {
			var text string
			returned.Assistant, text, err = decodeSynchronousResponse([]byte(record.Response), intent.Dispatch.Binding)
			returned.Text = text
		}
		if err != nil {
			return ToolTurnObservation{}, err
		}
		if !synchronousResponseMatchesToolObservation(returned, observation) {
			return ToolTurnObservation{}, errors.New("recorded synchronous tool response and transcript differ")
		}
	}
	return observation, nil
}

func synchronousResponseMatchesToolObservation(returned synchronousResponseObservation, observation ToolTurnObservation) bool {
	if returned.Assistant != observation.Final || returned.Text != observation.Text {
		return false
	}
	if observation.StructuredOutput == nil {
		return returned.StructuredOutput == nil && returned.StructuredOutputTool == nil
	}
	return returned.StructuredOutput != nil && returned.StructuredOutputTool != nil && equalCanonical(*returned.StructuredOutput, *observation.StructuredOutput) && equalCanonical(*returned.StructuredOutputTool, *observation.StructuredOutputTool)
}

func validateSynchronousToolEvidenceBound(record synchronousToolRecord) error {
	if record.Transcript == "" {
		return errors.New("synchronous tool transcript evidence required")
	}
	raw, err := canonical.Bytes(record)
	if err != nil || len(raw) > maxSynchronousEvidenceBytes {
		return errors.New("combined synchronous tool evidence exceeds journal bound")
	}
	return nil
}
