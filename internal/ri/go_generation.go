package ri

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

const (
	goGenerationSchema        = "engorch.ri.go-generation.v1"
	goGenerationDiscoveryV1   = "sibling-go-v1"
	goGenerationMaxSeedPaths  = 64
	goGenerationMaxGoAttempts = 64
	goGenerationMaxFiles      = 64
	goGenerationMaxFileBytes  = 1 << 20
	goGenerationMaxTotalBytes = 8 << 20
	goGenerationMaxOmissions  = 64
)

// GoGenerationMetadata is a bounded, partial inventory of literal Go
// generation directives associated with selected source paths. It never
// executes commands or claims that an incomplete inventory proves absence.
type GoGenerationMetadata struct {
	Schema                  string                    `json:"schema"`
	Version                 int                       `json:"version"`
	SourceID                string                    `json:"source_id"`
	SeedPaths               []string                  `json:"seed_paths"`
	SeedDigest              string                    `json:"seed_digest"`
	Coverage                string                    `json:"coverage"`
	Bindings                []GoGenerationBinding     `json:"bindings"`
	Sources                 []repository.SourceDigest `json:"sources"`
	ContextFiles            []taskcontext.File        `json:"-"`
	InventoryFiles          int                       `json:"inventory_files"`
	AttemptedDirectiveFiles int                       `json:"attempted_directive_files"`
	OmittedCount            int                       `json:"omitted_count"`
	Omissions               []taskcontext.Omission    `json:"omissions"`
	OmissionsTrimmed        bool                      `json:"omissions_trimmed"`
	Truncated               bool                      `json:"truncated"`
	Digest                  string                    `json:"digest"`
}

// GoGenerationBinding records one literal directive and its committed output.
// Supporting build/tool sources are included only when a narrow Makefile and
// go:embed pattern establishes an unambiguous committed source relationship.
type GoGenerationBinding struct {
	GeneratorPath          string                  `json:"generator_path"`
	GeneratedPath          string                  `json:"generated_path"`
	Directive              string                  `json:"directive"`
	DirectiveRange         GoRange                 `json:"directive_range"`
	Command                string                  `json:"command"`
	DirectiveSource        repository.SourceDigest `json:"directive_source"`
	OutputSource           repository.SourceDigest `json:"output_source"`
	ToolSources            []GoGenerationSourceRef `json:"tool_sources"`
	ToolProvenanceComplete bool                    `json:"tool_provenance_complete"`
}

// GoGenerationSourceRef identifies committed supporting source without
// asserting that the referenced source was compiled or executed.
type GoGenerationSourceRef struct {
	Path   string                  `json:"path"`
	Role   string                  `json:"role"`
	Source repository.SourceDigest `json:"source"`
}

type goGenerationRead struct {
	entry   repository.SourceEntry
	content []byte
	digest  repository.SourceDigest
}

type goGenerationBuffer struct {
	data  []byte
	limit int64
}

// Write rejects bytes beyond the exact object size admitted by the Git batch header.
func (b *goGenerationBuffer) Write(data []byte) (int, error) {
	if int64(len(data)) > b.limit-int64(len(b.data)) {
		return 0, errors.New("committed generator source exceeded admitted bound")
	}
	b.data = append(b.data, data...)
	return len(data), nil
}

type goGenerationSink struct{ buffer *goGenerationBuffer }

// Write forwards one committed blob chunk into its bounded buffer.
func (s goGenerationSink) Write(data []byte) (int, error) { return s.buffer.Write(data) }

// Close does not take ownership of the shared source buffer.
func (s goGenerationSink) Close() error { return nil }

