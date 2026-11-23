package ri

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

const (
	goGenerationContextSchema = "engorch.ri.go-generation-context.v1"
	goGenerationContextLimit  = 8 << 10
	goGenerationContextFiles  = 32
	goGenerationContextRecord = 64
)

// GoGenerationContext is a bounded, partial prompt excerpt set for committed
// generator ownership evidence. ContextFiles are transient bytes and are not
// persisted; Selected binds every exposed excerpt to a full committed source.
type GoGenerationContext struct {
	Schema                string                     `json:"schema"`
	Version               int                        `json:"version"`
	SourceID              string                     `json:"source_id"`
	MetadataDigest        string                     `json:"metadata_digest"`
	SeedDigest            string                     `json:"seed_digest"`
	Coverage              string                     `json:"coverage"`
	Selected              []taskcontext.SelectedFile `json:"selected"`
	SelectedBytes         int                        `json:"selected_bytes"`
	DiscoveryOmittedCount int                        `json:"discovery_omitted_count"`
	DiscoveryOmissions    []taskcontext.Omission     `json:"discovery_omissions"`
	OmittedCount          int                        `json:"omitted_count"`
	Omissions             []taskcontext.Omission     `json:"omissions"`
	Truncated             bool                       `json:"truncated"`
	ContextFiles          []taskcontext.File         `json:"-"`
	Digest                string                     `json:"digest"`
}

type generationContextCandidate struct {
	path   string
	reason string
	rank   int
}

// CompileGoGenerationContext selects at most 8 KiB of complete committed files
// for generator owners and their narrowly established tool sources.
// It does not execute generation commands or turn partial discovery into proof
// that no other generator exists.
func CompileGoGenerationContext(metadata GoGenerationMetadata, objective string) (GoGenerationContext, error) {
	if err := ValidateGoGenerationMetadata(metadata); err != nil {
		return GoGenerationContext{}, fmt.Errorf("validate Go generation metadata: %w", err)
	}
	if metadata.ContextFiles == nil || !utf8.ValidString(objective) || strings.TrimSpace(objective) == "" || len(objective) > 16<<10 {
		return GoGenerationContext{}, errors.New("Go generation context requires committed bytes and a bounded UTF-8 objective")
	}
	files := make(map[string]taskcontext.File, len(metadata.ContextFiles))
	for _, file := range metadata.ContextFiles {
		files[file.Path] = file
	}
	candidates := generationContextCandidates(metadata)
	selected := make([]taskcontext.SelectedFile, 0, min(goGenerationContextFiles, len(candidates)))
	selectedFiles := make([]taskcontext.File, 0, cap(selected))
	omissions := make([]taskcontext.Omission, 0)
	omitted := 0
	remaining := goGenerationContextLimit
	for index, candidate := range candidates {
		if len(selected) >= goGenerationContextFiles {
			omitted += len(candidates) - index
			for _, rest := range candidates[index:] {
				omissions = append(omissions, taskcontext.Omission{Path: rest.path, Reason: "generation_context_file_limit"})
			}
			break
		}
		file, ok := files[candidate.path]
		if !ok {
			omitted++
			omissions = append(omissions, taskcontext.Omission{Path: candidate.path, Reason: "generation_context_source_unavailable"})
			continue
		}
		if len(file.Content) > remaining {
			omitted++
			omissions = append(omissions, taskcontext.Omission{Path: candidate.path, Reason: "generation_context_complete_file_over_budget"})
			continue
		}
		if len(file.Content) == 0 || !utf8.Valid(file.Content) {
			omitted++
			reason := "generation_context_source_not_utf8"
			if len(file.Content) == 0 {
				reason = "generation_context_empty_source"
			}
			omissions = append(omissions, taskcontext.Omission{Path: candidate.path, Reason: reason})
			continue
		}
		fullHash := file.Hash
		entry := taskcontext.SelectedFile{
			Path: file.Path, Hash: fullHash, Start: 0, End: int64(len(file.Content)),
			ExcerptHash: fullHash, Reason: candidate.reason, Content: string(file.Content),
		}
		selected = append(selected, entry)
		selectedFiles = append(selectedFiles, file)
		remaining -= len(file.Content)
	}
	sort.Slice(omissions, func(i, j int) bool {
		if omissions[i].Path != omissions[j].Path {
			return omissions[i].Path < omissions[j].Path
		}
		return omissions[i].Reason < omissions[j].Reason
	})
	trimmed := len(omissions) > goGenerationContextRecord
	if trimmed {
		omissions = omissions[:goGenerationContextRecord]
	}
	discoveryOmissions := make([]taskcontext.Omission, len(metadata.Omissions))
	copy(discoveryOmissions, metadata.Omissions)
	result := GoGenerationContext{
		Schema: goGenerationContextSchema, Version: 1, SourceID: metadata.SourceID,
		MetadataDigest: metadata.Digest, SeedDigest: metadata.SeedDigest, Coverage: "PARTIAL",
		Selected: selected, DiscoveryOmittedCount: metadata.OmittedCount,
		DiscoveryOmissions: discoveryOmissions,
		OmittedCount:       omitted, Omissions: omissions,
		Truncated:    metadata.Truncated || metadata.OmissionsTrimmed || trimmed || omitted > len(omissions),
		ContextFiles: selectedFiles,
	}
	for _, file := range selected {
		result.SelectedBytes += int(file.End - file.Start)
	}
	body := result
	body.Digest = ""
	digest, err := canonical.Hash(goGenerationContextSchema, body)
	if err != nil {
		return GoGenerationContext{}, fmt.Errorf("hash Go generation context: %w", err)
	}
	result.Digest = digest
	if err := ValidateGoGenerationContext(result, metadata); err != nil {
		return GoGenerationContext{}, fmt.Errorf("validate compiled Go generation context: %w", err)
	}
	return result, nil
}

