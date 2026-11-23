package control

import (
	"context"
	"errors"

	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/worktree"
	"github.com/iancuuandrei/fabric/internal/writercontract"
)

// AnchoredSemanticRejection retains exact bounded preimages for pure replay of
// an unapplied content rejection. A source/candidate/hash failure is not one.
type AnchoredSemanticRejection struct {
	Version   int                  `json:"version"`
	Candidate worktree.Candidate   `json:"candidate"`
	Manifest  []worktree.FileState `json:"manifest"`
	Preimages []WriterEditPreimage `json:"preimages"`
}

func anchoredCorrectionSnapshot(s Snapshot, taskID string) (Snapshot, error) {
	if taskID == "" || !isolatedImplementationEnabled(s) {
		return s, nil
	}
	var binding isolatedWriterBinding
	binding, err := isolatedWriterBindingForSnapshot(s, taskID)
	if err != nil {
		return s, err
	}
	s.Workspace = &binding.Workspace
	s.Candidate = &binding.Candidate
	return s, nil
}

func captureAnchoredSemanticRejection(ctx context.Context, path string, s Snapshot, taskID string, result runtime.Result) (*AnchoredSemanticRejection, error) {
	if !writercontract.IsAnchoredEdits(s.Creation.Config.WriterContract) || result.Requested.Role != "writer" && result.Requested.Role != "fixer" {
		return nil, nil
	}
	proposal, err := decodeAnchoredProposal(result.Output)
	if err != nil {
		// Malformed JSON/shape uses the existing no-preimage correction path.
		return nil, nil
	}
	bound, err := anchoredCorrectionSnapshot(s, taskID)
	if err != nil || bound.Candidate == nil {
		return nil, errors.Join(ErrAutonomousUnsafe, err)
	}
	candidateID, err := bound.Candidate.ID()
	if err != nil || proposal.CandidateID != candidateID {
		return nil, ErrAutonomousUnsafe
	}
	manifest, preimages, err := readAnchoredPreimages(ctx, path, bound, proposal, taskID)
	if err != nil {
		return nil, err
	}
	proof := &AnchoredSemanticRejection{Version: 1, Candidate: *bound.Candidate, Manifest: manifest, Preimages: preimages}
	if _, composeErr := composeAnchoredProposal(proposal, manifest, preimages); composeErr == nil {
		// A valid proposal may still need ownership replanning. It has no
		// semantic rejection proof and must not consume a correction turn.
		return nil, nil
	}
	if _, err := validateAnchoredSemanticRejection(s, taskID, proposal, proof); err != nil {
		return nil, err
	}
	return proof, nil
}

func validateAnchoredSemanticRejection(s Snapshot, taskID string, proposal writercontract.AnchoredProposal, proof *AnchoredSemanticRejection) (error, error) {
	bound, err := anchoredCorrectionSnapshot(s, taskID)
	if err != nil || proof == nil || proof.Version != 1 || bound.Candidate == nil || proof.Candidate != *bound.Candidate {
		return nil, errors.Join(ErrAutonomousUnsafe, err)
	}
	candidateID, err := proof.Candidate.ID()
	if err != nil || candidateID != proposal.CandidateID {
		return nil, ErrAutonomousUnsafe
	}
	manifestID, err := worktree.FilesID(proof.Manifest)
	if err != nil || manifestID != proof.Candidate.FilesHash || len(proof.Manifest) != proof.Candidate.FileCount {
		return nil, errors.Join(ErrAutonomousUnsafe, err)
	}
	_, rejection := composeAnchoredProposal(proposal, proof.Manifest, proof.Preimages)
	if !semanticOutputFailureOnly(rejection) {
		return nil, errors.Join(ErrAutonomousUnsafe, errors.New("anchor rejection is not solely unapplied content"), rejection)
	}
	return rejection, nil
}
