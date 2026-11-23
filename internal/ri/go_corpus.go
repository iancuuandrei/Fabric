package ri

import (
	"bytes"
	"context"
	"encoding/json"
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
	"unicode"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

const (
	goCorpusMaxFiles       = 24
	goCorpusDiscoveryFiles = 48
	goCorpusMaxFileBytes   = 1 << 20
	goCorpusMaxTotalBytes  = 8 << 20
	goCorpusMaxGraphBytes  = 512 << 10
	goCorpusMaxOmissions   = 64
	goCorpusSourceLocalV1  = "source_local_v1"
	goCorpusNoEligibleCode = "no_eligible_complete_go_source"
)

// GoCorpusSelectionReceiverAwareV2 opts into a bounded discovery pass before
// selecting the final graph corpus. Zero preserves the original path-only
// selection recipe exactly.
const GoCorpusSelectionReceiverAwareV2 = 2

// GoCommittedCorpus is an immutable, bounded source-local Go corpus. Counts
// and omissions preserve incomplete coverage; successful parsing does not
// establish Go type resolution or semantic completeness.
type GoCommittedCorpus struct {
	Source         Source                    `json:"source"`
	Sources        []repository.SourceDigest `json:"sources"`
	GraphInputs    []GoGraphFileInput        `json:"graph_inputs"`
	ContextFiles   []taskcontext.File        `json:"context_files,omitempty"`
	Generators     []GoGeneratorBinding      `json:"generators"`
	InventoryFiles int                       `json:"inventory_files"`
	AttemptedFiles int                       `json:"attempted_files"`
	// ReadFiles counts complete files retained after parser-request and graph
	// admission bounds, not merely files whose committed bytes were observed.
	ReadFiles        int                    `json:"read_files"`
	OmittedCount     int                    `json:"omitted_count"`
	Omissions        []taskcontext.Omission `json:"omissions"`
	OmissionsTrimmed bool                   `json:"omissions_trimmed"`
	Unavailable      string                 `json:"unavailable,omitempty"`
	ModuleInventory  *GoModuleInventory     `json:"module_inventory,omitempty"`
}

// GoCorpusOptions opts into committed module declarations and parser-cache
// reuse. Disabled cache behavior preserves the existing collection recipe.
type GoCorpusOptions struct {
	ModuleInventory *GoModuleInventory
	// EnableParseCache permits reuse of validated GoFileFacts entries under cacheDir.
	EnableParseCache bool
	// SelectionVersion selects a bounded corpus-ranking recipe. Zero retains
	// the original path-only recipe; v2 uses exact declaration evidence from
	// the already admitted discovery bytes.
	SelectionVersion int
}

type goCorpusCandidate struct {
	entry repository.SourceEntry
	score int
}

type goCorpusCommittedFile struct {
	entry       repository.SourceEntry
	digest      repository.SourceDigest
	content     []byte
	packageInfo GoPackageBinding
	facts       GoFileFacts
	declRank    goCorpusDeclarationRank
}

type goCorpusBuffer struct {
	bytes.Buffer
	limit int64
}

// Write rejects any bytes beyond the exact object size admitted by the Git
// batch header.
func (b *goCorpusBuffer) Write(value []byte) (int, error) {
	if int64(len(value)) > b.limit-int64(b.Len()) {
		return 0, errors.New("committed Go source exceeds admitted corpus bound")
	}
	return b.Buffer.Write(value)
}

type goCorpusSink struct {
	buffer *goCorpusBuffer
}

// Write retains only bytes for a selected blob whose complete size was admitted.
func (s *goCorpusSink) Write(value []byte) (int, error) {
	return s.buffer.Write(value)
}

// Close satisfies io.WriteCloser without taking ownership of the shared buffer.
func (s *goCorpusSink) Close() error { return nil }

// CollectCommittedGoCorpus inventories the exact Git tree, chooses a bounded
// deterministic Go corpus, copies only selected objects through one cat-file
// batch process, and parses their facts through one pinned RI stream using the
// legacy no-cache options. It rejects a nonempty cacheDir. Call
// CollectCommittedGoCorpusWithOptions with EnableParseCache to opt in.
func CollectCommittedGoCorpus(ctx context.Context, identity repository.Identity, client Client, cacheDir, objective string) (GoCommittedCorpus, error) {
	return CollectCommittedGoCorpusWithOptions(ctx, identity, client, cacheDir, objective, GoCorpusOptions{})
}

// CollectCommittedGoCorpusWithOptions admits an explicit source-bound module
// inventory for declared package ownership. It never establishes active Go
// build resolution, and malformed or omitted manifest coverage stays partial.
func CollectCommittedGoCorpusWithOptions(ctx context.Context, identity repository.Identity, client Client, cacheDir, objective string, options GoCorpusOptions) (GoCommittedCorpus, error) {
	corpus := GoCommittedCorpus{Sources: []repository.SourceDigest{}, GraphInputs: []GoGraphFileInput{}, Generators: []GoGeneratorBinding{}, Omissions: []taskcontext.Omission{}}
	if options.ModuleInventory != nil {
		if err := ValidateGoModuleInventory(*options.ModuleInventory, identity); err != nil {
			return corpus, err
		}
		corpus.ModuleInventory = options.ModuleInventory
	}
	if options.SelectionVersion != 0 && options.SelectionVersion != GoCorpusSelectionReceiverAwareV2 {
		return corpus, errors.New("invalid Go corpus selection version")
	}
	if options.EnableParseCache {
		if cacheDir == "" {
			return corpus, errors.New("Go corpus parse-cache opt-in requires a cache directory")
		}
		volumeRoot := filepath.VolumeName(cacheDir) + string(filepath.Separator)
		if !filepath.IsAbs(cacheDir) || filepath.Clean(cacheDir) != cacheDir || filepath.Clean(cacheDir) == volumeRoot {
			return corpus, errors.New("Go corpus parse-cache directory must be absolute, clean, and non-root")
		}
	} else if cacheDir != "" {
		return corpus, errors.New("Go corpus parse-cache directory requires explicit EnableParseCache opt-in")
	}
	if len(objective) == 0 || len(objective) > 16<<10 || !utf8.ValidString(objective) {
		return corpus, errors.New("Go corpus objective is empty, oversized, or invalid UTF-8")
	}
	source, err := FromRepository(identity)
	if err != nil {
		return corpus, err
	}
	corpus.Source = source
	stream, err := client.OpenStream(ctx)
	if err != nil {
		return corpus, err
	}
	defer stream.Close()

	terms := goCorpusTerms(objective)
	candidates := make([]goCorpusCandidate, 0, 128)
	appendOmission := func(path, reason string) {
		corpus.OmittedCount++
		if len(corpus.Omissions) >= goCorpusMaxOmissions {
			corpus.OmissionsTrimmed = true
			return
		}
		if path != "" && (!taskcontext.EligiblePath(path) || safepath.Relative(path) != nil) {
			path = "[redacted]"
		}
		corpus.Omissions = append(corpus.Omissions, taskcontext.Omission{Path: path, Reason: reason})
	}
	err = repository.VisitSource(ctx, identity, func(entry repository.SourceEntry) error {
		if filepath.Ext(entry.Path) != ".go" {
			return nil
		}
		corpus.InventoryFiles++
		if entry.Kind != "file" {
			appendOmission(entry.Path, "unsupported_source_kind")
			return nil
		}
		if safepath.Relative(entry.Path) != nil {
			appendOmission(entry.Path, "unsafe_path")
			return nil
		}
		if !taskcontext.EligiblePath(entry.Path) {
			appendOmission(entry.Path, "sensitive_path")
			return nil
		}
		if err := safepath.Writable(entry.Path); err != nil {
			appendOmission(entry.Path, "protected_path")
			return nil
		}
		candidates = append(candidates, goCorpusCandidate{entry: entry, score: goCorpusPathScore(entry.Path, terms)})
		return nil
	})
	if err != nil {
		return GoCommittedCorpus{}, err
	}
	selected := selectGoCorpusCandidates(candidates)
	if options.SelectionVersion == GoCorpusSelectionReceiverAwareV2 {
		selected = selectGoCorpusCandidatesLimit(candidates, goCorpusDiscoveryFiles)
	}
	corpus.AttemptedFiles = len(selected)
	selectedSet := make(map[string]bool, len(selected))
	for _, candidate := range selected {
		selectedSet[candidate.entry.Path] = true
	}
	for _, candidate := range candidates {
		if !selectedSet[candidate.entry.Path] {
			appendOmission(candidate.entry.Path, "file_budget")
		}
	}
	if len(selected) == 0 {
		corpus.Unavailable = goCorpusNoEligibleCode
		return corpus, nil
	}

	buffers := make(map[string]*goCorpusBuffer, len(selected))
	discarded := make(map[string]string, len(selected))
	usedBytes := int64(0)
	graphInputs := make([]GoGraphFileInput, 0, len(selected))
	sources := make([]repository.SourceDigest, 0, len(selected))
	readContextFiles := make([]taskcontext.File, 0, len(selected))
	committed := make([]goCorpusCommittedFile, 0, len(selected))
	err = repository.CopySelectedSourceBatch(ctx, identity, goCorpusEntries(selected), func(entry repository.SourceEntry, size int64) (io.WriteCloser, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if size > goCorpusMaxFileBytes {
			discarded[entry.Path] = "file_byte_limit"
			return nil, nil
		}
		if size > int64(goCorpusMaxTotalBytes)-usedBytes {
			discarded[entry.Path] = "corpus_byte_limit"
			return nil, nil
		}
		buffer := &goCorpusBuffer{limit: size}
		buffers[entry.Path] = buffer
		usedBytes += size
		return &goCorpusSink{buffer: buffer}, nil
	}, func(entry repository.SourceEntry, digest *repository.SourceDigest) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if reason := discarded[entry.Path]; reason != "" {
			appendOmission(entry.Path, reason)
			return nil
		}
		buffer := buffers[entry.Path]
		if buffer == nil || digest == nil || digest.RepositoryID != source.RepositoryID || digest.Commit != source.Commit || digest.Path != entry.Path || digest.Blob != entry.Object || digest.Bytes != int64(buffer.Len()) {
			return fmt.Errorf("selected committed Go source binding mismatch: %q", entry.Path)
		}
		content := buffer.Bytes()
		delete(buffers, entry.Path)
		if !utf8.Valid(content) {
			appendOmission(entry.Path, "non_utf8")
			return nil
		}
		fileSet := token.NewFileSet()
		file, parseErr := parser.ParseFile(fileSet, entry.Path, content, parser.PackageClauseOnly)
		if parseErr != nil || file == nil || file.Name == nil || file.Name.Name == "" {
			appendOmission(entry.Path, "invalid_package_clause")
			return nil
		}
		binding, bindingErr := SourceLocalGoPackageBinding(source.RepositoryID, entry.Path, file.Name.Name)
		if options.ModuleInventory != nil {
			binding, bindingErr = DeclaredGoPackageBinding(*options.ModuleInventory, entry.Path, file.Name.Name)
		}
		if bindingErr != nil {
			return bindingErr
		}
		if binding.IdentityKind == goCorpusSourceLocalV1 && strings.HasSuffix(entry.Path, "_test.go") && strings.HasSuffix(file.Name.Name, "_test") {
			basePackageName := strings.TrimSuffix(file.Name.Name, "_test")
			baseBinding, err := SourceLocalGoPackageBinding(source.RepositoryID, entry.Path, basePackageName)
			if err != nil {
				return err
			}
			binding.TestOfPackageIdentity = baseBinding.PackageIdentity
		}
		committed = append(committed, goCorpusCommittedFile{entry: entry, digest: *digest, content: content, packageInfo: binding})
		return nil
	})
	if err != nil {
		return GoCommittedCorpus{}, err
	}
	selectedRank := make(map[string]int, len(selected))
	for index, candidate := range selected {
		selectedRank[candidate.entry.Path] = index
	}
	sort.Slice(committed, func(i, j int) bool {
		return selectedRank[committed[i].entry.Path] < selectedRank[committed[j].entry.Path]
	})
	parsed := make([]goCorpusCommittedFile, 0, len(committed))
	receiverTargets := []goCorpusReceiverTarget(nil)
	if options.SelectionVersion == GoCorpusSelectionReceiverAwareV2 {
		receiverTargets = goCorpusReceiverTargets(objective)
	}
	for index := range committed {
		sourceFile := &committed[index]
		if err := ctx.Err(); err != nil {
			return GoCommittedCorpus{}, err
		}
		request, _, requestErr := goFileFactsRequest(sourceFile.entry.Path, sourceFile.content, cacheDir, stream.producerHash)
		if requestErr != nil {
			return GoCommittedCorpus{}, fmt.Errorf("committed Go facts request failed for %q: %w", sourceFile.entry.Path, requestErr)
		}
		if _, requestErr = canonical.Bytes(request); requestErr != nil {
			appendOmission(sourceFile.entry.Path, "facts_request_byte_limit")
			continue
		}
		facts, factsErr := stream.GoFileFacts(ctx, sourceFile.entry.Path, sourceFile.content, cacheDir)
		if factsErr != nil {
			return GoCommittedCorpus{}, fmt.Errorf("committed Go facts failed for %q: %w", sourceFile.entry.Path, factsErr)
		}
		sourceFile.facts = facts
		if options.SelectionVersion == GoCorpusSelectionReceiverAwareV2 {
			sourceFile.declRank = goCorpusReceiverAwareRank(sourceFile.content, receiverTargets)
		}
		parsed = append(parsed, *sourceFile)
	}
	if options.SelectionVersion == GoCorpusSelectionReceiverAwareV2 {
		sort.SliceStable(parsed, func(i, j int) bool {
			if parsed[i].declRank != parsed[j].declRank {
				return parsed[i].declRank.betterThan(parsed[j].declRank)
			}
			return selectedRank[parsed[i].entry.Path] < selectedRank[parsed[j].entry.Path]
		})
	}
	for _, sourceFile := range parsed {
		if corpus.ReadFiles >= goCorpusMaxFiles {
			appendOmission(sourceFile.entry.Path, "declaration_file_budget")
			continue
		}
		input := GoGraphFileInput{Facts: sourceFile.facts, Source: sourceFile.content, Package: sourceFile.packageInfo}
		trial := make([]GoGraphFileInput, 0, len(graphInputs)+1)
		trial = append(trial, graphInputs...)
		trial = append(trial, input)
		fits, fitErr := goCorpusGraphFitsWithModules(source.RepositoryID, stream.producerHash, trial, options.ModuleInventory)
		if fitErr != nil {
			return GoCommittedCorpus{}, fitErr
		}
		if !fits {
			appendOmission(sourceFile.entry.Path, "graph_byte_budget")
			continue
		}
		graphInputs = append(graphInputs, input)
		sources = append(sources, sourceFile.digest)
		readContextFiles = append(readContextFiles, taskcontext.File{Path: sourceFile.entry.Path, Hash: sourceFile.digest.SHA256, Content: sourceFile.content})
		corpus.ReadFiles++
	}
	corpus.Sources = sources
	corpus.GraphInputs = graphInputs
	corpus.ContextFiles = readContextFiles
	if corpus.ReadFiles == 0 {
		corpus.Unavailable = goCorpusNoEligibleCode
	}
	return corpus, nil
}

