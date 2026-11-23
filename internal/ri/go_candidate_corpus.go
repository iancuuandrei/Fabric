package ri

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

const goCandidateCorpusVersion = 1

// Eight admitted files times the existing one MiB file cap bounds candidate
// reads to the same eight MiB aggregate budget as the committed corpus.
const goCandidateCorpusMaxFiles = 8

// GoCandidateCorpus is a bounded, read-only candidate graph observation. Graph
// contains no source bytes. Coverage remains PARTIAL: omitted paths and absent
// facts never establish that a candidate has no other Go changes.
type GoCandidateCorpus struct {
	Version                   int                `json:"version"`
	Source                    Source             `json:"source"`
	CandidateID               string             `json:"candidate_id"`
	CandidateFilesHash        string             `json:"candidate_files_hash"`
	ProducerSHA256            string             `json:"producer_sha256"`
	BaseGraphDigest           string             `json:"base_graph_digest"`
	BaseModuleInventoryDigest string             `json:"base_module_inventory_digest"`
	ModuleInventory           GoModuleInventory  `json:"module_inventory"`
	Graph                     GoEngineeringGraph `json:"graph"`
	ChangedPaths              []string           `json:"changed_paths"`
	// AdmittedPaths are candidate Go paths outside the bounded base graph.
	// They are observed candidate inputs, not claims of a known base diff.
	AdmittedPaths    []string               `json:"admitted_paths"`
	DeletedPaths     []string               `json:"deleted_paths"`
	OmittedCount     int                    `json:"omitted_count"`
	Omissions        []taskcontext.Omission `json:"omissions"`
	OmissionsTrimmed bool                   `json:"omissions_trimmed"`
	Coverage         string                 `json:"coverage"`
	// CandidateFactCacheHits/Misses are local diagnostics only. They are never
	// serialized into a graph, review record, prompt, or semantic identity.
	CandidateFactCacheHits   int `json:"-"`
	CandidateFactCacheMisses int `json:"-"`
}

// CollectCandidateGoCorpus derives a candidate-bound graph under one shared
// read-lease ownership interval. Close verification failures are propagated.
func CollectCandidateGoCorpus(ctx context.Context, identity repository.Identity, binding worktree.Binding, expected worktree.Candidate, baseGraph GoEngineeringGraph, baseInventory GoModuleInventory, client Client, cacheDir string) (GoCandidateCorpus, error) {
	lease, err := worktree.AcquireRead(binding.Request)
	if err != nil {
		return GoCandidateCorpus{}, err
	}
	var result GoCandidateCorpus
	collectErr := lease.WithOwnership(binding.Request, func(worktree.LeaseIdentity) error {
		var inner error
		result, inner = collectCandidateGoCorpusLeased(ctx, identity, binding, expected, baseGraph, baseInventory, client, cacheDir)
		return inner
	})
	closeErr := lease.Close()
	if err := errors.Join(collectErr, closeErr); err != nil {
		return GoCandidateCorpus{}, err
	}
	return result, nil
}

