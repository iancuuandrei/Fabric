package ri

import (
	"errors"
	"path/filepath"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/safepath"
)

// ImportPlan is the complete immutable payload for a journaled import effect.
// Its identity binds executable, producer inputs, source authority and destination.
// It grants no permission and does not establish that inputs are committed files.
type ImportPlan struct {
	// ProducerIntentID optionally binds import to a confirmed journaled producer.
	ProducerIntentID string `json:"producer_intent_id"`
	// MaterializeSources includes exclusive committed-source copies in this effect.
	MaterializeSources bool                `json:"materialize_sources"`
	Version            int                 `json:"version"`
	Executable         string              `json:"executable"`
	ExecutableSHA256   string              `json:"executable_sha256"`
	Repository         repository.Identity `json:"repository"`
	Request            ImportRequest       `json:"request"`
	OutputPath         string              `json:"output_path"`
}

// ID validates controller bindings and hashes the entire effect payload.
// Rust admission still validates semantic manifest and index constraints.
func (p ImportPlan) ID() (string, error) {
	if p.ProducerIntentID != "" {
		if err := safepath.RequireDigest(p.ProducerIntentID); err != nil {
			return "", err
		}
	}
	source, err := FromRepository(p.Repository)
	if err != nil {
		return "", err
	}
	if p.Version != 1 || p.Request.Source != source || p.Request.Manifest.Source != source || p.Request.Manifest.Format != 1 || p.Request.Producer == "" || (p.Request.Policy != "strict" && p.Request.Policy != "scip_go027") {
		return "", errors.New("invalid import plan binding")
	}
	for _, path := range []string{p.Executable, p.Request.IndexPath, p.OutputPath} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return "", errors.New("normalized absolute import path required")
		}
	}
	if err := safepath.RequireDigest(p.ExecutableSHA256); err != nil {
		return "", err
	}
	if p.Request.Sources == nil || len(p.Request.Sources) > 4095 {
		return "", errors.New("invalid import source selection")
	}
	for _, path := range p.Request.Sources {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return "", errors.New("invalid import source path")
		}
	}
	return canonical.Hash("harness.ri.import-plan.v1", p)
}

// Intent binds one validated payload to the run and approved plan.
func (p ImportPlan) Intent(runID, planID string) (effects.Intent, error) {
	hash, err := p.ID()
	if err != nil {
		return effects.Intent{}, err
	}
	repositoryID, err := p.Repository.ID()
	if err != nil {
		return effects.Intent{}, err
	}
	intent := effects.Intent{Version: 1, RunID: runID, PlanID: planID, RepositoryID: repositoryID, Kind: "ri_import", InputHash: hash}
	if _, err := intent.ID(); err != nil {
		return effects.Intent{}, err
	}
	return intent, nil
}