func goCorpusGraphFits(sourceID, producer string, files []GoGraphFileInput) (bool, error) {
	return goCorpusGraphFitsWithModules(sourceID, producer, files, nil)
}

func goCorpusGraphFitsWithModules(sourceID, producer string, files []GoGraphFileInput, inventory *GoModuleInventory) (bool, error) {
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: sourceID, ProducerSHA256: producer, Files: files, Generators: []GoGeneratorBinding{}, ModuleInventory: inventory})
	if err != nil {
		if err.Error() == "JSON size or UTF-8 invalid" || strings.Contains(err.Error(), "exceeds") {
			return false, nil
		}
		return false, fmt.Errorf("committed Go graph admission failed: %w", err)
	}
	canonicalSize, err := goCorpusCanonicalJSONSize(graph)
	if err != nil {
		return false, err
	}
	return canonicalSize <= goCorpusMaxGraphBytes, nil
}

// goCorpusCanonicalJSONSize returns the byte length of canonical JSON without
// decoding and re-encoding the already validated typed graph. encoding/json
// escapes U+2028 and U+2029 for JavaScript compatibility; canonical v1 emits
// those UTF-8 runes directly, so each such escape reduces the canonical length
// by three bytes. HTML escaping is disabled because canonical v1 keeps <, >, &.
func goCorpusCanonicalJSONSize(value any) (int, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return 0, err
	}
	raw := encoded.Bytes()
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return 0, errors.New("JSON encoder did not terminate the value")
	}
	size := len(raw) - 1 // Encoder.Encode appends one newline not present in canonical v1.
	for index := 0; index < len(raw)-1; {
		if raw[index] != '"' {
			index++
			continue
		}
		index++
		for index < len(raw)-1 && raw[index] != '"' {
			if raw[index] != '\\' {
				index++
				continue
			}
			if index+1 >= len(raw)-1 {
				return 0, errors.New("JSON encoder emitted an incomplete escape")
			}
			if raw[index+1] == 'u' {
				if index+6 > len(raw)-1 {
					return 0, errors.New("JSON encoder emitted an incomplete unicode escape")
				}
				escape := raw[index+2 : index+6]
				if bytes.Equal(escape, []byte("2028")) || bytes.Equal(escape, []byte("2029")) {
					size -= 3 // six ASCII escape bytes become one three-byte UTF-8 rune.
				}
				index += 6
				continue
			}
			index += 2
		}
		if index >= len(raw)-1 {
			return 0, errors.New("JSON encoder emitted an unterminated string")
		}
		index++
	}
	return size, nil
}

