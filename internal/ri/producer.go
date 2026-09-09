package ri

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/verification"
	"io"
	"os"
	"path/filepath"
)

// ProducerPlan freezes process inputs and output location for a semantic indexer.
// Source/workspace acquisition and journal authorization belong to the controller.
type ProducerPlan struct {
	Version    int                     `json:"version"`
	Repository repository.Identity     `json:"repository"`
	Invocation verification.Invocation `json:"invocation"`
	OutputPath string                  `json:"output_path"`
}

// ProducerArtifact binds the exact binary index emitted by a successful process.
type ProducerArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// ProducerResult separates process success from semantic artifact admission.
type ProducerResult struct {
	PlanID   string              `json:"plan_id"`
	Process  verification.Result `json:"process"`
	Artifact *ProducerArtifact   `json:"artifact"`
}

// ValidateProducerResult checks the persisted envelope against its exact plan.
// It validates process facts and artifact metadata, not artifact bytes or SCIP.
// A successful process without an admitted artifact remains a valid incomplete
// observation and must not be classified as a confirmed producer effect.
func ValidateProducerResult(plan ProducerPlan, result ProducerResult) error {
	id, err := plan.ID()
	if err != nil {
		return err
	}
	if result.PlanID != id {
		return errors.New("producer result plan mismatch")
	}
	if err := verification.ValidateResult(plan.Invocation, result.Process); err != nil {
		return err
	}
	if artifact := result.Artifact; artifact != nil {
		if result.Process.Status != "PASS" || artifact.Path != plan.OutputPath || artifact.Bytes < 1 || artifact.Bytes > 64<<20 {
			return errors.New("producer artifact outcome or bounds mismatch")
		}
		if err := safepath.RequireDigest(artifact.SHA256); err != nil {
			return err
		}
	}
	return nil
}

// Intent binds the entire frozen producer invocation to an approved run plan.
// This is an authorization target; it does not itself dispatch or journal work.
func (p ProducerPlan) Intent(runID, planID string) (effects.Intent, error) {
	hash, err := p.ID()
	if err != nil {
		return effects.Intent{}, err
	}
	repositoryID, err := p.Repository.ID()
	if err != nil {
		return effects.Intent{}, err
	}
	intent := effects.Intent{Version: 1, RunID: runID, PlanID: planID, RepositoryID: repositoryID, Kind: "ri_producer", InputHash: hash}
	if _, err := intent.ID(); err != nil {
		return effects.Intent{}, err
	}
	return intent, nil
}

// PrepareProducer pins a finite process invocation without starting the indexer.
func PrepareProducer(identity repository.Identity, directory string, check config.Check, output string) (ProducerPlan, error) {
	id, err := identity.ID()
	if err != nil {
		return ProducerPlan{}, err
	}
	invocation, err := verification.Prepare(id, directory, check)
	if err != nil {
		return ProducerPlan{}, err
	}
	plan := ProducerPlan{1, identity, invocation, output}
	_, err = plan.ID()
	return plan, err
}

// ID validates source/process bindings and returns a distinct producer-plan identity.
func (p ProducerPlan) ID() (string, error) {
	id, err := p.Repository.ID()
	if err != nil {
		return "", err
	}
	if p.Version != 1 || p.Invocation.CandidateID != id || !filepath.IsAbs(p.OutputPath) || filepath.Clean(p.OutputPath) != p.OutputPath {
		return "", errors.New("invalid producer plan binding")
	}
	if err := p.Invocation.Validate(); err != nil {
		return "", err
	}
	return canonical.Hash("harness.ri.producer-plan.v1", p)
}

// ExecuteProducer runs once using the existing bounded process engine and hashes
// a new regular output. Caller must journal intent before invocation. A returned
// artifact proves exact bytes only; SCIP admission is a separate required step.
func ExecuteProducer(ctx context.Context, plan ProducerPlan) (ProducerResult, error) {
	id, err := plan.ID()
	if err != nil {
		return ProducerResult{}, err
	}
	if err := safepath.Directory(filepath.Dir(plan.OutputPath)); err != nil {
		return ProducerResult{}, err
	}
	if _, err := os.Lstat(plan.OutputPath); !os.IsNotExist(err) {
		return ProducerResult{}, errors.New("producer output must be new")
	}
	result := ProducerResult{PlanID: id}
	result.Process, err = verification.Execute(ctx, plan.Invocation)
	if err != nil {
		return result, err
	}
	if err := verification.ValidateResult(plan.Invocation, result.Process); err != nil {
		return result, err
	}
	if result.Process.Status != "PASS" {
		return result, errors.New("semantic producer did not pass execution")
	}
	root, err := os.OpenRoot(filepath.Dir(plan.OutputPath))
	if err != nil {
		return result, err
	}
	defer root.Close()
	name := filepath.Base(plan.OutputPath)
	if err := safepath.Check(root, name, false); err != nil {
		return result, err
	}
	file, err := root.Open(name)
	if err != nil {
		return result, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 64<<20 {
		return result, errors.New("invalid producer artifact type or size")
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, (64<<20)+1))
	if err != nil {
		return result, err
	}
	if size != info.Size() || size > 64<<20 {
		return result, errors.New("producer artifact size changed")
	}
	result.Artifact = &ProducerArtifact{plan.OutputPath, hex.EncodeToString(hash.Sum(nil)), size}
	return result, ValidateProducerResult(plan, result)
}
