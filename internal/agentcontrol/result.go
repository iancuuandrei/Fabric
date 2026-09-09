package agentcontrol

import (
	"context"
	"errors"

	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
)

const (
	// AcceptedResultRoleExplorer is the first controller role permitted to
	// publish an accepted child result.
	AcceptedResultRoleExplorer = "explorer"
	// AcceptedResultKindExploration identifies an accepted explorer.recorded
	// result without treating its advisory body as verified fact.
	AcceptedResultKindExploration = "exploration"
)

// AcceptedResultReference identifies one controller-accepted child turn
// result without copying its body out of the authoritative controller event.
type AcceptedResultReference struct {
	Version             int    `json:"version"`
	TreeID              string `json:"tree_id"`
	ParentAgentID       string `json:"parent_agent_id"`
	AgentID             string `json:"agent_id"`
	TurnID              string `json:"turn_id"`
	TurnSequence        int    `json:"turn_sequence"`
	InvocationID        string `json:"invocation_id"`
	AdmissionID         string `json:"admission_id"`
	ControllerEventHash string `json:"controller_event_hash"`
	Role                string `json:"role"`
	ResultKind          string `json:"result_kind"`
	ResultSHA256        string `json:"result_sha256"`
}

// ID returns the immutable accepted-result identity.
func (r AcceptedResultReference) ID() (string, error) {
	if err := validateAcceptedResultReference(r); err != nil {
		return "", err
	}
	return canonical.Hash("harness.agentcontrol-accepted-result.v1", r)
}

// AcceptedResult is one body-free durable result index entry.
type AcceptedResult struct {
	ResultID  string                  `json:"result_id"`
	Reference AcceptedResultReference `json:"reference"`
}

type acceptedResultEvent struct {
	Version    int            `json:"version"`
	Result     AcceptedResult `json:"result"`
	Activities []Activity     `json:"activities"`
}

// IndexAcceptedResult records a controller-accepted child result reference.
// AgentTree and turn state prove topology and runtime observation only. This
// method trusts its controller caller to have verified the admission and exact
// controller event; those states cannot synthesize acceptance here.
func (s *Service) IndexAcceptedResult(ctx context.Context, reference AcceptedResultReference) (AcceptedResult, error) {
	if err := validateService(s); err != nil || ctx == nil || ctx.Err() != nil || validateAcceptedResultReference(reference) != nil || reference.TreeID != s.treeID {
		return AcceptedResult{}, errors.Join(errors.New("invalid accepted child result"), err, contextError(ctx))
	}
	tree, err := agenttree.Inspect(s.treePath)
	if err != nil || tree.TreeID != s.treeID || !acceptedResultTopology(tree, reference) {
		return AcceptedResult{}, errors.Join(errors.New("accepted child result topology differs"), err)
	}
	resultID, err := reference.ID()
	if err != nil {
		return AcceptedResult{}, err
	}
	wanted := AcceptedResult{ResultID: resultID, Reference: reference}
	var lastAppendErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return AcceptedResult{}, err
		}
		state, inspectErr := Inspect(s.journalPath)
		if inspectErr != nil {
			return AcceptedResult{}, inspectErr
		}
		if state.TreeID != s.treeID {
			return AcceptedResult{}, errors.New("agent control tree identity changed")
		}
		if prior, ok := acceptedResultForTurn(state, reference.TurnID); ok {
			if prior == wanted {
				return prior, nil
			}
			return AcceptedResult{}, errors.New("accepted child result differs")
		}
		if !acceptedTurnActivity(state, reference) {
			return AcceptedResult{}, errors.New("accepted child turn lacks exact successful runtime observation")
		}
		if len(state.Activities) > MaximumActivities-2 {
			return AcceptedResult{}, errors.New("agent control activity limit exceeded")
		}
		activities := []Activity{
			acceptedResultActivity(reference.ParentAgentID, nextActivitySequence(state, reference.ParentAgentID), reference, resultID),
			acceptedResultActivity(reference.AgentID, nextActivitySequence(state, reference.AgentID), reference, resultID),
		}
		_, appendErr := journal.Append(s.journalPath, "agentcontrol.result-accepted", acceptedResultEvent{Version: 1, Result: wanted, Activities: activities}, func(events []journal.Event) error {
			_, replayErr := Replay(events)
			return replayErr
		})
		if appendErr == nil {
			return wanted, nil
		}
		lastAppendErr = appendErr
	}
	state, inspectErr := Inspect(s.journalPath)
	if inspectErr == nil && state.TreeID == s.treeID {
		if prior, ok := acceptedResultForTurn(state, reference.TurnID); ok {
			if prior == wanted {
				return prior, nil
			}
			return AcceptedResult{}, errors.New("accepted child result differs")
		}
	}
	return AcceptedResult{}, errors.Join(errors.New("accepted child result contention"), lastAppendErr, inspectErr)
}

