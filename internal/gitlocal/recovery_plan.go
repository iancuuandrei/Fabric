package gitlocal

import (
	"errors"
	"strings"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/safepath"
)

// RecoveryPlan is a new approval target for one observed partial commit state.
// It does not authorize any operation through the original commit approval.
type RecoveryPlan struct {
	PreviousIntentID string         `json:"previous_intent_id,omitempty"`
	Version          int            `json:"version"`
	Commit           CommitPlan     `json:"commit"`
	OriginalIntent   effects.Intent `json:"original_intent"`
	TreeID           string         `json:"tree_id"`
	CommitID         string         `json:"commit_id"`
	Before           RecoveryState  `json:"before"`
	Nonce            string         `json:"nonce"`
	Evidence         string         `json:"evidence"`
	WorkloadsStopped bool           `json:"workloads_stopped"`
}

// ID validates scope, predictions, partial state and evidence before hashing them.
func (p RecoveryPlan) ID() (string, error) {
	if p.PreviousIntentID != "" {
		if err := safepath.RequireDigest(p.PreviousIntentID); err != nil {
			return "", err
		}
	}
	if p.Version != 1 || !p.WorkloadsStopped || strings.TrimSpace(p.Nonce) == "" || len(p.Nonce) > 128 || !utf8.ValidString(p.Nonce) || strings.TrimSpace(p.Evidence) == "" || len(p.Evidence) > 4096 || !utf8.ValidString(p.Evidence) {
		return "", errors.New("explicit bounded commit recovery evidence required")
	}
	planID, err := p.Commit.ID()
	if err != nil {
		return "", err
	}
	if _, err := p.OriginalIntent.ID(); err != nil {
		return "", err
	}
	repo, err := p.Commit.Workspace.Request.Source.ID()
	if err != nil {
		return "", err
	}
	if p.OriginalIntent.Kind != "commit" || p.OriginalIntent.InputHash != planID || p.OriginalIntent.RepositoryID != repo || p.OriginalIntent.RunID != p.Commit.Workspace.Request.RunID {
		return "", errors.New("recovery original intent mismatch")
	}
	_, expected, err := CommitObject(p.Commit, p.TreeID)
	if err != nil {
		return "", err
	}
	if expected != p.CommitID {
		return "", errors.New("recovery commit prediction mismatch")
	}
	if _, err := p.Before.Candidate.ID(); err != nil {
		return "", err
	}
	switch p.Before.Stage {
	case "BEFORE_REF":
		if p.Before.Candidate != p.Commit.Candidate {
			return "", errors.New("recovery original candidate mismatch")
		}
	case "AFTER_REF":
		updated := p.Commit.Workspace
		updated.Request.Source.Commit, updated.Request.Source.Tree = p.CommitID, p.TreeID
		bindingID, err := updated.ID()
		if err != nil {
			return "", err
		}
		expected := p.Commit.Candidate
		expected.WorktreeID, expected.Head = bindingID, p.CommitID
		if p.Before.Candidate != expected {
			return "", errors.New("recovery post-ref candidate mismatch")
		}
	default:
		return "", errors.New("only partial commit states require recovery; reconcile finalized state")
	}
	return canonical.Hash("harness.commit-recovery.v1", p)
}

// Intent derives a recovery effect within the original run/plan/repository scope.
func (p RecoveryPlan) Intent() (effects.Intent, error) {
	id, err := p.ID()
	if err != nil {
		return effects.Intent{}, err
	}
	intent := p.OriginalIntent
	intent.Kind, intent.InputHash = "commit_recovery", id
	return intent, nil
}