// DiscoverCommittedGoGenerators observes literal generation declarations in
// seed files and a bounded deterministic set of Go siblings. It reads only
// committed blobs, never follows working-tree edits, and never runs a tool.
// Coverage is always PARTIAL because the bounded sibling search is not a proof
// that no other generator exists elsewhere in the repository.
func DiscoverCommittedGoGenerators(ctx context.Context, identity repository.Identity, seedPaths []string) (GoGenerationMetadata, error) {
	result := GoGenerationMetadata{
		Schema: goGenerationSchema, Version: 1, Coverage: "PARTIAL",
		SeedPaths: []string{}, Bindings: []GoGenerationBinding{}, Sources: []repository.SourceDigest{},
		ContextFiles: []taskcontext.File{}, Omissions: []taskcontext.Omission{},
	}
	source, err := FromRepository(identity)
	if err != nil {
		return result, err
	}
	result.SourceID = source.RepositoryID
	seeds, err := normalizeGoGenerationSeeds(seedPaths)
	if err != nil {
		return result, err
	}
	result.SeedPaths = seeds
	seedDigest, err := canonical.Hash("harness.ri.go-generation.seeds.v1", struct {
		SourceID string   `json:"source_id"`
		Recipe   string   `json:"recipe"`
		Seeds    []string `json:"seeds"`
	}{source.RepositoryID, goGenerationDiscoveryV1, seeds})
	if err != nil {
		return result, err
	}
	result.SeedDigest = seedDigest
	appendOmission := func(name, reason string) {
		result.OmittedCount++
		if len(result.Omissions) >= goGenerationMaxOmissions {
			result.OmissionsTrimmed = true
			return
		}
		if name != "" && (safepath.Relative(name) != nil || !taskcontext.EligiblePath(name)) {
			name = "[redacted]"
		}
		result.Omissions = append(result.Omissions, taskcontext.Omission{Path: name, Reason: reason})
	}

	seedSet := make(map[string]bool, len(seeds))
	directories := make(map[string]bool, len(seeds))
	for _, seed := range seeds {
		seedSet[seed] = true
		directories[path.Dir(seed)] = true
	}
	entries := make(map[string]repository.SourceEntry)
	goEntries := make([]repository.SourceEntry, 0, len(seeds)+16)
	categoryCounts := [4]int{}
	makefiles := make([]repository.SourceEntry, 0, len(seeds)+1)
	err = repository.VisitSource(ctx, identity, func(entry repository.SourceEntry) error {
		if filepath.Ext(entry.Path) == ".go" && directories[path.Dir(entry.Path)] {
			result.InventoryFiles++
			group := goGenerationCandidateGroup(entry.Path, seedSet[entry.Path])
			if group == 0 || categoryCounts[group] < goGenerationMaxGoAttempts {
				entries[entry.Path] = entry
				goEntries = append(goEntries, entry)
				categoryCounts[group]++
			} else {
				result.Truncated = true
				appendOmission(entry.Path, "sibling_inventory_limit")
			}
		}
		if path.Base(entry.Path) == "Makefile" && (entry.Path == "Makefile" || directories[path.Dir(entry.Path)]) {
			entries[entry.Path] = entry
			makefiles = append(makefiles, entry)
		}
		return nil
	})
	if err != nil {
		return GoGenerationMetadata{}, err
	}
	for _, seed := range seeds {
		if _, ok := entries[seed]; !ok {
			appendOmission(seed, "seed_not_in_committed_tree")
		}
	}
	ordered := orderGoGenerationCandidates(goEntries, seedSet)
	if len(ordered) > goGenerationMaxGoAttempts {
		for _, item := range ordered[goGenerationMaxGoAttempts:] {
			appendOmission(item.Path, "sibling_attempt_limit")
		}
		ordered = ordered[:goGenerationMaxGoAttempts]
		result.Truncated = true
	}
	for _, candidate := range ordered {
		if candidate.Kind != "file" || !eligibleGoGenerationPath(candidate.Path) {
			appendOmission(candidate.Path, "ineligible_generator_source")
		}
	}
	for _, candidate := range makefiles {
		if candidate.Kind != "file" || !eligibleGoGenerationPath(candidate.Path) {
			appendOmission(candidate.Path, "ineligible_build_definition")
		}
	}

	selected := make([]repository.SourceEntry, 0, len(ordered)+len(makefiles))
	for _, candidate := range ordered {
		if candidate.Kind == "file" && eligibleGoGenerationPath(candidate.Path) {
			selected = append(selected, candidate)
		}
	}
	for _, makefile := range makefiles {
		if makefile.Kind == "file" && eligibleGoGenerationPath(makefile.Path) {
			selected = append(selected, makefile)
		}
	}
	selected = orderedUniqueSourceEntries(selected)
	reads, sizeTotal, omitted, err := copyGoGenerationSources(ctx, identity, selected, 0)
	if err != nil {
		return GoGenerationMetadata{}, err
	}
	for name, reason := range omitted {
		appendOmission(name, reason)
		if reason == "metadata_file_limit" {
			result.Truncated = true
		}
	}
	for name, read := range reads {
		result.Sources = append(result.Sources, read.digest)
		result.ContextFiles = append(result.ContextFiles, taskcontext.File{Path: name, Hash: read.digest.SHA256, Content: append([]byte(nil), read.content...)})
	}
	sort.Slice(result.Sources, func(i, j int) bool { return result.Sources[i].Path < result.Sources[j].Path })
	sort.Slice(result.ContextFiles, func(i, j int) bool { return result.ContextFiles[i].Path < result.ContextFiles[j].Path })

	makefileContents := make(map[string][]byte, len(makefiles))
	for _, file := range makefiles {
		if read, ok := reads[file.Path]; ok {
			makefileContents[file.Path] = read.content
		}
	}
	parsed := make([]parsedGoGeneration, 0)
	for _, candidate := range ordered {
		read, ok := reads[candidate.Path]
		if !ok {
			continue
		}
		result.AttemptedDirectiveFiles++
		directives, syntaxOK := parseGoGenerationDirectives(candidate.Path, read.content)
		if !syntaxOK {
			appendOmission(candidate.Path, "invalid_go_generator_source")
			continue
		}
		for _, directive := range directives {
			command, generated, parseErr := parseLiteralGoGenerate(candidate.Path, directive.Text)
			if parseErr != nil {
				appendOmission(candidate.Path, "unsupported_generate_directive")
				continue
			}
			parsed = append(parsed, parsedGoGeneration{source: read, directive: directive, command: command, generated: generated})
		}
	}
	outputEntries := make([]repository.SourceEntry, 0, len(parsed))
	for _, directive := range parsed {
		entry, ok := entries[directive.generated]
		if !ok {
			continue
		}
		if entry.Kind != "file" || !eligibleGoGenerationPath(entry.Path) {
			appendOmission(directive.generated, "generated_output_ineligible")
			continue
		}
		outputEntries = append(outputEntries, entry)
	}
	neededOutputs := make(map[string]bool, len(parsed))
	for _, directive := range parsed {
		if _, ok := entries[directive.generated]; !ok {
			neededOutputs[directive.generated] = true
		}
	}
	if len(neededOutputs) > 0 {
		err = repository.VisitSource(ctx, identity, func(entry repository.SourceEntry) error {
			if neededOutputs[entry.Path] {
				entries[entry.Path] = entry
				delete(neededOutputs, entry.Path)
			}
			return nil
		})
		if err != nil {
			return GoGenerationMetadata{}, err
		}
		for output := range neededOutputs {
			appendOmission(output, "generated_output_not_in_committed_tree")
		}
		for _, directive := range parsed {
			entry, ok := entries[directive.generated]
			if !ok || entry.Kind != "file" || !eligibleGoGenerationPath(entry.Path) {
				continue
			}
			outputEntries = append(outputEntries, entry)
		}
	}
	outputEntries = uniqueSourceEntries(outputEntries)
	outputEntries = remainingGoGenerationEntries(outputEntries, reads, appendOmission)
	outputReads, outputBytes, outputOmitted, err := copyGoGenerationSources(ctx, identity, outputEntries, sizeTotal)
	if err != nil {
		return GoGenerationMetadata{}, err
	}
	for name, reason := range outputOmitted {
		appendOmission(name, reason)
	}
	for name, read := range outputReads {
		reads[name] = read
		result.Sources = append(result.Sources, read.digest)
		result.ContextFiles = append(result.ContextFiles, taskcontext.File{Path: name, Hash: read.digest.SHA256, Content: append([]byte(nil), read.content...)})
	}
	toolDirectories := map[string]bool{}
	toolBindings := make(map[string]goGenerationToolMatch)
	for _, directive := range parsed {
		match := matchGoGenerationToolDetails(directive.command, directive.source.entry.Path, makefileContents)
		key := directive.source.entry.Path + "\x00" + fmt.Sprint(directive.directive.Range.StartByte)
		toolBindings[key] = match
		if match.complete {
			toolDirectories[match.directory] = true
		}
	}
	toolEntries := []repository.SourceEntry{}
	if len(toolDirectories) > 0 {
		toolEntryByPath := map[string]repository.SourceEntry{}
		toolDirectoryTruncated := map[string]bool{}
		err = repository.VisitSource(ctx, identity, func(entry repository.SourceEntry) error {
			directory := path.Dir(entry.Path)
			if !toolDirectories[directory] {
				return nil
			}
			ext := filepath.Ext(entry.Path)
			if ext != ".go" && ext != ".tmpl" {
				toolDirectories[directory] = false
				return nil
			}
			if entry.Kind != "file" || !eligibleGoGenerationPath(entry.Path) {
				toolDirectories[directory] = false
				appendOmission(entry.Path, "ineligible_generator_tool_source")
				return nil
			}
			if len(toolEntryByPath) >= goGenerationMaxFiles {
				toolDirectoryTruncated[directory] = true
				return nil
			}
			toolEntryByPath[entry.Path] = entry
			return nil
		})
		if err != nil {
			return GoGenerationMetadata{}, err
		}
		for _, entry := range toolEntryByPath {
			toolEntries = append(toolEntries, entry)
		}
		sort.Slice(toolEntries, func(i, j int) bool { return toolEntries[i].Path < toolEntries[j].Path })
		for key, match := range toolBindings {
			if !match.complete || !toolDirectories[match.directory] || toolDirectoryTruncated[match.directory] {
				if toolDirectoryTruncated[match.directory] {
					appendOmission(match.directory, "tool_source_file_limit")
				}
				match.complete = false
				toolBindings[key] = match
				continue
			}
			for _, entry := range toolEntries {
				if path.Dir(entry.Path) == match.directory {
					match.paths = append(match.paths, entry.Path)
				}
			}
			toolBindings[key] = match
		}
	}
	toolEntries = remainingGoGenerationEntries(toolEntries, reads, appendOmission)
	toolReads, _, toolOmitted, err := copyGoGenerationSources(ctx, identity, toolEntries, sizeTotal+outputBytes)
	if err != nil {
		return GoGenerationMetadata{}, err
	}
	for name, reason := range toolOmitted {
		appendOmission(name, reason)
	}
	for name, read := range toolReads {
		reads[name] = read
		result.Sources = append(result.Sources, read.digest)
		result.ContextFiles = append(result.ContextFiles, taskcontext.File{Path: name, Hash: read.digest.SHA256, Content: append([]byte(nil), read.content...)})
	}
	for key, match := range toolBindings {
		if !match.complete {
			continue
		}
		contents := make(map[string][]byte, len(match.paths))
		for _, name := range match.paths {
			if read, ok := reads[name]; ok {
				contents[name] = read.content
			}
		}
		if !validGoGenerationToolSources(match.paths, contents) {
			match.complete = false
			toolBindings[key] = match
		}
	}

	sizeTotal += outputBytes
	outputFor := map[string][]parsedGoGeneration{}
	for _, directive := range parsed {
		outputFor[directive.generated] = append(outputFor[directive.generated], directive)
	}
	outputPaths := make([]string, 0, len(outputFor))
	for outputPath := range outputFor {
		outputPaths = append(outputPaths, outputPath)
	}
	sort.Strings(outputPaths)
	for _, outputPath := range outputPaths {
		directives := outputFor[outputPath]
		if len(directives) != 1 {
			appendOmission(outputPath, "ambiguous_generated_output")
			continue
		}
		generated, ok := reads[outputPath]
		if !ok {
			continue
		}
		markerOK, markerErr := generatedMarker(outputPath, generated.content)
		if markerErr != nil || !markerOK {
			appendOmission(outputPath, "generated_marker_missing_or_invalid")
			continue
		}
		directive := directives[0]
		binding := GoGenerationBinding{
			GeneratorPath: directive.source.entry.Path, GeneratedPath: outputPath, Directive: directive.directive.Text,
			DirectiveRange: directive.directive.Range, Command: directive.command,
			DirectiveSource: directive.source.digest, OutputSource: generated.digest,
			ToolSources: []GoGenerationSourceRef{},
		}
		key := directive.source.entry.Path + "\x00" + fmt.Sprint(directive.directive.Range.StartByte)
		match := toolBindings[key]
		if match.complete {
			if buildRead, ok := reads[match.buildPath]; ok {
				binding.ToolSources = append(binding.ToolSources, GoGenerationSourceRef{Path: match.buildPath, Role: "build_definition", Source: buildRead.digest})
			}
			for _, toolPath := range match.paths {
				if read, ok := reads[toolPath]; ok {
					binding.ToolSources = append(binding.ToolSources, GoGenerationSourceRef{Path: toolPath, Role: goGenerationSourceRole(toolPath, match.buildPath), Source: read.digest})
				}
			}
			if len(binding.ToolSources) == len(match.paths)+1 {
				binding.ToolProvenanceComplete = true
			}
		}
		if !binding.ToolProvenanceComplete {
			appendOmission(directive.source.entry.Path, "tool_source_provenance_incomplete")
		}
		if _, err := canonical.Bytes(binding); err != nil {
			appendOmission(directive.source.entry.Path, "generator_metadata_byte_limit")
			continue
		}
		result.Bindings = append(result.Bindings, binding)
	}
	sort.Slice(result.Omissions, func(i, j int) bool {
		if result.Omissions[i].Path != result.Omissions[j].Path {
			return result.Omissions[i].Path < result.Omissions[j].Path
		}
		return result.Omissions[i].Reason < result.Omissions[j].Reason
	})
	sort.Slice(result.Bindings, func(i, j int) bool {
		if result.Bindings[i].GeneratorPath != result.Bindings[j].GeneratorPath {
			return result.Bindings[i].GeneratorPath < result.Bindings[j].GeneratorPath
		}
		return result.Bindings[i].DirectiveRange.StartByte < result.Bindings[j].DirectiveRange.StartByte
	})
	sort.Slice(result.Sources, func(i, j int) bool { return result.Sources[i].Path < result.Sources[j].Path })
	sort.Slice(result.ContextFiles, func(i, j int) bool { return result.ContextFiles[i].Path < result.ContextFiles[j].Path })
	result.Sources = uniqueSourceDigests(result.Sources)
	result.ContextFiles = uniqueTaskContextFiles(result.ContextFiles)
	body := result
	body.Digest = ""
	encoded, err := canonical.Bytes(body)
	if err != nil {
		return GoGenerationMetadata{}, fmt.Errorf("canonical generator metadata: %w", err)
	}
	digest := sha256.Sum256(encoded)
	result.Digest = hex.EncodeToString(digest[:])
	return result, nil
}