// CollectCandidateGoCorpus derives a candidate-bound Go graph from a validated
// committed base graph. It reads only bounded candidate Go files under a shared
// lease, never invokes a provider, writes a journal, or retries an observation.
func collectCandidateGoCorpusLeased(ctx context.Context, identity repository.Identity, binding worktree.Binding, expected worktree.Candidate, baseGraph GoEngineeringGraph, baseInventory GoModuleInventory, client Client, cacheDir string) (GoCandidateCorpus, error) {
	result := GoCandidateCorpus{Version: goCandidateCorpusVersion, ChangedPaths: []string{}, AdmittedPaths: []string{}, DeletedPaths: []string{}, Omissions: []taskcontext.Omission{}, Coverage: "PARTIAL"}
	source, err := FromRepository(identity)
	if err != nil {
		return result, err
	}
	if err := ValidateGoEngineeringGraph(baseGraph); err != nil {
		return result, err
	}
	if baseGraph.SourceID != source.RepositoryID || baseGraph.CandidateID != "" || baseGraph.ProducerSHA256 != client.ExecutableHash || baseGraph.ModuleInventory == nil || baseInventory.Digest != baseGraph.ModuleInventory.Digest || ValidateGoModuleInventory(baseInventory, identity) != nil {
		return result, errors.New("candidate Go corpus requires a committed module-backed base graph")
	}
	if cacheDir != "" && (!filepath.IsAbs(cacheDir) || filepath.Clean(cacheDir) != cacheDir || filepath.Clean(cacheDir) == filepath.VolumeName(cacheDir)+string(filepath.Separator)) {
		return result, errors.New("candidate Go corpus cache directory must be absolute, clean, and non-root")
	}
	if err := expected.ValidateBinding(binding); err != nil {
		return result, err
	}
	candidateID, err := expected.ID()
	if err != nil {
		return result, err
	}
	inventory, err := CollectCandidateGoModuleInventory(ctx, identity, binding, expected, baseInventory)
	if err != nil {
		return result, err
	}
	if err := ValidateCandidateGoModuleInventory(inventory, identity, expected, baseInventory); err != nil {
		return result, err
	}
	result.Source, result.CandidateID, result.CandidateFilesHash, result.ProducerSHA256 = source, candidateID, expected.FilesHash, client.ExecutableHash
	result.BaseGraphDigest, result.BaseModuleInventoryDigest, result.ModuleInventory = baseGraph.Digest, baseInventory.Digest, cloneGoModuleInventory(inventory)

	observed, states, err := worktree.Capture(ctx, binding)
	if err != nil || observed != expected {
		if err != nil {
			return result, err
		}
		return result, errors.New("candidate changed before corpus read")
	}
	stateByPath := make(map[string]worktree.FileState, len(states))
	for _, state := range states {
		stateByPath[state.Path] = state
	}
	baseByPath := make(map[string]GoGraphFile, len(baseGraph.Files))
	for _, file := range baseGraph.Files {
		baseByPath[file.Facts.Path] = file
	}
	selected := make([]string, 0, goCandidateCorpusMaxFiles)
	changedBase := make(map[string]bool)
	droppedBase := make(map[string]bool)
	for path, file := range baseByPath {
		state, exists := stateByPath[path]
		if !exists {
			result.DeletedPaths = append(result.DeletedPaths, path)
			continue
		}
		if state.Hash != file.Facts.SourceSHA256 {
			changedBase[path] = true
			if safepath.Writable(path) != nil || !taskcontext.EligiblePath(path) {
				candidateCorpusOmit(&result, path, "unsafe_or_sensitive_path")
				droppedBase[path] = true
				continue
			}
			selected = append(selected, path)
			result.ChangedPaths = append(result.ChangedPaths, path)
		}
	}
	for path := range stateByPath {
		if filepath.Ext(path) == ".go" {
			if _, baseFile := baseByPath[path]; !baseFile {
				if safepath.Writable(path) != nil || !taskcontext.EligiblePath(path) {
					candidateCorpusOmit(&result, path, "unsafe_or_sensitive_path")
					continue
				}
				selected = append(selected, path)
			}
		}
	}
	sort.Strings(selected)
	sort.Strings(result.ChangedPaths)
	sort.Strings(result.DeletedPaths)
	if len(selected) > goCandidateCorpusMaxFiles {
		for _, path := range selected[goCandidateCorpusMaxFiles:] {
			candidateCorpusOmit(&result, path, "file_budget")
			if changedBase[path] {
				droppedBase[path] = true
			}
		}
		selected = selected[:goCandidateCorpusMaxFiles]
	}

	stream, err := client.OpenStream(ctx)
	if err != nil {
		return result, err
	}
	defer stream.Close()
	replacements := make([]GoGraphFileInput, 0, len(selected))
	overlayDeletes := append([]string(nil), result.DeletedPaths...)
	deleteBase := func(path string) {
		if changedBase[path] {
			droppedBase[path] = true
		}
	}
	for _, path := range selected {
		state := stateByPath[path]
		if filepath.Ext(path) != ".go" || safepath.Writable(path) != nil || !taskcontext.EligiblePath(path) {
			candidateCorpusOmit(&result, path, "unsafe_or_sensitive_path")
			deleteBase(path)
			continue
		}
		content, err := readCandidateGoCorpusFile(ctx, binding, expected, candidateID, state)
		if err != nil {
			candidateCorpusOmit(&result, path, "candidate_read_failed")
			deleteBase(path)
			continue
		}
		if len(content) > goCorpusMaxFileBytes {
			candidateCorpusOmit(&result, path, "file_byte_limit")
			deleteBase(path)
			continue
		}
		set := token.NewFileSet()
		parsed, parseErr := parser.ParseFile(set, path, content, parser.PackageClauseOnly)
		if parseErr != nil || parsed == nil || parsed.Name == nil {
			candidateCorpusOmit(&result, path, "invalid_package_clause")
			deleteBase(path)
			continue
		}
		facts, err := stream.GoFileFacts(ctx, path, content, cacheDir)
		if err != nil {
			return result, fmt.Errorf("candidate Go facts %q: %w", path, err)
		}
		switch facts.Cache {
		case "hit":
			result.CandidateFactCacheHits++
		case "miss":
			result.CandidateFactCacheMisses++
		}
		binding, err := DeclaredGoPackageBinding(inventory, path, parsed.Name.Name)
		if err != nil {
			return result, err
		}
		replacements = append(replacements, GoGraphFileInput{Facts: facts, Source: content, Package: binding})
		if !changedBase[path] {
			result.AdmittedPaths = append(result.AdmittedPaths, path)
		}
	}
	sort.Strings(result.AdmittedPaths)
	// A binding is retained only if neither endpoint changed. New or modified
	// directives are not inferred from candidate text in this v1 slice.
	overlayDeleteSet := make(map[string]bool, len(overlayDeletes)+len(droppedBase))
	for _, path := range overlayDeletes {
		overlayDeleteSet[path] = true
	}
	for path := range droppedBase {
		overlayDeleteSet[path] = true
	}
	overlayDeletes = overlayDeletes[:0]
	for path := range overlayDeleteSet {
		overlayDeletes = append(overlayDeletes, path)
	}
	changed := make(map[string]bool, len(result.ChangedPaths)+len(overlayDeletes))
	for _, path := range result.ChangedPaths {
		changed[path] = true
	}
	for _, path := range overlayDeletes {
		changed[path] = true
	}
	generators := make([]GoGeneratorBinding, 0, len(baseGraph.Generators))
	for _, relation := range baseGraph.Generators {
		if !changed[relation.GeneratorPath] && !changed[relation.GeneratedPath] {
			generators = append(generators, relation)
		}
	}
	sort.Strings(overlayDeletes)
	overlay := GoGraphOverlayInput{BaseDigest: baseGraph.Digest, CandidateID: candidateID, ProducerSHA256: client.ExecutableHash, Replacements: replacements, Deleted: overlayDeletes, Generators: generators, ModuleInventory: &inventory}
	if len(replacements) == 0 && len(overlayDeletes) == 0 {
		files, err := rebindGoGraphFiles(baseGraph.Files, source.RepositoryID, &inventory)
		if err != nil {
			return result, err
		}
		result.Graph, err = buildGoEngineeringGraph(source.RepositoryID, candidateID, client.ExecutableHash, files, generators, &inventory)
	} else {
		result.Graph, err = ApplyGoEngineeringOverlay(baseGraph, overlay)
	}
	if err != nil {
		return result, err
	}
	after, _, err := worktree.Capture(ctx, binding)
	if err != nil || after != expected {
		if err != nil {
			return result, err
		}
		return result, errors.New("candidate changed during corpus read")
	}
	return result, nil
}