func goCorpusEntries(candidates []goCorpusCandidate) []repository.SourceEntry {
	entries := make([]repository.SourceEntry, len(candidates))
	for i, candidate := range candidates {
		entries[i] = candidate.entry
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}

func goCorpusTerms(objective string) []string {
	terms := strings.FieldsFunc(strings.ToLower(objective), func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_')
	})
	sort.Strings(terms)
	result := terms[:0]
	for _, term := range terms {
		if len(term) > 1 && (len(result) == 0 || result[len(result)-1] != term) {
			result = append(result, term)
		}
	}
	return result
}

func goCorpusPathScore(filePath string, terms []string) int {
	folded := strings.ToLower(filePath)
	score := 0
	for _, term := range terms {
		if strings.Contains(folded, term) {
			score++
		}
		if (term == "generate" || term == "generated" || term == "generator") && (strings.Contains(folded, "gen") || strings.Contains(folded, "template")) {
			score++
		}
	}
	return score
}

func selectGoCorpusCandidates(candidates []goCorpusCandidate) []goCorpusCandidate {
	return selectGoCorpusCandidatesLimit(candidates, goCorpusMaxFiles)
}

func selectGoCorpusCandidatesLimit(candidates []goCorpusCandidate, limit int) []goCorpusCandidate {
	if limit < 1 {
		return nil
	}
	ordered := append([]goCorpusCandidate(nil), candidates...)
	better := func(left, right goCorpusCandidate) bool {
		if left.score != right.score {
			return left.score > right.score
		}
		return left.entry.Path < right.entry.Path
	}
	sort.Slice(ordered, func(i, j int) bool { return better(ordered[i], ordered[j]) })
	if len(ordered) <= limit {
		return ordered
	}
	seedCount := 16
	if len(ordered) < seedCount {
		seedCount = len(ordered)
	}
	seeds := append([]goCorpusCandidate(nil), ordered[:seedCount]...)
	selected := append([]goCorpusCandidate(nil), seeds...)
	seen := make(map[string]bool, limit)
	for _, seed := range seeds {
		seen[seed.entry.Path] = true
	}
	directories := make(map[string]bool)
	for _, seed := range seeds {
		directories[path.Dir(seed.entry.Path)] = true
	}
	related := make([]goCorpusCandidate, 0)
	for _, candidate := range ordered[seedCount:] {
		if !directories[path.Dir(candidate.entry.Path)] {
			continue
		}
		related = append(related, candidate)
	}
	sort.Slice(related, func(i, j int) bool {
		iTest := strings.HasSuffix(related[i].entry.Path, "_test.go")
		jTest := strings.HasSuffix(related[j].entry.Path, "_test.go")
		if iTest != jTest {
			return iTest
		}
		return better(related[i], related[j])
	})
	for _, candidate := range related {
		if len(selected) == limit {
			break
		}
		if seen[candidate.entry.Path] {
			continue
		}
		seen[candidate.entry.Path] = true
		selected = append(selected, candidate)
	}
	for _, candidate := range ordered[seedCount:] {
		if len(selected) == limit {
			break
		}
		if seen[candidate.entry.Path] {
			continue
		}
		seen[candidate.entry.Path] = true
		selected = append(selected, candidate)
	}
	return selected
}