// ValidateGoGenerationMetadata checks internal source, path, digest and range
// bindings without reading Git, parsing Go syntax, or invoking a generator.
func ValidateGoGenerationMetadata(metadata GoGenerationMetadata) error {
	if metadata.Schema != goGenerationSchema || metadata.Version != 1 || metadata.Coverage != "PARTIAL" || safepath.RequireDigest(metadata.SourceID) != nil || safepath.RequireDigest(metadata.SeedDigest) != nil || safepath.RequireDigest(metadata.Digest) != nil {
		return errors.New("invalid Go generation metadata header")
	}
	seeds, err := normalizeGoGenerationSeeds(metadata.SeedPaths)
	if err != nil || !equalStrings(seeds, metadata.SeedPaths) {
		return errors.New("Go generation metadata seed paths are not canonical")
	}
	expectedSeedDigest, err := canonical.Hash("harness.ri.go-generation.seeds.v1", struct {
		SourceID string   `json:"source_id"`
		Recipe   string   `json:"recipe"`
		Seeds    []string `json:"seeds"`
	}{metadata.SourceID, goGenerationDiscoveryV1, metadata.SeedPaths})
	if err != nil || expectedSeedDigest != metadata.SeedDigest {
		return errors.New("Go generation metadata seed binding mismatch")
	}
	if metadata.InventoryFiles < 0 || metadata.AttemptedDirectiveFiles < 0 || metadata.AttemptedDirectiveFiles > goGenerationMaxGoAttempts || metadata.AttemptedDirectiveFiles > metadata.InventoryFiles || metadata.OmittedCount < len(metadata.Omissions) || metadata.OmittedCount < 0 || len(metadata.Omissions) > goGenerationMaxOmissions || metadata.OmissionsTrimmed != (metadata.OmittedCount > len(metadata.Omissions)) || len(metadata.Sources) > goGenerationMaxFiles || metadata.ContextFiles != nil && len(metadata.ContextFiles) != len(metadata.Sources) || len(metadata.Bindings) > goGenerationMaxFiles {
		return errors.New("Go generation metadata exceeds its declared bounds")
	}
	if metadata.Sources == nil || metadata.Bindings == nil || metadata.Omissions == nil {
		return errors.New("Go generation metadata contains nil collections")
	}
	var contents map[string]taskcontext.File
	if metadata.ContextFiles != nil {
		contents = make(map[string]taskcontext.File, len(metadata.ContextFiles))
	}
	seenFolded := map[string]bool{}
	commit := ""
	var sourceBytes int64
	for index, source := range metadata.Sources {
		if source.RepositoryID != metadata.SourceID || !validGitObjectDigest(source.Commit) || len(source.Blob) != len(source.Commit) || !lowerHex(source.Blob) || source.Bytes < 0 || source.Bytes > goGenerationMaxFileBytes || safepath.Relative(source.Path) != nil || !eligibleGoGenerationPath(source.Path) || safepath.RequireDigest(source.SHA256) != nil {
			return errors.New("Go generation metadata contains an invalid committed source reference")
		}
		if index > 0 && metadata.Sources[index-1].Path >= source.Path {
			return errors.New("Go generation source references are not sorted and unique")
		}
		if source.Bytes > goGenerationMaxTotalBytes-sourceBytes {
			return errors.New("Go generation source references exceed aggregate byte limit")
		}
		sourceBytes += source.Bytes
		if commit == "" {
			commit = source.Commit
		} else if source.Commit != commit {
			return errors.New("Go generation metadata mixes committed revisions")
		}
		if contents != nil {
			file := metadata.ContextFiles[index]
			if file.Path != source.Path || file.Hash != source.SHA256 || int64(len(file.Content)) != source.Bytes || !sameGenerationHash(file.Hash, file.Content) || !utf8.Valid(file.Content) {
				return errors.New("Go generation context bytes differ from committed source reference")
			}
			contents[file.Path] = file
		}
		folded := strings.ToLower(source.Path)
		if seenFolded[folded] {
			return errors.New("Go generation source paths collide under case folding")
		}
		seenFolded[folded] = true
	}
	for index, omission := range metadata.Omissions {
		if omission.Path == "" || len(omission.Path) > 4096 || omission.Reason == "" || len(omission.Reason) > 128 || !utf8.ValidString(omission.Path) || !utf8.ValidString(omission.Reason) {
			return errors.New("Go generation metadata contains an invalid omission")
		}
		if index > 0 && (metadata.Omissions[index-1].Path > omission.Path || metadata.Omissions[index-1].Path == omission.Path && metadata.Omissions[index-1].Reason > omission.Reason) {
			return errors.New("Go generation omissions are not sorted")
		}
	}
	seenOutputs := map[string]bool{}
	for index, binding := range metadata.Bindings {
		if safepath.Relative(binding.GeneratorPath) != nil || filepath.Ext(binding.GeneratorPath) != ".go" || safepath.Relative(binding.GeneratedPath) != nil || filepath.Ext(binding.GeneratedPath) != ".go" || binding.GeneratorPath == binding.GeneratedPath || len(binding.Directive) > 4096 || binding.Command == "" || len(binding.Command) > 4096 || binding.DirectiveRange.StartByte < 0 || binding.DirectiveRange.EndByte <= binding.DirectiveRange.StartByte || binding.DirectiveRange.EndByte-binding.DirectiveRange.StartByte != len(binding.Directive) {
			return errors.New("Go generation metadata contains an invalid directive binding")
		}
		if seenOutputs[binding.GeneratedPath] {
			return errors.New("Go generation metadata repeats an output binding")
		}
		seenOutputs[binding.GeneratedPath] = true
		if index > 0 {
			previous := metadata.Bindings[index-1]
			if previous.GeneratorPath > binding.GeneratorPath || previous.GeneratorPath == binding.GeneratorPath && previous.DirectiveRange.StartByte >= binding.DirectiveRange.StartByte {
				return errors.New("Go generation bindings are not sorted")
			}
		}
		owner, ownerOK := contents[binding.GeneratorPath]
		_, ownerSourceOK := sourceFor(metadata.Sources, binding.GeneratorPath)
		_, outputSourceOK := sourceFor(metadata.Sources, binding.GeneratedPath)
		if !ownerSourceOK || !outputSourceOK || contents != nil && (!ownerOK || binding.DirectiveSource != sourceDigestFor(metadata.Sources, binding.GeneratorPath)) || binding.OutputSource != sourceDigestFor(metadata.Sources, binding.GeneratedPath) || binding.DirectiveSource != sourceDigestFor(metadata.Sources, binding.GeneratorPath) {
			return errors.New("Go generation binding source reference mismatch")
		}
		start, end := binding.DirectiveRange.StartByte, binding.DirectiveRange.EndByte
		if int64(end) > binding.DirectiveSource.Bytes {
			return errors.New("Go generation directive range exceeds committed owner size")
		}
		if contents != nil && (end > len(owner.Content) || string(owner.Content[start:end]) != binding.Directive || !rawGenerationCommentAtLineStart(owner.Content, start)) {
			return errors.New("Go generation directive range does not match committed owner bytes")
		}
		command, generated, parseErr := parseLiteralGoGenerate(binding.GeneratorPath, binding.Directive)
		if parseErr != nil || command != binding.Command || generated != binding.GeneratedPath {
			return errors.New("Go generation directive command or output binding mismatch")
		}
		if err := validateGenerationToolRefs(binding, contents); err != nil {
			return err
		}
		for _, ref := range binding.ToolSources {
			if ref.Source != sourceDigestFor(metadata.Sources, ref.Path) {
				return errors.New("Go generation tool reference differs from committed source digest")
			}
		}
	}
	body := metadata
	body.Digest = ""
	encoded, err := canonical.Bytes(body)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(encoded)
	if hex.EncodeToString(digest[:]) != metadata.Digest {
		return errors.New("Go generation metadata digest mismatch")
	}
	return nil
}

