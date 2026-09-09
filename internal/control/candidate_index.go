package control

import (
	"context"
	"encoding/json"
	"errors"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/worktree"
)

// CandidateIndexObservation is diagnostic raw-index evidence bound to the
// admitted semantic candidate. It never updates candidate/effect authority.
type CandidateIndexObservation struct {
	Candidate      worktree.Candidate        `json:"candidate"`
	Index          worktree.IndexObservation `json:"index"`
	PreviousRaw    string                    `json:"previous_raw"`
	Classification string                    `json:"classification"`
}

func observeCandidateBaseline(ctx context.Context, path string) (Snapshot, error) {
	s, err := Inspect(path)
	if err != nil || s.Creation.Config.CandidateIdentity != "semantic-index-v2" {
		return s, err
	}
	if _, err := observeCandidate(ctx, path, s); err != nil {
		return s, err
	}
	return Inspect(path)
}

func indexClassification(s Snapshot, c worktree.Candidate, raw string) string {
	if s.Candidate == nil || c != *s.Candidate {
		return "CANDIDATE_MUTATION"
	}
	if s.CandidateIndex == nil || s.CandidateIndex.Candidate != c {
		return "BASELINE"
	}
	if s.CandidateIndex.Index.RawHash != raw {
		return "METADATA_ONLY_INDEX_CHANGE"
	}
	return "UNCHANGED"
}

func replayCandidateIndex(s *Snapshot, raw json.RawMessage) error {
	var o CandidateIndexObservation
	if s.Workspace == nil || s.Candidate == nil || s.Creation.Config.CandidateIdentity != "semantic-index-v2" || canonical.Decode(raw, &o) != nil {
		return errors.New("invalid index evidence context")
	}
	if _, err := o.Candidate.ID(); err != nil {
		return err
	}
	if o.Candidate.Version != 2 || o.Index.SemanticHash != o.Candidate.IndexHash {
		return errors.New("index evidence semantic binding mismatch")
	}
	if err := safepath.RequireDigest(o.Index.RawHash); err != nil {
		return err
	}
	previous := ""
	if s.CandidateIndex != nil {
		previous = s.CandidateIndex.Index.RawHash
	}
	if o.PreviousRaw != previous || o.Classification != indexClassification(*s, o.Candidate, o.Index.RawHash) {
		return errors.New("index reconciliation evidence mismatch")
	}
	s.CandidateIndex = &o
	return nil
}

// observeCandidate runs only at existing leased controller gates. Raw index
// changes are classified after complete semantic/file observation, never ignored.
func observeCandidate(ctx context.Context, path string, s Snapshot) (worktree.Candidate, error) {
	if s.Creation.Config.CandidateIdentity != "semantic-index-v2" {
		return worktree.Fingerprint(ctx, *s.Workspace)
	}
	first, err := worktree.IndexEvidence(ctx, *s.Workspace)
	if err != nil {
		return worktree.Candidate{}, err
	}
	c, err := worktree.Fingerprint(ctx, *s.Workspace)
	if err != nil {
		return c, err
	}
	last, err := worktree.IndexEvidence(ctx, *s.Workspace)
	if err != nil {
		return c, err
	}
	if first != last || c.IndexHash != last.SemanticHash {
		return c, errors.New("index changed during semantic reconciliation")
	}
	fresh, err := Inspect(path)
	if err != nil {
		return c, err
	}
	if fresh.Candidate == nil || s.Candidate == nil || *fresh.Candidate != *s.Candidate {
		return c, errors.New("candidate authority changed during reconciliation")
	}
	previous := ""
	if fresh.CandidateIndex != nil {
		previous = fresh.CandidateIndex.Index.RawHash
	}
	classification := indexClassification(fresh, c, last.RawHash)
	if classification != "UNCHANGED" {
		if err := Append(path, "candidate.index-observed", CandidateIndexObservation{c, last, previous, classification}); err != nil {
			return c, err
		}
	}
	if classification == "CANDIDATE_MUTATION" {
		return c, errors.New("CANDIDATE_MUTATION")
	}
	return c, nil
}