func candidateCorpusOmit(result *GoCandidateCorpus, path, reason string) {
	result.OmittedCount++
	if len(result.Omissions) >= goCorpusMaxOmissions {
		result.OmissionsTrimmed = true
		return
	}
	if safepath.Relative(path) != nil || !taskcontext.EligiblePath(path) {
		path = "[redacted]"
	}
	result.Omissions = append(result.Omissions, taskcontext.Omission{Path: path, Reason: reason})
}

func readCandidateGoCorpusFile(ctx context.Context, binding worktree.Binding, expected worktree.Candidate, candidateID string, state worktree.FileState) ([]byte, error) {
	if state.Hash == "" || state.Path == "" {
		return nil, errors.New("candidate file state is invalid")
	}
	var content []byte
	var expectedSize int64 = -1
	for offset := int64(0); ; {
		chunk, err := worktree.ReadSource(ctx, binding, expected, state.Path, offset, 32768)
		if err != nil {
			return nil, err
		}
		if chunk.CandidateID != candidateID || chunk.Path != state.Path || chunk.SHA256 != state.Hash || chunk.Offset != offset || chunk.Size > goCorpusMaxFileBytes || expectedSize >= 0 && chunk.Size != expectedSize {
			return nil, errors.New("candidate source page binding mismatch")
		}
		expectedSize = chunk.Size
		page, err := base64.StdEncoding.DecodeString(chunk.ContentBase64)
		if err != nil {
			return nil, errors.New("candidate source page encoding is invalid")
		}
		content = append(content, page...)
		if chunk.NextOffset == nil {
			break
		}
		if *chunk.NextOffset != int64(len(content)) || *chunk.NextOffset <= offset {
			return nil, errors.New("candidate source pages are not contiguous")
		}
		offset = *chunk.NextOffset
	}
	if int64(len(content)) != expectedSize || !utf8.Valid(content) {
		return nil, errors.New("candidate source content is incomplete or invalid UTF-8")
	}
	sum := sha256.Sum256(content)
	if hex.EncodeToString(sum[:]) != state.Hash {
		return nil, errors.New("candidate source digest mismatch")
	}
	return content, nil
}