func rawGenerationCommentAtLineStart(content []byte, start int) bool {
	if start < 0 || start >= len(content) || (start > 0 && content[start-1] != '\n') {
		return false
	}
	line := content[start:]
	return bytes.HasPrefix(line, []byte("//go:generate ")) || bytes.HasPrefix(line, []byte("//go:generate\t"))
}

func validateGenerationToolRefs(binding GoGenerationBinding, contents map[string]taskcontext.File) error {
	if !binding.ToolProvenanceComplete {
		if len(binding.ToolSources) != 0 {
			return errors.New("incomplete tool provenance carries source references")
		}
		return nil
	}
	if len(binding.ToolSources) < 3 || len(binding.ToolSources) > goGenerationMaxFiles {
		return errors.New("complete tool provenance has an invalid source count")
	}
	builds, generators, templates := 0, 0, 0
	toolDir := ""
	seen := map[string]bool{}
	for _, ref := range binding.ToolSources {
		if seen[ref.Path] || ref.Source.Path != ref.Path || ref.Source.SHA256 == "" {
			return errors.New("Go generation tool references repeat or mismatch a source")
		}
		seen[ref.Path] = true
		if contents != nil {
			file, ok := contents[ref.Path]
			if !ok || file.Hash != ref.Source.SHA256 || !sameGenerationHash(ref.Source.SHA256, file.Content) {
				return errors.New("Go generation tool reference is not bound to complete source bytes")
			}
		}
		switch ref.Role {
		case "build_definition":
			if ref.Path != "Makefile" {
				return errors.New("Go generation build definition path is unsupported")
			}
			builds++
		case "generator_source":
			if filepath.Ext(ref.Path) != ".go" {
				return errors.New("Go generation tool source has an invalid extension")
			}
			if toolDir == "" {
				toolDir = path.Dir(ref.Path)
			} else if toolDir != path.Dir(ref.Path) {
				return errors.New("Go generation tool sources span multiple directories")
			}
			generators++
		case "generator_template":
			if filepath.Ext(ref.Path) != ".tmpl" || toolDir != "" && toolDir != path.Dir(ref.Path) {
				return errors.New("Go generation template is outside its tool directory")
			}
			toolDir = path.Dir(ref.Path)
			templates++
		default:
			return errors.New("Go generation tool reference has an unsupported role")
		}
	}
	if builds != 1 || generators == 0 || templates == 0 {
		return errors.New("complete Go generation provenance lacks a build, source, or template")
	}
	return nil
}

