package ri

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

type goContextAnchor struct {
	Path   string
	Pivot  int
	Depth  int
	Reason string
}

// GoContextInput compiles a bounded prompt view from one graph and its exact
// source observations. It performs no filesystem reads or authorization.
// PPR, when non-nil, opts the v2 compiler into bounded Personalized PageRank
// path hints over the already admitted graph (see engineering_ppr.go). Nil
// preserves exact historical v1/v2 selection wire and manifest hashes.
type GoContextInput struct {
	ContextVersion int
	SourceID       string
	Graph          GoEngineeringGraph
	Objective      string
	Files          []taskcontext.File
	ChangedPaths   []string
	Limits         taskcontext.Limits
	PPR            *GoPPRRequest
}

// GoPPRRequest is the explicit opt-in to the single fixed PPR treatment. It
// carries no tunable fields: parameters are the documented constants, seeds
// derive from the bound objective and changed paths, and ranks are advisory
// only. Non-nil versus nil is the entire treatment identity at this layer;
// immutable policy binding lives in the controller record.
type GoPPRRequest struct{}

// GoContextManifest retains source excerpts and the partial graph observations
// behind their selection. Missing evidence never implies absence or safety.
type GoContextManifest struct {
	Schema           string                     `json:"schema"`
	GraphDigest      string                     `json:"graph_digest"`
	ProducerSHA256   string                     `json:"producer_sha256"`
	Coverage         string                     `json:"coverage"`
	QuerySHA256      string                     `json:"query_sha256"`
	Selection        taskcontext.Manifest       `json:"selection"`
	ContractExcerpts []taskcontext.SelectedFile `json:"contract_excerpts,omitempty"`
	Symbols          []GoGraphNode              `json:"symbols"`
	Relations        []GoGraphEdge              `json:"relations"`
	FactsTruncated   bool                       `json:"facts_truncated"`
	HintsTruncated   bool                       `json:"hints_truncated"`
	// PPR retains the independent PPR rank provenance when the opt-in
	// treatment compiled this manifest. Nil preserves exact historical
	// manifest bytes and digest. Ranks are advisory file ordering only;
	// excerpt bytes, symbol/span visibility and all bounds are unchanged.
	PPR    *GoPPRProvenance `json:"ppr,omitempty"`
	Digest string           `json:"digest"`
}

const (
	goContextV1                 = 1
	goContextV2                 = 2
	goContextV2MaxPromptBytes   = 48 << 10
	goContextV2MaxBaseFiles     = 12
	goContextV2MaxExtraExcerpts = 32
	goContextV2MaxParseFiles    = 512
	goContextV2MaxParseBytes    = 8 << 20
	goContextV2MaxCallerDepth   = 3
)

