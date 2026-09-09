package codexruntime

import (
	"context"
	"encoding/json"

	"harness.local/engorch/internal/candidatetools"
)

// CandidateBinding pins mutable workspace reads to an exact admitted candidate.
// The caller must hold the workspace lease throughout runtime execution.
type CandidateBinding = candidatetools.Binding

type candidatePage = candidatetools.Page

type listArgs = candidatetools.ListArgs

type readArgs = candidatetools.ReadArgs

func listCandidate(ctx context.Context, binding CandidateBinding, after string, limit int) (candidatePage, error) {
	content, _, err := candidatetools.Execute(ctx, binding, candidatetools.ListName, mustCandidateArguments(after, limit))
	if err != nil {
		return candidatePage{}, err
	}
	return content.(candidatetools.Page), nil
}

func mustCandidateArguments(after string, limit int) json.RawMessage {
	raw, _ := json.Marshal(struct {
		After string `json:"after"`
		Limit int    `json:"limit"`
	}{after, limit})
	return raw
}