func sameGenerationHash(expected string, content []byte) bool {
	hash := sha256.Sum256(content)
	return expected == hex.EncodeToString(hash[:])
}

func sourceDigestFor(sources []repository.SourceDigest, name string) repository.SourceDigest {
	digest, _ := sourceFor(sources, name)
	return digest
}

func sourceFor(sources []repository.SourceDigest, name string) (repository.SourceDigest, bool) {
	index := sort.Search(len(sources), func(i int) bool { return sources[i].Path >= name })
	if index < len(sources) && sources[index].Path == name {
		return sources[index], true
	}
	return repository.SourceDigest{}, false
}

func validGitObjectDigest(value string) bool {
	return (len(value) == 40 || len(value) == 64) && lowerHex(value)
}

func lowerHex(value string) bool {
	return value != "" && strings.Trim(value, "0123456789abcdef") == ""
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func normalizeGoGenerationSeeds(paths []string) ([]string, error) {
	if len(paths) == 0 || len(paths) > goGenerationMaxSeedPaths {
		return nil, errors.New("Go generation requires 1..64 selected seed paths")
	}
	result := append([]string(nil), paths...)
	for _, name := range result {
		if safepath.Relative(name) != nil || filepath.Ext(name) != ".go" || !taskcontext.EligiblePath(name) || safepath.Writable(name) != nil {
			return nil, fmt.Errorf("invalid Go generator seed path %q", name)
		}
	}
	sort.Strings(result)
	for index := 1; index < len(result); index++ {
		if result[index] == result[index-1] {
			return nil, errors.New("Go generation seed paths must be unique")
		}
	}
	return result, nil
}

func eligibleGoGenerationPath(name string) bool {
	return safepath.Relative(name) == nil && taskcontext.EligiblePath(name) && safepath.Writable(name) == nil
}

func orderGoGenerationCandidates(entries []repository.SourceEntry, seeds map[string]bool) []repository.SourceEntry {
	result := append([]repository.SourceEntry(nil), entries...)
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		leftGroup, rightGroup := goGenerationCandidateGroup(left.Path, seeds[left.Path]), goGenerationCandidateGroup(right.Path, seeds[right.Path])
		if leftGroup != rightGroup {
			return leftGroup < rightGroup
		}
		return left.Path < right.Path
	})
	return result
}

