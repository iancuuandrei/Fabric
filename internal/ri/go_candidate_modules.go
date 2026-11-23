package ri

import (
	"context"
	"encoding/base64"
	"errors"
	"sort"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

const goCandidateModulePageBytes = 32 << 10

// CollectCandidateGoModuleInventory reads module-related files from one exact
// admitted candidate under a shared worktree lease. It inventories the full
// candidate file-path closure, including added and deleted manifests, while
// retaining only bounded manifest content. It records declarations, not Go
// build resolution or authority to execute a workspace/replacement.
func CollectCandidateGoModuleInventory(ctx context.Context, identity repository.Identity, binding worktree.Binding, expected worktree.Candidate, base GoModuleInventory) (GoModuleInventory, error) {
	if err := ValidateGoModuleInventory(base, identity); err != nil {
		return GoModuleInventory{}, errors.New("committed base module inventory required")
	}
	if binding.Request.Source != identity || expected.Head != identity.Commit || expected.ValidateBinding(binding) != nil {
		return GoModuleInventory{}, errors.New("candidate module inventory source or worktree binding mismatch")
	}
	lease, err := worktree.AcquireRead(binding.Request)
	if err != nil {
		return GoModuleInventory{}, err
	}
	var result GoModuleInventory
	collectErr := lease.WithOwnership(binding.Request, func(worktree.LeaseIdentity) error {
		var err error
		result, err = collectCandidateGoModuleInventoryLeased(ctx, identity, binding, expected, base)
		return err
	})
	closeErr := lease.Close()
	if err := errors.Join(collectErr, closeErr); err != nil {
		return GoModuleInventory{}, err
	}
	if err := ValidateCandidateGoModuleInventory(result, identity, expected, base); err != nil {
		return GoModuleInventory{}, err
	}
	return result, nil
}

func collectCandidateGoModuleInventoryLeased(ctx context.Context, identity repository.Identity, binding worktree.Binding, expected worktree.Candidate, base GoModuleInventory) (GoModuleInventory, error) {
	repositoryID, err := identity.ID()
	if err != nil {
		return GoModuleInventory{}, err
	}
	candidateID, err := expected.ID()
	if err != nil {
		return GoModuleInventory{}, err
	}
	captured, states, err := worktree.Capture(ctx, binding)
	if err != nil {
		return GoModuleInventory{}, err
	}
	if captured != expected {
		return GoModuleInventory{}, errors.New("candidate changed before module inventory")
	}
	result := GoModuleInventory{
		Version: GoModuleInventoryVersionCandidate, RepositoryID: repositoryID,
		Commit: identity.Commit, Tree: identity.Tree, CandidateID: candidateID,
		CandidateFilesHash: expected.FilesHash, BaseInventoryDigest: base.Digest,
		Coverage: "complete", Files: []GoManifestObservation{}, Omissions: []GoManifestOmission{},
	}
	usedBytes := int64(0)
	for _, state := range states {
		if err := ctx.Err(); err != nil {
			return GoModuleInventory{}, err
		}
		kind := goManifestKind(state.Path)
		if kind == "" {
			continue
		}
		result.ObservedManifestCount++
		if len(result.Files)+len(result.Omissions) >= goModuleMaxRecords {
			result.TruncatedManifestCount++
			result.Coverage = "partial"
			continue
		}
		if !taskcontext.EligiblePath(state.Path) {
			result.Omissions = append(result.Omissions, GoManifestOmission{Kind: kind, Path: "[redacted]", Reason: "sensitive_path"})
			result.Coverage = "partial"
			continue
		}
		if err := safepath.Relative(state.Path); err != nil {
			result.Omissions = append(result.Omissions, GoManifestOmission{Kind: kind, Path: "[redacted]", Reason: "unsafe_path"})
			result.Coverage = "partial"
			continue
		}
		if err := safepath.Writable(state.Path); err != nil {
			result.Omissions = append(result.Omissions, GoManifestOmission{Kind: kind, Path: state.Path, Reason: "protected_path"})
			result.Coverage = "partial"
			continue
		}
		observation := GoManifestObservation{Kind: kind, Path: state.Path}
		content, size, digest, err := readCandidateManifest(ctx, binding, expected, candidateID, state, usedBytes)
		if err != nil {
			return GoModuleInventory{}, err
		}
		observation.Bytes, observation.SHA256 = size, digest
		if size > goModuleMaxFileBytes {
			observation.Status = "oversized"
			result.Coverage = "partial"
		} else if size > goModuleMaxTotalBytes-usedBytes {
			observation.Status = "budget_omitted"
			result.Coverage = "partial"
		} else {
			usedBytes += size
			if !utf8.Valid(content) {
				observation.Status = "invalid_utf8"
				result.Coverage = "partial"
			} else if err := parseGoManifest(&observation, content); err != nil {
				observation.Status = "invalid"
				result.Coverage = "partial"
			} else {
				observation.Status = "parsed"
			}
		}
		result.Files = append(result.Files, observation)
	}
	result.TruncatedManifestCount = max(result.TruncatedManifestCount, result.ObservedManifestCount-len(result.Files)-len(result.Omissions))
	if result.TruncatedManifestCount > 0 {
		result.Coverage = "partial"
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	sortGoManifestOmissions(result.Omissions)
	after, _, err := worktree.Capture(ctx, binding)
	if err != nil {
		return GoModuleInventory{}, err
	}
	if after != expected {
		return GoModuleInventory{}, errors.New("candidate changed during module inventory")
	}
	if err := finalizeGoModuleInventory(&result); err != nil {
		return GoModuleInventory{}, err
	}
	return result, nil
}

func readCandidateManifest(ctx context.Context, binding worktree.Binding, expected worktree.Candidate, candidateID string, state worktree.FileState, usedBytes int64) ([]byte, int64, string, error) {
	content := make([]byte, 0, min(goModuleMaxFileBytes, goCandidateModulePageBytes))
	var expectedSize int64 = -1
	var expectedHash string
	var offset int64
	for {
		chunk, err := worktree.ReadSource(ctx, binding, expected, state.Path, offset, goCandidateModulePageBytes)
		if err != nil {
			return nil, 0, "", err
		}
		if chunk.CandidateID != candidateID || chunk.Path != state.Path || chunk.Offset != offset || chunk.SHA256 != state.Hash || expectedHash != "" && chunk.SHA256 != expectedHash || expectedSize >= 0 && chunk.Size != expectedSize {
			return nil, 0, "", errors.New("candidate manifest page binding mismatch")
		}
		expectedHash, expectedSize = chunk.SHA256, chunk.Size
		if expectedSize > goModuleMaxFileBytes || expectedSize > goModuleMaxTotalBytes-usedBytes {
			return nil, expectedSize, expectedHash, nil
		}
		page, err := base64.StdEncoding.DecodeString(chunk.ContentBase64)
		if err != nil || len(page) != min(goCandidateModulePageBytes, int(expectedSize-offset)) {
			return nil, 0, "", errors.New("candidate manifest page bytes are invalid")
		}
		content = append(content, page...)
		if chunk.NextOffset == nil {
			break
		}
		if *chunk.NextOffset <= offset || *chunk.NextOffset > expectedSize || int64(len(content)) != *chunk.NextOffset {
			return nil, 0, "", errors.New("candidate manifest pages are not contiguous")
		}
		offset = *chunk.NextOffset
	}
	if int64(len(content)) != expectedSize {
		return nil, 0, "", errors.New("candidate manifest is incomplete")
	}
	return content, expectedSize, expectedHash, nil
}

// ValidateCandidateGoModuleInventory checks a v2 record against a previously
// validated committed inventory and an exact candidate observation. It does
// no Git or filesystem I/O and is suitable for graph replay validation.
func ValidateCandidateGoModuleInventory(inventory GoModuleInventory, identity repository.Identity, expected worktree.Candidate, base GoModuleInventory) error {
	if err := ValidateGoModuleInventory(base, identity); err != nil {
		return errors.New("candidate module inventory base is invalid")
	}
	repositoryID, err := identity.ID()
	if err != nil {
		return err
	}
	candidateID, err := expected.ID()
	if err != nil {
		return err
	}
	if expected.Head != identity.Commit || inventory.Version != GoModuleInventoryVersionCandidate || inventory.RepositoryID != repositoryID || inventory.Commit != identity.Commit || inventory.Tree != identity.Tree || inventory.CandidateID != candidateID || inventory.CandidateFilesHash != expected.FilesHash || inventory.BaseInventoryDigest != base.Digest {
		return errors.New("candidate module inventory identity differs")
	}
	return ValidateGoModuleInventoryRecord(inventory)
}