// CompileGoContext combines exact path/symbol seeds and bounded dependency
// hints with the existing lexical selector. Returned facts fit selected source
// spans; they are observations, not proof of semantic resolution or write scope.
func CompileGoContext(input GoContextInput) (GoContextManifest, error) {
	var empty GoContextManifest
	if input.ContextVersion != 0 && input.ContextVersion != goContextV1 && input.ContextVersion != goContextV2 {
		return empty, errors.New("unsupported Go context version")
	}
	if err := ValidateGoEngineeringGraph(input.Graph); err != nil {
		return empty, err
	}
	if safepath.RequireDigest(input.SourceID) != nil || input.SourceID != input.Graph.SourceID || len(input.Files) != len(input.Graph.Files) {
		return empty, errors.New("Go context requires the complete bound graph source corpus")
	}
	expected := make(map[string]string, len(input.Graph.Files))
	for _, file := range input.Graph.Files {
		expected[file.Facts.Path] = file.Facts.SourceSHA256
	}
	seen := make(map[string]bool, len(input.Files))
	for _, file := range input.Files {
		hash := sha256.Sum256(file.Content)
		if seen[file.Path] || expected[file.Path] == "" || expected[file.Path] != file.Hash || file.Hash != hex.EncodeToString(hash[:]) {
			return empty, errors.New("Go context source bytes differ from graph")
		}
		seen[file.Path] = true
	}
	// Full identifiers rather than arbitrary substrings seed graph hints.
	terms := make(map[string]bool)
	for _, word := range strings.FieldsFunc(input.Objective, func(r rune) bool {
		return !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r))
	}) {
		terms[strings.ToLower(word)] = true
	}
	hints := make(map[string]bool)
	anchors := make(map[string]int)
	for _, node := range input.Graph.Nodes {
		if node.Kind == "declaration" && terms[strings.ToLower(node.Label)] && taskcontext.EligiblePath(node.Path) {
			hints[node.Path] = true
			if node.Range != nil {
				if prior, ok := anchors[node.Path]; !ok || node.Range.StartByte < prior {
					anchors[node.Path] = node.Range.StartByte
				}
			}
		}
	}
	for _, changed := range input.ChangedPaths {
		if !seen[changed] {
			return empty, errors.New("Go context changed path absent from bound corpus")
		}
	}
	seeds := append([]string(nil), input.ChangedPaths...)
	for path := range hints {
		seeds = append(seeds, path)
	}
	sort.Strings(seeds)
	seeds = compactContextPaths(seeds)
	hintsTruncated := len(seeds) > goEngineeringMaxSeeds
	if len(seeds) > goEngineeringMaxSeeds {
		seeds = seeds[:goEngineeringMaxSeeds]
	}
	if len(seeds) > 0 {
		impact, err := QueryGoImpact(input.Graph, GoImpactQuery{Paths: seeds, MaxDepth: 2, Limit: goEngineeringMaxSeeds})
		if err != nil {
			return empty, err
		}
		hintsTruncated = hintsTruncated || impact.Truncated
		for _, path := range impact.Paths {
			if taskcontext.EligiblePath(path) {
				hints[path] = true
			}
		}
	}
	orderedHints := make([]string, 0, len(hints))
	for path := range hints {
		orderedHints = append(orderedHints, path)
	}
	sort.Strings(orderedHints)
	if len(orderedHints) > 512 {
		orderedHints = orderedHints[:512]
		hintsTruncated = true
	}
	// Opt-in PPR treatment (planner go-source-context-v2 seam only): derive
	// exact observed seeds and diffuse the fixed lazy undirected walk over
	// the already admitted graph. Only positive-score ranks become an
	// explicit advisory ordering for the SAME bounded excerpt selector, file
	// and byte limits and symbol/span narrowing; the ordering breaks score
	// ties and never promotes zero-signal files to evidence. Scores are not
	// weights and add no hint mass: legacy scores decide admission, the
	// independent rank order decides ties, and full rank provenance below
	// stays the audit trail. No-signal or work-exhausted outcomes keep the
	// current selector with explicit provenance instead of aborting.
	var pprProvenance *GoPPRProvenance
	var pprOrder []string
	if input.PPR != nil {
		if input.ContextVersion != goContextV2 {
			return empty, errors.New("Go PPR treatment requires context version 2")
		}
		pprSeeds, err := DeriveGoPPRSeeds(input.Graph, input.Objective, input.ChangedPaths)
		if err != nil {
			return empty, err
		}
		provenance, err := ComputeGoPPR(input.Graph, pprSeeds, input.Objective, DefaultGoPPRConfig())
		if err != nil {
			return empty, err
		}
		pprProvenance = &provenance
		hintsTruncated = hintsTruncated || pprSeeds.Truncated
		if !provenance.NoSignal {
			for _, rank := range provenance.Ranks {
				if rank.Score <= 0 {
					continue
				}
				pprOrder = append(pprOrder, rank.Path)
			}
		}
	}
	anchorPaths := make([]string, 0, len(anchors))
	for path := range anchors {
		anchorPaths = append(anchorPaths, path)
	}
	sort.Strings(anchorPaths)
	if len(anchorPaths) > goEngineeringMaxSeeds {
		hintsTruncated = true
		for _, path := range anchorPaths[goEngineeringMaxSeeds:] {
			delete(anchors, path)
		}
	}
	contextVersion := input.ContextVersion
	if contextVersion == 0 {
		contextVersion = goContextV1
	}
	var contractAnchors []goContextAnchor
	anchorFactsTruncated := false
	if contextVersion == goContextV2 {
		contractAnchors, anchorFactsTruncated = graphContextAnchors(input)
	}
	selectionLimits := input.Limits
	if contextVersion == goContextV2 {
		total := min(selectionLimits.MaxBytes, goContextV2MaxPromptBytes)
		base := total
		if len(contractAnchors) > 0 {
			base = total / 2
		}
		if base < 256 {
			base = 256
		}
		selectionLimits.MaxBytes = base
		selectionLimits.MaxFiles = min(selectionLimits.MaxFiles, goContextV2MaxBaseFiles)
		selectionLimits.MaxBytesPerFile = min(selectionLimits.MaxBytesPerFile, base)
	}
	selection, err := taskcontext.Select(taskcontext.Input{Version: 1, Scope: taskcontext.Scope{SourceID: input.SourceID, CandidateID: input.Graph.CandidateID}, Objective: input.Objective, Files: input.Files, ChangedPaths: input.ChangedPaths, PathHints: orderedHints, Anchors: anchors, PPR: pprOrder, Limits: selectionLimits})
	if err != nil {
		return empty, err
	}
	queryHash := sha256.Sum256([]byte(input.Objective))
	manifestSchema := "engorch.ri.go-context.v1"
	manifestDomain := "harness.ri.go-context.v1"
	manifest := GoContextManifest{Schema: manifestSchema, GraphDigest: input.Graph.Digest, ProducerSHA256: input.Graph.ProducerSHA256, Coverage: "PARTIAL", QuerySHA256: hex.EncodeToString(queryHash[:]), Selection: selection, ContractExcerpts: []taskcontext.SelectedFile{}, Symbols: []GoGraphNode{}, Relations: []GoGraphEdge{}, HintsTruncated: hintsTruncated, PPR: pprProvenance}
	if contextVersion == goContextV2 {
		manifest.Schema = "engorch.ri.go-context.v2"
		manifestDomain = "harness.ri.go-context.v2"
		manifest.FactsTruncated = anchorFactsTruncated
		remaining := min(input.Limits.MaxBytes, goContextV2MaxPromptBytes) - selection.SelectedBytes
		if remaining < 128 {
			manifest.FactsTruncated = len(contractAnchors) > 0
		} else {
			filesByPath := make(map[string]taskcontext.File, len(input.Files))
			for _, file := range input.Files {
				filesByPath[file.Path] = file
			}
			for _, anchor := range contractAnchors {
				if len(manifest.ContractExcerpts) >= goContextV2MaxExtraExcerpts || remaining < 128 {
					manifest.FactsTruncated = true
					break
				}
				file, ok := filesByPath[anchor.Path]
				if !ok {
					manifest.FactsTruncated = true
					continue
				}
				one := remaining
				perFile := min(input.Limits.MaxBytesPerFile, one)
				if perFile < 128 {
					manifest.FactsTruncated = true
					break
				}
				selectedOne, selectErr := taskcontext.Select(taskcontext.Input{Version: 1, Scope: taskcontext.Scope{SourceID: input.SourceID, CandidateID: input.Graph.CandidateID}, Objective: input.Objective, Files: []taskcontext.File{file}, PathHints: []string{anchor.Path}, Anchors: map[string]int{anchor.Path: anchor.Pivot}, Limits: taskcontext.Limits{MaxFiles: 1, MaxBytes: one, MaxBytesPerFile: perFile, MaxInputBytes: max(input.Limits.MaxInputBytes, one), MaxOmissions: 0}})
				if selectErr != nil || len(selectedOne.Selected) != 1 {
					manifest.FactsTruncated = true
					continue
				}
				extra := selectedOne.Selected[0]
				extra.Reason = anchor.Reason
				if contextPointCovered(manifest.Selection.Selected, manifest.ContractExcerpts, anchor.Path, anchor.Pivot) {
					continue
				}
				if contextExcerptOverlaps(manifest.Selection.Selected, manifest.ContractExcerpts, extra) {
					manifest.FactsTruncated = true
					continue
				}
				manifest.ContractExcerpts = append(manifest.ContractExcerpts, extra)
				remaining -= int(extra.End - extra.Start)
			}
		}
	}
	selected := make(map[string][]taskcontext.SelectedFile, len(selection.Selected)+len(manifest.ContractExcerpts))
	for _, file := range selection.Selected {
		selected[file.Path] = append(selected[file.Path], file)
	}
	for _, file := range manifest.ContractExcerpts {
		selected[file.Path] = append(selected[file.Path], file)
	}
	visible := func(path string, span *GoRange) bool {
		for _, file := range selected[path] {
			if span == nil || int64(span.StartByte) >= file.Start && int64(span.EndByte) <= file.End {
				return true
			}
		}
		return false
	}
	// Hard count ceilings bound structural metadata separately from source bytes.
	for _, node := range input.Graph.Nodes {
		if node.Kind == "declaration" && visible(node.Path, node.Range) {
			if len(manifest.Symbols) == 128 {
				manifest.FactsTruncated = true
				continue
			}
			manifest.Symbols = append(manifest.Symbols, cloneGoGraphNode(node))
		}
	}
	for _, edge := range input.Graph.Edges {
		if contextVersion == goContextV2 && edge.Range == nil {
			continue
		}
		if visible(edge.Path, edge.Range) {
			if len(manifest.Relations) == 128 {
				manifest.FactsTruncated = true
				continue
			}
			manifest.Relations = append(manifest.Relations, cloneGoGraphEdge(edge))
		}
	}
	manifest.Digest, err = canonical.Hash(manifestDomain, manifest)
	if err != nil {
		return empty, err
	}
	encoded, err := canonical.Bytes(manifest)
	if err != nil || len(encoded) > 128<<10 {
		return empty, errors.New("Go context manifest exceeds 128 KiB output budget")
	}
	return manifest, nil
}