func goGenerationCandidateGroup(name string, seed bool) int {
	if seed {
		return 0
	}
	base := path.Base(name)
	if base == "gen.go" || base == "generate.go" {
		return 1
	}
	if strings.HasSuffix(base, "_ext.go") {
		return 2
	}
	return 3
}

func copyGoGenerationSources(ctx context.Context, identity repository.Identity, selected []repository.SourceEntry, usedBytes int64) (map[string]goGenerationRead, int64, map[string]string, error) {
	reads := make(map[string]goGenerationRead)
	omitted := make(map[string]string)
	if len(selected) == 0 {
		return reads, 0, omitted, nil
	}
	selected = orderedUniqueSourceEntries(selected)
	if len(selected) > goGenerationMaxFiles {
		for _, entry := range selected[goGenerationMaxFiles:] {
			omitted[entry.Path] = "metadata_file_limit"
		}
		selected = selected[:goGenerationMaxFiles]
	}
	buffers := make(map[string]*goGenerationBuffer, len(selected))
	addedBytes := int64(0)
	identityID, err := identity.ID()
	if err != nil {
		return nil, 0, nil, err
	}
	err = repository.CopySelectedSourceBatch(ctx, identity, selected, func(entry repository.SourceEntry, size int64) (io.WriteCloser, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if size > goGenerationMaxFileBytes {
			omitted[entry.Path] = "file_byte_limit"
			return nil, nil
		}
		if usedBytes+addedBytes+size > goGenerationMaxTotalBytes {
			omitted[entry.Path] = "aggregate_byte_limit"
			return nil, nil
		}
		buffer := &goGenerationBuffer{limit: size}
		buffers[entry.Path] = buffer
		addedBytes += size
		return goGenerationSink{buffer}, nil
	}, func(entry repository.SourceEntry, digest *repository.SourceDigest) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if reason := omitted[entry.Path]; reason != "" {
			return nil
		}
		buffer := buffers[entry.Path]
		if buffer == nil || digest == nil || digest.RepositoryID != identityID || digest.Commit != identity.Commit || digest.Path != entry.Path || digest.Blob != entry.Object || digest.Bytes != int64(len(buffer.data)) {
			return errors.New("committed generator source binding mismatch")
		}
		if !utf8.Valid(buffer.data) {
			omitted[entry.Path] = "non_utf8"
			return nil
		}
		reads[entry.Path] = goGenerationRead{entry: entry, content: append([]byte(nil), buffer.data...), digest: *digest}
		return nil
	})
	return reads, addedBytes, omitted, err
}

