package control

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexruntime"
)

const explorerCapacityRetrySuffix = " | provider-capacity-retry: 1"

// Capacity rejection is a known failed read-only effect, never an exploration.
// Only a sealed, exact runtime can close its controller receipt. No host starts.
func reconcileExplorerCapacityFailures(path string, s Snapshot) (Snapshot, error) {
	// Version 2 has a separate model-access/usage settlement contract.
	if s.Creation.Config.Version != 1 {
		return s, nil
	}
	var err error
	s, err = Inspect(path)
	if err != nil {
		return s, err
	}
	for _, host := range s.ExplorerRuns {
		if host.RuntimeReceipt != nil || !host.Ready || host.Receipt == nil || host.Intent.Invocation.Profile.Runtime != "codex-app-server" {
			continue
		}
		state, head, err := codexruntime.InspectWithHead(filepath.Join(host.Intent.Launch.Root, "explorer.jsonl"))
		if err != nil || !explorerCapacityFailure(state, host, s) {
			continue
		}
		hash, err := canonical.Hash("harness.explorer-capacity-failure.v1", *state.TerminalResponse)
		if err != nil {
			return s, err
		}
		receipt := ExplorerRuntimeReceipt{InvocationID: host.Intent.Invocation.ID, ThreadID: state.Thread.ThreadID, TurnID: state.TurnID, JournalHead: head, ResultHash: hash, FailureCode: "serverOverloaded"}
		if err := Append(path, "explorer.runtime-observed", receipt); err != nil {
			return s, err
		}
	}
	return Inspect(path)
}

func explorerCapacityFailure(state codexruntime.State, host ExplorerHostState, s Snapshot) bool {
	if state.Intent == nil || state.Intent.Invocation != host.Intent.Invocation || state.Intent.Directory != filepath.Join(host.Intent.Launch.Root, "workspace") ||
		state.Thread == nil || state.TurnID == "" || state.TurnStatus != "failed" || !state.NotificationStreamComplete || state.PendingTool != nil ||
		state.Result != nil || state.SemanticResult != nil || state.Source == nil || *state.Source != s.Creation.Repository || state.Candidate == nil || s.Workspace == nil || state.Candidate.Workspace != *s.Workspace ||
		state.TerminalResponse == nil || state.TerminalResponse.ThreadID != state.Thread.ThreadID || state.TerminalResponse.TurnID != state.TurnID || state.TerminalResponse.TurnStatus != "failed" {
		return false
	}
	if state.Thread.Validate(host.Intent.Invocation.Profile, state.Intent.Directory) != nil {
		return false
	}
	var input struct {
		CandidateID string `json:"candidate_id"`
	}
	candidateID, err := state.Candidate.Candidate.ID()
	if err != nil || json.Unmarshal([]byte(host.Intent.Invocation.Input), &input) != nil || input.CandidateID != candidateID {
		return false
	}
	var failure struct {
		Code string `json:"codexErrorInfo"`
	}
	return json.Unmarshal(state.TerminalResponse.TurnError, &failure) == nil && failure.Code == "serverOverloaded"
}

// A new question yields a new invocation and schedule; the failed invocation
// is never resumed. One retry is allowed, and replay selects it idempotently.
func explorerCapacityRetryQuestion(s Snapshot, question string) (string, error) {
	for _, host := range s.ExplorerRuns {
		if host.Intent.Question != question || host.RuntimeReceipt == nil || host.RuntimeReceipt.FailureCode != "serverOverloaded" {
			continue
		}
		retry := question + explorerCapacityRetrySuffix
		for _, next := range s.ExplorerRuns {
			if next.Intent.Question == retry && next.RuntimeReceipt != nil && next.RuntimeReceipt.FailureCode != "" {
				return "", errors.New("explorer provider capacity retry exhausted")
			}
		}
		if len(retry) > 4096 || strings.HasSuffix(question, explorerCapacityRetrySuffix) {
			return "", errors.New("explorer capacity retry question bound exceeded")
		}
		return retry, nil
	}
	return question, nil
}