// ValidateGoGenerationContext checks replay bindings without Git access. Each
// selected body is complete and must hash to its committed source digest, so
// persisted JSON replay can verify it without transient source bytes.
func ValidateGoGenerationContext(result GoGenerationContext, metadata GoGenerationMetadata) error {
	if err := ValidateGoGenerationMetadata(metadata); err != nil {
		return fmt.Errorf("validate Go generation metadata: %w", err)
	}
	if result.Schema != goGenerationContextSchema || result.Version != 1 || result.SourceID != metadata.SourceID || result.MetadataDigest != metadata.Digest || result.SeedDigest != metadata.SeedDigest || result.Coverage != "PARTIAL" || safepath.RequireDigest(result.Digest) != nil || result.Selected == nil || result.DiscoveryOmissions == nil || result.Omissions == nil || result.OmittedCount < len(result.Omissions) || result.OmittedCount < 0 || len(result.Omissions) > goGenerationContextRecord || len(result.Selected) > goGenerationContextFiles || result.SelectedBytes < 0 || result.SelectedBytes > goGenerationContextLimit || len(result.ContextFiles) != 0 && len(result.ContextFiles) != len(result.Selected) {
		return fmt.Errorf("invalid Go generation context header or bounds: selected=%d selected_nil=%t selected_bytes=%d omitted=%d omissions=%d files=%d digest=%s", len(result.Selected), result.Selected == nil, result.SelectedBytes, result.OmittedCount, len(result.Omissions), len(result.ContextFiles), result.Digest)
	}
	if result.DiscoveryOmittedCount != metadata.OmittedCount || !equalOmissions(result.DiscoveryOmissions, metadata.Omissions) {
		return errors.New("Go generation context discovery omissions differ from metadata")
	}
	sourceByPath := make(map[string]repository.SourceDigest, len(metadata.Sources))
	for _, source := range metadata.Sources {
		sourceByPath[source.Path] = source
	}
	fileByPath := map[string]taskcontext.File{}
	for _, file := range result.ContextFiles {
		fileByPath[file.Path] = file
	}
	metadataFiles := map[string]taskcontext.File{}
	for _, file := range metadata.ContextFiles {
		metadataFiles[file.Path] = file
	}
	expectedReasons := map[string]string{}
	for _, candidate := range generationContextCandidates(metadata) {
		expectedReasons[candidate.path] = candidate.reason
	}
	bytesTotal := 0
	seen := map[string]bool{}
	for index, item := range result.Selected {
		if safepath.Relative(item.Path) != nil || seen[item.Path] || item.Start != 0 || item.End <= item.Start || item.End-item.Start != int64(len(item.Content)) || item.Hash == "" || safepath.RequireDigest(item.Hash) != nil || item.ExcerptHash != item.Hash || !utf8.ValidString(item.Content) || !allowedGenerationContextReason(item.Reason) || expectedReasons[item.Path] != item.Reason {
			return errors.New("Go generation context contains an invalid selected excerpt")
		}
		seen[item.Path] = true
		if index > 0 && result.Selected[index-1].Path == item.Path {
			return errors.New("Go generation context repeats a source path")
		}
		source, ok := sourceByPath[item.Path]
		if !ok || item.Hash != source.SHA256 || item.End != source.Bytes || !sameGenerationHash(item.Hash, []byte(item.Content)) {
			return errors.New("Go generation context excerpt differs from committed source reference")
		}
		if len(result.ContextFiles) > 0 {
			file, ok := fileByPath[item.Path]
			if !ok || file.Hash != item.Hash || item.End > int64(len(file.Content)) || string(file.Content[item.Start:item.End]) != item.Content {
				return errors.New("Go generation context excerpt differs from transient committed bytes")
			}
		}
		if len(metadata.ContextFiles) > 0 {
			file, ok := metadataFiles[item.Path]
			if !ok || item.End > int64(len(file.Content)) || string(file.Content[item.Start:item.End]) != item.Content {
				return errors.New("Go generation context excerpt differs from metadata source bytes")
			}
		}
		bytesTotal += int(item.End - item.Start)
	}
	if bytesTotal != result.SelectedBytes {
		return errors.New("Go generation context selected byte total mismatch")
	}
	if (metadata.Truncated || metadata.OmissionsTrimmed || result.OmittedCount > len(result.Omissions)) && !result.Truncated {
		return errors.New("Go generation context hides a declared truncation")
	}
	for index, omission := range result.Omissions {
		if omission.Path == "" || omission.Reason == "" || !utf8.ValidString(omission.Path) || !utf8.ValidString(omission.Reason) || index > 0 && (result.Omissions[index-1].Path > omission.Path || result.Omissions[index-1].Path == omission.Path && result.Omissions[index-1].Reason > omission.Reason) {
			return errors.New("Go generation context omissions are invalid or unsorted")
		}
	}
	body := result
	body.Digest = ""
	expected, err := canonical.Hash(goGenerationContextSchema, body)
	if err != nil || expected != result.Digest {
		return errors.New("Go generation context digest mismatch")
	}
	return nil
}