func remainingGoGenerationEntries(entries []repository.SourceEntry, already map[string]goGenerationRead, omit func(string, string)) []repository.SourceEntry {
	result := make([]repository.SourceEntry, 0, min(len(entries), goGenerationMaxFiles-len(already)))
	for _, entry := range entries {
		if _, ok := already[entry.Path]; ok {
			continue
		}
		if len(already)+len(result) >= goGenerationMaxFiles {
			omit(entry.Path, "metadata_file_limit")
			continue
		}
		result = append(result, entry)
	}
	return result
}

type parsedGoDirective struct {
	Text  string
	Range GoRange
}

type parsedGoGeneration struct {
	source    goGenerationRead
	directive parsedGoDirective
	command   string
	generated string
}

func parseGoGenerationDirectives(name string, content []byte) ([]parsedGoDirective, bool) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, content, parser.ParseComments|parser.AllErrors)
	if err != nil || file == nil {
		return nil, false
	}
	tokenFile := fset.File(file.Pos())
	if tokenFile == nil {
		return nil, false
	}
	result := []parsedGoDirective{}
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if !strings.HasPrefix(comment.Text, "//go:generate") {
				continue
			}
			start, end := tokenFile.Offset(comment.Pos()), tokenFile.Offset(comment.End())
			if start < 0 || end <= start || end > len(content) {
				return nil, false
			}
			lineStart := start
			for lineStart > 0 && content[lineStart-1] != '\n' {
				lineStart--
			}
			text := string(content[start:end])
			if lineStart != start || !(strings.HasPrefix(text, "//go:generate ") || strings.HasPrefix(text, "//go:generate\t")) {
				continue
			}
			result = append(result, parsedGoDirective{Text: text, Range: GoRange{StartByte: start, EndByte: end}})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Range.StartByte < result[j].Range.StartByte })
	return result, true
}

func parseLiteralGoGenerate(sourcePath, directive string) (string, string, error) {
	if strings.ContainsAny(directive, "\r\n\x00") || !(strings.HasPrefix(directive, "//go:generate ") || strings.HasPrefix(directive, "//go:generate\t")) {
		return "", "", errors.New("directive is not a strict literal command")
	}
	commandText := strings.TrimLeft(strings.TrimPrefix(directive, "//go:generate"), " \t")
	fields := strings.Fields(commandText)
	if len(fields) < 2 {
		return "", "", errors.New("directive command is incomplete")
	}
	for _, field := range fields {
		for _, r := range field {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("_./=+-", r)) {
				return "", "", errors.New("directive contains shell syntax or unsupported characters")
			}
		}
	}
	command := fields[0]
	if strings.HasPrefix(command, "/") || strings.Contains(command, "..") || path.Clean(command) != command {
		return "", "", errors.New("generator command path is not a safe literal")
	}
	output := ""
	for index := 1; index < len(fields); index++ {
		field := fields[index]
		if field == "-file" {
			if output != "" || index+1 >= len(fields) {
				return "", "", errors.New("directive has ambiguous output flag")
			}
			index++
			output = fields[index]
		} else if strings.HasPrefix(field, "-file=") {
			if output != "" || len(field) == len("-file=") {
				return "", "", errors.New("directive has ambiguous output flag")
			}
			output = strings.TrimPrefix(field, "-file=")
		}
	}
	if output == "" || safepath.Relative(output) != nil || filepath.Ext(output) != ".go" {
		return "", "", errors.New("directive output path is missing or unsafe")
	}
	directory := path.Dir(sourcePath)
	generated := output
	if directory != "." {
		generated = path.Join(directory, output)
	}
	if safepath.Relative(generated) != nil || generated == sourcePath {
		return "", "", errors.New("directive output escapes or replaces its source")
	}
	return command, generated, nil
}

func generatedMarker(name string, content []byte) (bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, content, parser.ParseComments|parser.AllErrors)
	if err != nil || file == nil {
		return false, errors.New("generated Go output is not parseable")
	}
	markers := make([]string, 0)
	for _, group := range file.Comments {
		for _, comment := range group.List {
			markers = append(markers, comment.Text)
		}
	}
	return hasGeneratedCodeMarker(markers), nil
}

type goGenerationToolMatch struct {
	paths     []string
	complete  bool
	buildPath string
	directory string
}