type goCorpusReceiverTarget struct {
	receiver string
	method   string
}

// goCorpusReceiverTargets retains only dotted Go identifier pairs from the
// objective. They are ranking hints, not symbol resolution or a claim that a
// matching declaration implements the requested behavior.
func goCorpusReceiverTargets(objective string) []goCorpusReceiverTarget {
	parts := strings.FieldsFunc(objective, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.')
	})
	seen := make(map[goCorpusReceiverTarget]bool)
	targets := []goCorpusReceiverTarget{}
	for _, part := range parts {
		segments := strings.Split(part, ".")
		if len(segments) != 2 || !token.IsIdentifier(segments[0]) || !token.IsIdentifier(segments[1]) {
			continue
		}
		target := goCorpusReceiverTarget{receiver: segments[0], method: segments[1]}
		if !seen[target] {
			seen[target] = true
			targets = append(targets, target)
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].receiver != targets[j].receiver {
			return targets[i].receiver < targets[j].receiver
		}
		return targets[i].method < targets[j].method
	})
	return targets
}

type goCorpusDeclarationRank struct {
	exact         bool
	otherMethods  int
	receiverTypes int
	freeFunctions int
}

// betterThan orders exact receiver/method matches before every nonexact
// declaration count, then uses bounded syntactic counts as deterministic
// within-category detail. It never resolves calls.
func (left goCorpusDeclarationRank) betterThan(right goCorpusDeclarationRank) bool {
	if left.exact != right.exact {
		return left.exact
	}
	if left.otherMethods != right.otherMethods {
		return left.otherMethods > right.otherMethods
	}
	if left.receiverTypes != right.receiverTypes {
		return left.receiverTypes > right.receiverTypes
	}
	return left.freeFunctions > right.freeFunctions
}