func generationContextCandidates(metadata GoGenerationMetadata) []generationContextCandidate {
	byPath := map[string]generationContextCandidate{}
	add := func(path, reason string, rank int) {
		if path == "" {
			return
		}
		prior, ok := byPath[path]
		if !ok || rank < prior.rank {
			byPath[path] = generationContextCandidate{path: path, reason: reason, rank: rank}
		}
	}
	for _, binding := range metadata.Bindings {
		add(binding.GeneratorPath, "generation_owner", 0)
		add(binding.GeneratedPath, "generation_output", 4)
		for _, ref := range binding.ToolSources {
			switch ref.Role {
			case "generator_template":
				add(ref.Path, "generation_template", 1)
			case "generator_source":
				add(ref.Path, "generation_tool", 2)
			case "build_definition":
				add(ref.Path, "generation_build", 3)
			}
		}
	}
	result := make([]generationContextCandidate, 0, len(byPath))
	for _, candidate := range byPath {
		result = append(result, candidate)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].rank != result[j].rank {
			return result[i].rank < result[j].rank
		}
		return result[i].path < result[j].path
	})
	return result
}

func equalOmissions(a, b []taskcontext.Omission) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func allowedGenerationContextReason(reason string) bool {
	switch reason {
	case "generation_owner", "generation_tool", "generation_template", "generation_build", "generation_output":
		return true
	default:
		return false
	}
}