func compactContextPaths(paths []string) []string {
	out := paths[:0]
	for _, path := range paths {
		if len(out) == 0 || out[len(out)-1] != path {
			out = append(out, path)
		}
	}
	return out
}

func graphContextAnchors(input GoContextInput) ([]goContextAnchor, bool) {
	terms := make(map[string]bool)
	for _, word := range strings.FieldsFunc(input.Objective, func(r rune) bool { return !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)) }) {
		terms[strings.ToLower(word)] = true
	}
	files := make(map[string][]byte, len(input.Files))
	for _, file := range input.Files {
		files[file.Path] = file.Content
	}
	anchors := make([]goContextAnchor, 0)
	truncated := false
	matchedNames := make(map[string]bool)
	for _, node := range input.Graph.Nodes {
		if node.Path == "" || node.Range == nil || !taskcontext.EligiblePath(node.Path) || !contextLabelMatches(node.Label, terms) {
			continue
		}
		if node.Kind != "declaration" && node.Kind != "call" {
			continue
		}
		anchors = append(anchors, goContextAnchor{Path: node.Path, Pivot: node.Range.StartByte, Depth: 0, Reason: "graph_anchor"})
		if node.Kind == "declaration" {
			matchedNames[node.Label] = true
		}
	}
	if len(matchedNames) == 0 {
		return normalizeContextAnchors(anchors, &truncated), truncated
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	parsed := make([]*ast.File, 0, min(len(paths), goContextV2MaxParseFiles))
	fset := token.NewFileSet()
	parsedBytes := 0
	for _, path := range paths {
		if len(parsed) >= goContextV2MaxParseFiles || parsedBytes+len(files[path]) > goContextV2MaxParseBytes {
			truncated = true
			break
		}
		parsedBytes += len(files[path])
		file, err := parser.ParseFile(fset, path, files[path], parser.AllErrors)
		if err != nil {
			truncated = true
			continue
		}
		parsed = append(parsed, file)
	}
	frontier := matchedNames
	seenOwners := make(map[string]bool)
	for depth := 1; depth <= goContextV2MaxCallerDepth && len(frontier) > 0; depth++ {
		next := make(map[string]bool)
		for _, file := range parsed {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil || fn.Name == nil {
					continue
				}
				owner := fn.Name.Name
				ownerPosition := fset.Position(fn.Pos())
				ownerKey := ownerPosition.Filename + ":" + owner + ":" + strconv.Itoa(ownerPosition.Offset)
				if seenOwners[ownerKey] {
					continue
				}
				matchedCall := -1
				ast.Inspect(fn.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					name := contextCallName(call.Fun)
					if frontier[name] && matchedCall < 0 {
						matchedCall = fset.Position(call.Pos()).Offset
					}
					return true
				})
				if matchedCall < 0 {
					continue
				}
				seenOwners[ownerKey] = true
				anchors = append(anchors, goContextAnchor{Path: ownerPosition.Filename, Pivot: matchedCall, Depth: depth, Reason: "graph_caller"})
				next[owner] = true
			}
		}
		frontier = next
	}
	if len(frontier) > 0 {
		// A nonempty frontier at the configured depth means further syntactic
		// callers may exist; keep the coverage claim explicitly partial.
		truncated = true
	}
	normalized := normalizeContextAnchors(anchors, &truncated)
	return normalized, truncated
}

