package ri

import (
	"errors"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/safepath"
	"path/filepath"
)

// LexicalPlan binds a finite local indexing effect without embedding a large
// manifest in its journal record. The payload is supplied by exact manifest ID.
type LexicalPlan struct {
	Version          int                 `json:"version"`
	Repository       repository.Identity `json:"repository"`
	ManifestID       string              `json:"manifest_id"`
	Files            int                 `json:"files"`
	Executable       string              `json:"executable"`
	ExecutableSHA256 string              `json:"executable_sha256"`
	StageRoot        string              `json:"stage_root"`
	BatchBytes       int64               `json:"batch_bytes"`
	BatchFiles       int                 `json:"batch_files"`
}

// ID validates exact bindings and returns the complete indexing plan identity.
func (p LexicalPlan) ID() (string, error) {
	if _, err := p.Repository.ID(); err != nil {
		return "", err
	}
	if p.Version != 1 || p.Files < 0 || p.Files > 500000 || p.BatchBytes < 1 || p.BatchBytes > 64<<20 || p.BatchFiles < 1 || p.BatchFiles > 10000 {
		return "", errors.New("invalid lexical plan bounds")
	}
	for _, id := range []string{p.ManifestID, p.ExecutableSHA256} {
		if err := safepath.RequireDigest(id); err != nil {
			return "", err
		}
	}
	for _, path := range []string{p.Executable, p.StageRoot} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return "", errors.New("normalized absolute lexical plan path required")
		}
	}
	return canonical.Hash("harness.ri.lexical-plan.v1", p)
}

// ValidateManifest checks the supplied large payload before any staging writes.
func (p LexicalPlan) ValidateManifest(m LexicalManifest) error {
	if _, err := p.ID(); err != nil {
		return err
	}
	id, err := m.ID()
	if err != nil {
		return err
	}
	source, err := FromRepository(p.Repository)
	if err != nil {
		return err
	}
	if id != p.ManifestID || source != m.Source || len(m.Files) != p.Files {
		return errors.New("lexical payload differs from plan")
	}
	for _, file := range m.Files {
		if file.Bytes > p.BatchBytes {
			return errors.New("lexical file exceeds planned batch ceiling")
		}
	}
	return nil
}

// Intent binds indexing to the exact run and approved implementation plan.
func (p LexicalPlan) Intent(runID, planID string) (effects.Intent, error) {
	id, err := p.ID()
	if err != nil {
		return effects.Intent{}, err
	}
	repositoryID, err := p.Repository.ID()
	if err != nil {
		return effects.Intent{}, err
	}
	intent := effects.Intent{Version: 1, RunID: runID, PlanID: planID, RepositoryID: repositoryID, Kind: "ri_lexical", InputHash: id}
	if _, err := intent.ID(); err != nil {
		return effects.Intent{}, err
	}
	return intent, nil
}