// goCorpusReceiverAwareRank uses only exact Go declarations from already
// source-bound discovery bytes. It deliberately gives a matching receiver and
// method priority over examples or calls that merely spell the method name.
func goCorpusReceiverAwareRank(source []byte, targets []goCorpusReceiverTarget) goCorpusDeclarationRank {
	if len(targets) == 0 {
		return goCorpusDeclarationRank{}
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), "objective.go", source, parser.AllErrors)
	if err != nil || parsed == nil {
		return goCorpusDeclarationRank{}
	}
	rank := goCorpusDeclarationRank{}
	for _, decl := range parsed.Decls {
		switch value := decl.(type) {
		case *ast.FuncDecl:
			if value.Name == nil {
				continue
			}
			receiver := goCorpusReceiverName(value.Recv)
			for _, target := range targets {
				switch {
				case value.Name.Name == target.method && receiver == target.receiver:
					rank.exact = true
				case value.Name.Name == target.method && receiver != "":
					rank.otherMethods = min(rank.otherMethods+1, 64)
				case value.Name.Name == target.method:
					rank.freeFunctions = min(rank.freeFunctions+1, 64)
				}
			}
		case *ast.GenDecl:
			for _, spec := range value.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				for _, target := range targets {
					if typeSpec.Name.Name == target.receiver {
						rank.receiverTypes = min(rank.receiverTypes+1, 64)
					}
				}
			}
		}
	}
	return rank
}

func goCorpusReceiverName(receivers *ast.FieldList) string {
	if receivers == nil || len(receivers.List) != 1 {
		return ""
	}
	typeExpr := receivers.List[0].Type
	for {
		switch value := typeExpr.(type) {
		case *ast.StarExpr:
			typeExpr = value.X
		case *ast.IndexExpr:
			typeExpr = value.X
		case *ast.IndexListExpr:
			typeExpr = value.X
		case *ast.ParenExpr:
			typeExpr = value.X
		default:
			name, _ := typeExpr.(*ast.Ident)
			if name == nil {
				return ""
			}
			return name.Name
		}
	}
}