func matchGoGenerationToolDetails(command, generatorPath string, makefiles map[string][]byte) goGenerationToolMatch {
	name := path.Base(command)
	if name == "" || name == "." || name == "/" {
		return goGenerationToolMatch{}
	}
	makefilePaths := make([]string, 0, len(makefiles))
	for name := range makefiles {
		makefilePaths = append(makefilePaths, name)
	}
	sort.Strings(makefilePaths)
	matches := []goGenerationToolMatch{}
	for _, makefile := range makefilePaths {
		if makefile != "Makefile" {
			continue
		}
		text := string(makefiles[makefile])
		if !utf8.ValidString(text) {
			continue
		}
		gobin, gobinCount := parseLiteralMakeGOBIN(text)
		if gobinCount != 1 || gobin == "" {
			continue
		}
		commandPath := path.Join(path.Dir(generatorPath), command)
		expectedCommandPath := path.Join(gobin, name)
		if commandPath != expectedCommandPath {
			continue
		}
		variables := []string{}
		assignmentCounts := make(map[string]int)
		matchingAssignments := make(map[string]int)
		for _, line := range strings.Split(text, "\n") {
			trimmed := strings.TrimSpace(line)
			left, value, ok := strings.Cut(trimmed, "=")
			if !ok {
				continue
			}
			variable := strings.TrimSpace(strings.TrimSuffix(left, ":"))
			value = strings.TrimSpace(value)
			if !strings.HasPrefix(variable, "GEN_") || strings.ContainsAny(variable, " \t") {
				continue
			}
			assignmentCounts[variable]++
			if strings.HasPrefix(value, "$(GOBIN)/") && path.Base(strings.TrimPrefix(value, "$(GOBIN)/")) == name && path.Clean(value) == value {
				variables = append(variables, variable)
				matchingAssignments[variable]++
			}
		}
		variables = uniqueStrings(variables)
		if len(variables) != 1 || assignmentCounts[variables[0]] != 1 || matchingAssignments[variables[0]] != 1 {
			continue
		}
		variable := variables[0]
		matchedDirs := []string{}
		ruleCount := 0
		for _, line := range strings.Split(text, "\n") {
			trimmed := strings.TrimSpace(line)
			prefix := "$(" + variable + "): $(wildcard ./"
			if !strings.HasPrefix(trimmed, prefix) || !strings.HasSuffix(trimmed, "/*)") {
				continue
			}
			directory := strings.TrimSuffix(strings.TrimPrefix(trimmed, prefix), "/*)")
			if directory == "" || strings.ContainsAny(directory, "$() *\\\t\r") {
				continue
			}
			directory = path.Join(path.Dir(makefile), directory)
			if directory != "." && safepath.Relative(directory) != nil {
				continue
			}
			matchedDirs = append(matchedDirs, directory)
			ruleCount++
		}
		matchedDirs = uniqueStrings(matchedDirs)
		if len(matchedDirs) != 1 || ruleCount != 1 {
			continue
		}
		directory := matchedDirs[0]
		// The rule proves a tool directory. The committed-tree inventory fills
		// its direct-child source and embedded-template set.
		matches = append(matches, goGenerationToolMatch{complete: true, buildPath: makefile, directory: directory})
	}
	if len(matches) != 1 {
		return goGenerationToolMatch{}
	}
	return matches[0]
}

func parseLiteralMakeGOBIN(makefile string) (string, int) {
	value := ""
	count := 0
	for _, line := range strings.Split(makefile, "\n") {
		trimmed := strings.TrimSpace(line)
		left, right, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		left = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(left), ":"))
		if left != "GOBIN" && left != "export GOBIN ?" && left != "export GOBIN" {
			continue
		}
		count++
		right = strings.TrimSpace(right)
		switch right {
		case "bin", "$(shell pwd)/bin":
			value = "bin"
		default:
			return "", count
		}
	}
	if count != 1 {
		return "", count
	}
	return value, count
}

func validGoGenerationToolSources(paths []string, contents map[string][]byte) bool {
	mainWithEmbed := false
	templateCount := 0
	for _, name := range paths {
		content, ok := contents[name]
		if !ok || !utf8.Valid(content) {
			return false
		}
		if filepath.Ext(name) == ".tmpl" {
			templateCount++
			continue
		}
		if filepath.Ext(name) != ".go" {
			return false
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, content, parser.ParseComments|parser.AllErrors)
		if err != nil || file == nil {
			return false
		}
		if file.Name != nil && file.Name.Name == "main" && hasExactEmbedTemplates(file.Comments) {
			mainWithEmbed = true
		}
	}
	return mainWithEmbed && templateCount > 0
}

func hasExactEmbedTemplates(groups []*ast.CommentGroup) bool {
	for _, group := range groups {
		for _, comment := range group.List {
			if strings.TrimSpace(comment.Text) == "//go:embed *.tmpl" {
				return true
			}
		}
	}
	return false
}

func uniqueStrings(items []string) []string {
	sort.Strings(items)
	result := items[:0]
	for _, item := range items {
		if len(result) == 0 || result[len(result)-1] != item {
			result = append(result, item)
		}
	}
	return result
}

func goGenerationSourceRole(name, buildPath string) string {
	if name == buildPath {
		return "build_definition"
	}
	if strings.HasSuffix(name, ".tmpl") {
		return "generator_template"
	}
	return "generator_source"
}

func uniqueSourceEntries(entries []repository.SourceEntry) []repository.SourceEntry {
	byPath := make(map[string]repository.SourceEntry, len(entries))
	for _, entry := range entries {
		byPath[entry.Path] = entry
	}
	result := make([]repository.SourceEntry, 0, len(byPath))
	for _, entry := range byPath {
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}

func orderedUniqueSourceEntries(entries []repository.SourceEntry) []repository.SourceEntry {
	result := make([]repository.SourceEntry, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if !seen[entry.Path] {
			seen[entry.Path] = true
			result = append(result, entry)
		}
	}
	return result
}

func uniqueSourceDigests(items []repository.SourceDigest) []repository.SourceDigest {
	result := items[:0]
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if !seen[item.Path] {
			seen[item.Path] = true
			result = append(result, item)
		}
	}
	return result
}

func uniqueTaskContextFiles(items []taskcontext.File) []taskcontext.File {
	result := items[:0]
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if !seen[item.Path] {
			seen[item.Path] = true
			result = append(result, item)
		}
	}
	return result
}