func contextLabelMatches(label string, terms map[string]bool) bool {
	for _, word := range strings.FieldsFunc(label, func(r rune) bool { return !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)) }) {
		if terms[strings.ToLower(word)] {
			return true
		}
	}
	return false
}

func contextCallName(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return value.Sel.Name
	case *ast.IndexExpr:
		return contextCallName(value.X)
	case *ast.IndexListExpr:
		return contextCallName(value.X)
	case *ast.ParenExpr:
		return contextCallName(value.X)
	default:
		return ""
	}
}

func normalizeContextAnchors(anchors []goContextAnchor, truncated *bool) []goContextAnchor {
	sort.Slice(anchors, func(i, j int) bool {
		if anchors[i].Depth != anchors[j].Depth {
			return anchors[i].Depth < anchors[j].Depth
		}
		if anchors[i].Path != anchors[j].Path {
			return anchors[i].Path < anchors[j].Path
		}
		return anchors[i].Pivot < anchors[j].Pivot
	})
	out := anchors[:0]
	seen := make(map[string]bool)
	for _, anchor := range anchors {
		key := anchor.Path + "\x00" + strconv.Itoa(anchor.Pivot)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, anchor)
	}
	if len(out) > goContextV2MaxExtraExcerpts {
		*truncated = true
		out = out[:goContextV2MaxExtraExcerpts]
	}
	return out
}

func contextExcerptOverlaps(base []taskcontext.SelectedFile, extras []taskcontext.SelectedFile, candidate taskcontext.SelectedFile) bool {
	for _, files := range [][]taskcontext.SelectedFile{base, extras} {
		for _, file := range files {
			if file.Path == candidate.Path && candidate.Start < file.End && file.Start < candidate.End {
				return true
			}
		}
	}
	return false
}

func contextPointCovered(base []taskcontext.SelectedFile, extras []taskcontext.SelectedFile, path string, point int) bool {
	for _, files := range [][]taskcontext.SelectedFile{base, extras} {
		for _, file := range files {
			if file.Path == path && int64(point) >= file.Start && int64(point) < file.End {
				return true
			}
		}
	}
	return false
}