func replayAcceptedResult(state *Snapshot, event journal.Event) error {
	var payload acceptedResultEvent
	if state == nil || canonical.Decode(event.Payload, &payload) != nil || payload.Version != 1 || validateAcceptedResultReference(payload.Result.Reference) != nil || payload.Result.Reference.TreeID != state.TreeID {
		return errors.New("invalid accepted child result")
	}
	resultID, err := payload.Result.Reference.ID()
	if err != nil || payload.Result.ResultID != resultID || !acceptedTurnActivity(*state, payload.Result.Reference) {
		return errors.Join(errors.New("accepted child result lacks exact turn observation"), err)
	}
	if prior, ok := acceptedResultForTurn(*state, payload.Result.Reference.TurnID); ok {
		if prior == payload.Result {
			return errors.New("duplicate accepted child result")
		}
		return errors.New("accepted child result differs")
	}
	if len(payload.Activities) != 2 || len(state.Activities) > MaximumActivities-2 || payload.Activities[0].AgentID != payload.Result.Reference.ParentAgentID || payload.Activities[1].AgentID != payload.Result.Reference.AgentID {
		return errors.New("invalid accepted child result activity")
	}
	for _, activity := range payload.Activities {
		if validateActivity(*state, activity) != nil || activity.Kind != "result" || activity.ResultID != resultID || activity.TurnID != payload.Result.Reference.TurnID || activity.TurnSequence != payload.Result.Reference.TurnSequence || activity.ResultSHA256 != payload.Result.Reference.ResultSHA256 {
			return errors.New("invalid accepted child result activity")
		}
	}
	state.Results = append(state.Results, payload.Result)
	state.Activities = append(state.Activities, payload.Activities...)
	for _, activity := range payload.Activities {
		state.Latest[activity.AgentID] = activity
	}
	return nil
}

func validateAcceptedResultReference(reference AcceptedResultReference) error {
	if reference.Version != 1 || safepath.RequireDigest(reference.TreeID) != nil || safepath.RequireDigest(reference.ParentAgentID) != nil || safepath.RequireDigest(reference.AgentID) != nil || reference.ParentAgentID == reference.AgentID || safepath.RequireDigest(reference.TurnID) != nil || reference.TurnSequence < 1 || safepath.RequireDigest(reference.InvocationID) != nil || safepath.RequireDigest(reference.AdmissionID) != nil || safepath.RequireDigest(reference.ControllerEventHash) != nil || safepath.RequireDigest(reference.ResultSHA256) != nil || reference.Role != AcceptedResultRoleExplorer || reference.ResultKind != AcceptedResultKindExploration {
		return errors.New("invalid accepted result reference")
	}
	return nil
}

func acceptedResultTopology(tree agenttree.Snapshot, reference AcceptedResultReference) bool {
	for _, node := range tree.Nodes {
		if node.AgentID == reference.AgentID {
			return node.ParentAgentID == reference.ParentAgentID && node.Role == reference.Role && (reference.TurnSequence > 1 || node.InvocationID == reference.InvocationID)
		}
	}
	return false
}

func acceptedTurnActivity(state Snapshot, reference AcceptedResultReference) bool {
	for index := len(state.Activities) - 1; index >= 0; index-- {
		activity := state.Activities[index]
		if activity.Kind == "turn" && activity.TurnID == reference.TurnID {
			return activity.AgentID == reference.AgentID && activity.TurnSequence == reference.TurnSequence && activity.Status == agenttree.StatusSucceeded && activity.ResultSHA256 == reference.ResultSHA256
		}
	}
	return false
}

func acceptedResultForTurn(state Snapshot, turnID string) (AcceptedResult, bool) {
	for _, result := range state.Results {
		if result.Reference.TurnID == turnID {
			return result, true
		}
	}
	return AcceptedResult{}, false
}

func acceptedResultActivity(agentID string, sequence int, reference AcceptedResultReference, resultID string) Activity {
	return Activity{AgentID: agentID, Sequence: sequence, Kind: "result", TurnID: reference.TurnID, TurnSequence: reference.TurnSequence, Status: agenttree.StatusSucceeded, ResultSHA256: reference.ResultSHA256, ResultID: resultID}
}
