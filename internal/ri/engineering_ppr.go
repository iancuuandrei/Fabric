package ri

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

// Bounded Personalized PageRank over an already admitted Go engineering graph.
//
// Research provenance (principles only, no donor code):
//   - Brin/Page "The Anatomy of a Large-Scale Hypertextual Web Search Engine"
//     (https://infolab.stanford.edu/~backrub/google.html), section 2.1.1, uses
//     damping d = 0.85. Fabric keeps that fixed diffusion factor as
//     alpha = 17/20 with restart 3/20 and never tunes it per benchmark.
//   - Andersen/Chung/Lang, "Local Graph Partitioning using PageRank Vectors"
//     (FOCS 2006, https://mathweb.ucsd.edu/~fan/wp/localpartition.pdf),
//     section 2, uses a personalized restart vector and a LAZY UNDIRECTED
//     walk. Fabric adopts only that walk shape (stay with probability 1/2,
//     else uniform over undirected neighbors; dangling nodes always stay).
//     Their partitioning/mixing guarantees are NOT imported: Fabric's graph is
//     explicitly PARTIAL with UNRESOLVED calls, so a PPR rank is advisory file
//     ordering only. It cannot acquire evidence, permit writes or effects, or
//     turn missing/PARTIAL/UNRESOLVED observations into absence or resolution.
//
// Iteration: pi_{k+1} = (1-alpha)*s + alpha*P^T*pi_k with alpha = 17/20.
// All arithmetic is fixed-point integer probability at declared scale
// goPPRScale with floor plus deterministic remainder distribution by exact
// path order, so total mass is conserved exactly (sum == scale) every
// iteration. Reported scores are a finite approximation, never claimed exact
// convergence. No dense N*N matrix, no floating-point identity, no stochastic
// walk, no exact rationals, no graph server/vector DB/models/framework.
const (
	goPPRAlphaNum = 17
	goPPRAlphaDen = 20
	// goPPRRestartNum/goPPRRestartDen is the fixed restart mass 3/20.
	goPPRRestartNum = 3
	goPPRRestartDen = 20
	// goPPRScale is the fixed-point probability mass unit. One file score of
	// goPPRScale means probability 1. Integer floor plus deterministic
	// remainder distribution by exact path order conserves total mass exactly
	// (scores sum to scale) every iteration. Stored scores are a finite
	// fixed-point approximation of the lazy-walk distribution at the reported
	// iteration count: no per-score error bound against the exact stationary
	// distribution is claimed, and repeated flooring is not assigned a
	// 1/scale guarantee. The analytical single-step reference lives in
	// TestPPRLazyWalkMatchesAnalyticalStep; mass conservation is asserted by
	// the untruncated orientation/dangling/permutation/disconnected/hub/cycle
	// cases.
	goPPRScale = 1000000
	// goPPRMaxIterations bounds the lazy-walk diffusion deterministically.
	goPPRMaxIterations = 50
	// goPPRConvergenceEpsilon is the L1 early-stop threshold in mass units
	// (0.1% of total mass). The converged flag reports that L1 movement fell
	// at or below this finite threshold at the reported iteration; it is a
	// stopping outcome, not a proof of exact stationarity.
	goPPRConvergenceEpsilon = goPPRScale / 1000
	// goPPRMaxWorkUnits bounds iterations*(files+projectedEdges). Exhaustion
	// degrades to the current selector before admission with provenance.
	// It is evaluated only AFTER the projection adjacency is allocated, so
	// it cannot bound IMPORTS/TESTS expansion memory on its own; the file
	// cap below is checked before any neighbor map is built.
	goPPRMaxWorkUnits = 20000000
	// goPPRMaxProjectionFiles bounds the eligible file-level projection
	// before any adjacency allocation. Graphs with more eligible files
	// return explicit graph_size_exhausted no-signal provenance instead of
	// building the dense neighbor maps that IMPORTS/TESTS expansion (one
	// importer adjacent to every eligible file of the imported package)
	// can create on large admitted graphs (up to 4096 files/300k edges).
	// The exact bound 128 is above the existing 70-file rank-truncation
	// fixture and the 24-file planner corpus (both unaffected) and caps the
	// worst-case dense projection at 128*127/2 = 8128 undirected edges, so
	// adjacency allocation and walk work stay trivially small with no new
	// knobs. A per-edge expansion cap would need counting during insertion;
	// the file cap is checked first with no dense allocation and no
	// truncated fabricated projection.
	goPPRMaxProjectionFiles = 128
	// goPPRMaxRanks bounds stored file ranks in durable provenance.
	goPPRMaxRanks = 64
	// goPPRCliqueLimit bounds package co-membership projection: groups above
	// this size project NO intra-package edges (sparse projection) instead of
	// a synthetic hub, so large packages never gain fabricated equivalence.
	// Planner corpus admission caps at 24 files, hence planner projections
	// never reach this bound; it guards only direct primitive use.
	goPPRCliqueLimit = 32
)

// GoPPRConfig carries the fixed diffusion parameters. Only the documented
// constants are admitted; any deviation is rejected, so there is no tuning
// surface and no general-purpose policy DSL.
type GoPPRConfig struct {
	AlphaNum      int `json:"alpha_num"`
	AlphaDen      int `json:"alpha_den"`
	Scale         int `json:"scale"`
	MaxIterations int `json:"max_iterations"`
}

// DefaultGoPPRConfig returns the single admitted PPR parameter set.
func DefaultGoPPRConfig() GoPPRConfig {
	return GoPPRConfig{AlphaNum: goPPRAlphaNum, AlphaDen: goPPRAlphaDen, Scale: goPPRScale, MaxIterations: goPPRMaxIterations}
}

func (c GoPPRConfig) validate() error {
	if c != DefaultGoPPRConfig() {
		return errors.New("Go PPR parameters differ from the fixed documented treatment")
	}
	return nil
}

// GoPPRSeeds is the exact observed seed set a PPR walk restarts from. Files
// are sorted unique eligible graph paths.
type GoPPRSeeds struct {
	Files     []string `json:"files"`
	Truncated bool     `json:"truncated,omitempty"`
}

// GoPPRFileRank is one file's finite-approximation integer score in
// [0, scale]: the fixed-point mass held after the reported iteration count,
// not a certified stationary probability. Ranks order by score descending
// with exact-path tie-break.
type GoPPRFileRank struct {
	Path  string `json:"path"`
	Score int    `json:"score"`
}

// GoPPRProvenance binds one PPR computation to its exact inputs. It is the
// durable treatment identity: admission and replay recompute from the bound
// graph, query and seeds and reject any substitution.
type GoPPRProvenance struct {
	GraphDigest    string          `json:"graph_digest"`
	QuerySHA256    string          `json:"query_sha256"`
	Seeds          []string        `json:"seeds"`
	SeedsTruncated bool            `json:"seeds_truncated,omitempty"`
	AlphaNum       int             `json:"alpha_num"`
	AlphaDen       int             `json:"alpha_den"`
	Scale          int             `json:"scale"`
	Iterations     int             `json:"iterations"`
	Converged      bool            `json:"converged,omitempty"`
	NoSignal       bool            `json:"no_signal,omitempty"`
	FallbackReason string          `json:"fallback_reason,omitempty"`
	WorkExhausted  bool            `json:"work_exhausted,omitempty"`
	ProjectionHash string          `json:"projection_hash"`
	Ranks          []GoPPRFileRank `json:"ranks,omitempty"`
	RanksTruncated bool            `json:"ranks_truncated,omitempty"`
	RankHash       string          `json:"rank_hash"`
}

// DeriveGoPPRSeeds derives exact observed seed files from changed paths,
// full-identifier query matches and explicit exact path cues. It performs no
// substring/prefix guessing and applies no fixture-specific heuristics or
// manual ranker weights:
//
//   - changedPaths must each be present in the graph (absent paths are an
//     error, matching CompileGoContext corpus binding);
//   - identifier seeds require a query token (split on non [A-Za-z0-9_],
//     length >= 2) to equal a declaration label EXACTLY (case-sensitive full
//     identifier, never substring or prefix);
//   - path-cue seeds require the objective to contain a graph file's exact
//     path string.
//
// Seeds are restricted to taskcontext-eligible paths; sensitive paths never
// seed. The result is sorted with deterministic truncation at the graph seed
// bound. Empty seeds are not an error: the caller treats them as explicit
// no-signal fallback to the current selector.
func DeriveGoPPRSeeds(graph GoEngineeringGraph, objective string, changedPaths []string) (GoPPRSeeds, error) {
	var empty GoPPRSeeds
	if err := ValidateGoEngineeringGraph(graph); err != nil {
		return empty, err
	}
	if len(objective) == 0 || len(objective) > 16<<10 || !utf8.ValidString(objective) {
		return empty, errors.New("Go PPR query is outside bounds")
	}
	eligible := make(map[string]bool, len(graph.Files))
	for _, file := range graph.Files {
		eligible[file.Facts.Path] = taskcontext.EligiblePath(file.Facts.Path)
	}
	changed := append([]string(nil), changedPaths...)
	sort.Strings(changed)
	for i, path := range changed {
		if err := safepath.Relative(path); err != nil || (i > 0 && changed[i-1] == path) {
			return empty, errors.New("Go PPR changed paths are invalid or duplicate")
		}
		if !eligible[path] {
			if _, present := eligible[path]; !present {
				return empty, errors.New("Go PPR changed path absent from graph")
			}
			// Sensitive changed paths stay valid corpus members but never
			// seed model-visible ranking.
			continue
		}
	}
	seeds := make(map[string]bool)
	for _, path := range changed {
		if eligible[path] {
			seeds[path] = true
		}
	}
	tokens := make(map[string]bool)
	for _, token := range strings.FieldsFunc(objective, func(r rune) bool {
		return !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r))
	}) {
		if len([]rune(token)) >= 2 {
			tokens[token] = true
		}
	}
	for _, node := range graph.Nodes {
		if node.Kind != "declaration" || node.Path == "" || !eligible[node.Path] {
			continue
		}
		if tokens[node.Label] {
			seeds[node.Path] = true
		}
	}
	for path, ok := range eligible {
		if ok && strings.Contains(objective, path) {
			seeds[path] = true
		}
	}
	files := make([]string, 0, len(seeds))
	for path := range seeds {
		files = append(files, path)
	}
	sort.Strings(files)
	truncated := len(files) > goEngineeringMaxSeeds
	if truncated {
		files = files[:goEngineeringMaxSeeds]
	}
	return GoPPRSeeds{Files: files, Truncated: truncated}, nil
}

// ComputeGoPPR runs the fixed lazy undirected walk from the derived seeds and
// returns bounded file ranks with full treatment provenance. The graph and
// query are re-validated; the supplied graph value is never mutated. Empty
// seeds return explicit no-signal provenance (not an error) so the caller
// falls back to the current selector safely. Work exhaustion likewise
// degrades before admission with provenance instead of aborting a valid run.
// Graphs above the fixed projection file cap return explicit
// graph_size_exhausted no-signal provenance before any adjacency allocation;
// invalid graphs still reject and never return success.
func ComputeGoPPR(graph GoEngineeringGraph, seeds GoPPRSeeds, query string, config GoPPRConfig) (GoPPRProvenance, error) {
	return computeGoPPRWithBudget(graph, seeds, query, config, goPPRMaxWorkUnits)
}

func computeGoPPRWithBudget(graph GoEngineeringGraph, seeds GoPPRSeeds, query string, config GoPPRConfig, workBudget int) (GoPPRProvenance, error) {
	var empty GoPPRProvenance
	if err := ValidateGoEngineeringGraph(graph); err != nil {
		return empty, err
	}
	if err := config.validate(); err != nil {
		return empty, err
	}
	if len(query) == 0 || len(query) > 16<<10 || !utf8.ValidString(query) {
		return empty, errors.New("Go PPR query is outside bounds")
	}
	querySum := sha256.Sum256([]byte(query))
	queryHash := hex.EncodeToString(querySum[:])
	// Projection file cap before ANY neighbor map allocation: IMPORTS/TESTS
	// expansion links one importer to every eligible file of the imported
	// package, so full adjacency on a large admitted graph can allocate
	// millions of neighbor entries before the post-hoc work budget applies.
	// Count eligible files cheaply (sorted path list only, no edges) and
	// exhaust honestly when above the fixed bound. Invalid graphs already
	// rejected above; seeds below are still validated against the eligible
	// set so a foreign seed never returns success.
	eligible := make([]string, 0, len(graph.Files))
	for _, file := range graph.Files {
		if taskcontext.EligiblePath(file.Facts.Path) {
			eligible = append(eligible, file.Facts.Path)
		}
	}
	sort.Strings(eligible)
	seedFiles := append([]string(nil), seeds.Files...)
	sort.Strings(seedFiles)
	compact := seedFiles[:0]
	for _, path := range seedFiles {
		if len(compact) == 0 || compact[len(compact)-1] != path {
			compact = append(compact, path)
		}
	}
	seedFiles = compact
	eligibleIndex := make(map[string]bool, len(eligible))
	for _, path := range eligible {
		eligibleIndex[path] = true
	}
	for _, path := range seedFiles {
		if !eligibleIndex[path] {
			return empty, errors.New("Go PPR seed is absent from the eligible projection")
		}
	}
	// Empty seeds must encode as [] (not null): GoPPRProvenance.Seeds is a
	// required canonical field and the journal rejects null for nonnullable
	// slices. DeriveGoPPRSeeds already returns an empty non-nil slice; store
	// the same shape so observed and stored empty sets compare equal and
	// journal roundtrip preserves them. Nonempty encodings are unchanged, so
	// no existing treatment identity changes.
	storedSeeds := append([]string(nil), seedFiles...)
	if len(storedSeeds) == 0 {
		storedSeeds = []string{}
	}
	if len(eligible) > goPPRMaxProjectionFiles {
		exhaustedHash, err := goPPRSizeExhaustedHash(graph.Digest, eligible, config)
		if err != nil {
			return empty, err
		}
		return GoPPRProvenance{
			GraphDigest: graph.Digest, QuerySHA256: queryHash,
			Seeds: storedSeeds, SeedsTruncated: seeds.Truncated,
			AlphaNum: config.AlphaNum, AlphaDen: config.AlphaDen, Scale: config.Scale,
			NoSignal: true, WorkExhausted: true, FallbackReason: "graph_size_exhausted",
			ProjectionHash: exhaustedHash, RankHash: goPPRRankHash(nil),
		}, nil
	}
	files, adjacency := goPPRProjection(graph)
	projectionHash, err := goPPRProjectionHash(graph.Digest, files, adjacency, config)
	if err != nil {
		return empty, err
	}
	base := GoPPRProvenance{
		GraphDigest: graph.Digest, QuerySHA256: queryHash,
		AlphaNum: config.AlphaNum, AlphaDen: config.AlphaDen, Scale: config.Scale,
		ProjectionHash: projectionHash,
	}
	fileIndex := make(map[string]int, len(files))
	for i, path := range files {
		fileIndex[path] = i
	}
	for _, path := range seedFiles {
		if _, ok := fileIndex[path]; !ok {
			return empty, errors.New("Go PPR seed is absent from the eligible projection")
		}
	}
	base.Seeds = storedSeeds
	base.SeedsTruncated = seeds.Truncated
	if len(seedFiles) == 0 {
		base.NoSignal = true
		base.FallbackReason = "empty_seeds"
		base.RankHash = goPPRRankHash(nil)
		return base, nil
	}
	edgeUnits := 0
	for _, neighbors := range adjacency {
		edgeUnits += len(neighbors)
	}
	// Each iteration visits every file plus each projected undirected edge
	// once per endpoint.
	unitsPerIteration := len(files) + edgeUnits
	if unitsPerIteration < 1 {
		unitsPerIteration = 1
	}
	allowed := workBudget / unitsPerIteration
	if allowed < 1 {
		base.NoSignal = true
		base.WorkExhausted = true
		base.FallbackReason = "work_exhausted"
		base.RankHash = goPPRRankHash(nil)
		return base, nil
	}
	iterations := config.MaxIterations
	if iterations > allowed {
		iterations = allowed
	}
	restart := goPPRRestartDistribution(files, seedFiles, config.Scale)
	ranks, used, converged := goPPRLazyWalk(files, adjacency, restart, config, iterations)
	ordered := make([]GoPPRFileRank, 0, len(files))
	for i, path := range files {
		ordered = append(ordered, GoPPRFileRank{Path: path, Score: ranks[i]})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Score != ordered[j].Score {
			return ordered[i].Score > ordered[j].Score
		}
		return ordered[i].Path < ordered[j].Path
	})
	base.Iterations = used
	base.Converged = converged
	if len(ordered) > goPPRMaxRanks {
		base.RanksTruncated = true
		ordered = ordered[:goPPRMaxRanks]
	}
	base.Ranks = ordered
	base.RankHash = goPPRRankHash(ordered)
	return base, nil
}

// ValidateGoPPRProvenance recomputes the walk from the bound graph, query and
// stored seeds and rejects any stale, foreign or tampered provenance. It
// returns an error on substitution; it never silently downgrades. Seeds are
// observed caller inputs reproduced here, not derived inside: this check
// proves self-consistency from the stored seeds, so callers that own fixed
// observations (planner admission owns graph, query and nil changed paths)
// must additionally pin stored seeds to DeriveGoPPRSeeds of those observed
// inputs, rejecting a self-consistent recomputation from a different valid
// seed set.
func ValidateGoPPRProvenance(graph GoEngineeringGraph, query string, provenance GoPPRProvenance) error {
	if err := ValidateGoEngineeringGraph(graph); err != nil {
		return err
	}
	if provenance.GraphDigest != graph.Digest {
		return errors.New("Go PPR provenance binds a foreign graph")
	}
	querySum := sha256.Sum256([]byte(query))
	if provenance.QuerySHA256 != hex.EncodeToString(querySum[:]) {
		return errors.New("Go PPR provenance binds a foreign query")
	}
	config := GoPPRConfig{AlphaNum: provenance.AlphaNum, AlphaDen: provenance.AlphaDen, Scale: provenance.Scale, MaxIterations: goPPRMaxIterations}
	// Iterations are an observed outcome, not an input: recompute with the
	// full fixed budget and require the stored outcome to reproduce exactly.
	recomputed, err := ComputeGoPPR(graph, GoPPRSeeds{Files: append([]string(nil), provenance.Seeds...), Truncated: provenance.SeedsTruncated}, query, config)
	if err != nil {
		return err
	}
	if recomputed.ProjectionHash != provenance.ProjectionHash ||
		recomputed.Iterations != provenance.Iterations ||
		recomputed.Converged != provenance.Converged ||
		recomputed.NoSignal != provenance.NoSignal ||
		recomputed.FallbackReason != provenance.FallbackReason ||
		recomputed.WorkExhausted != provenance.WorkExhausted ||
		recomputed.RanksTruncated != provenance.RanksTruncated ||
		recomputed.RankHash != provenance.RankHash ||
		len(recomputed.Ranks) != len(provenance.Ranks) {
		return errors.New("Go PPR provenance does not reproduce from bound inputs")
	}
	for i := range recomputed.Ranks {
		if recomputed.Ranks[i] != provenance.Ranks[i] {
			return errors.New("Go PPR rank substitution")
		}
	}
	return nil
}

// goPPRProjection builds the deterministic file-level undirected adjacency
// used by the walk. Only admitted relations project; nothing is inferred.
// Every projected relation is treated as UNDIRECTED adjacency for the lazy
// walk (mass flows both ways along one undirected edge); direction is not
// otherwise modeled and no directed call resolution is performed:
//
//   - IN_PACKAGE / DECLARED_OWNERSHIP co-membership: files sharing one exact
//     package binding node are adjacent (clique up to goPPRCliqueLimit).
//     Larger groups project no intra-package edges at all: a sparse
//     projection with no synthetic hub and no fabricated equivalence.
//   - IMPORTS: an importer file is adjacent to every eligible local file of
//     the imported package. External imports with no local files add no edge.
//   - TESTS: a test file is adjacent to every other eligible file of the
//     tested package.
//   - GENERATED_BY: the generated file is adjacent to its generator file
//     (explicit source-bound relation only).
//   - CALLS_UNRESOLVED file-to-call containment and CALL_NAME_CANDIDATE
//     same-file spelling hints NEVER create cross-file edges: the graph
//     leaves calls UNRESOLVED and PPR must not fabricate call resolution.
//
// Duplicate undirected edges are removed; self-loops are dropped (a file is
// never its own neighbor; dangling files keep mass through the lazy stay).
// Only taskcontext-eligible paths participate; sensitive paths are omitted
// from the projection without any absence claim.
func goPPRProjection(graph GoEngineeringGraph) (files []string, adjacency map[string][]string) {
	eligible := make(map[string]bool, len(graph.Files))
	for _, file := range graph.Files {
		if taskcontext.EligiblePath(file.Facts.Path) {
			eligible[file.Facts.Path] = true
		}
	}
	files = make([]string, 0, len(eligible))
	for path := range eligible {
		files = append(files, path)
	}
	sort.Strings(files)
	neighbors := make(map[string]map[string]bool, len(files))
	for _, path := range files {
		neighbors[path] = make(map[string]bool)
	}
	link := func(a, b string) {
		if a == b || !eligible[a] || !eligible[b] {
			return
		}
		neighbors[a][b] = true
		neighbors[b][a] = true
	}
	packageFiles := make(map[string][]string)
	for _, file := range graph.Files {
		if !eligible[file.Facts.Path] {
			continue
		}
		id := graphPackageBindingNodeID(file.Package)
		packageFiles[id] = append(packageFiles[id], file.Facts.Path)
	}
	for _, members := range packageFiles {
		sort.Strings(members)
		if len(members) > goPPRCliqueLimit {
			// Oversized package group: sparse projection, no synthetic
			// hub. Member files stay connected only through other
			// admitted relations (imports, tests, generation).
			continue
		}
		for i := 0; i < len(members); i++ {
			for j := i + 1; j < len(members); j++ {
				link(members[i], members[j])
			}
		}
	}
	fileByNodeID := make(map[string]string, len(files))
	for _, file := range graph.Files {
		if eligible[file.Facts.Path] {
			fileByNodeID[graphFileNodeID(file.Facts.Path)] = file.Facts.Path
		}
	}
	for _, edge := range graph.Edges {
		switch edge.Relation {
		case "IMPORTS":
			importer := edge.Path
			for _, member := range packageFiles[edge.To] {
				link(importer, member)
			}
		case "TESTS":
			tester := edge.Path
			for _, member := range packageFiles[edge.To] {
				link(tester, member)
			}
		case "GENERATED_BY":
			generated, okGenerated := fileByNodeID[edge.From]
			generator, okGenerator := fileByNodeID[edge.To]
			if okGenerated && okGenerator {
				link(generated, generator)
			}
		}
	}
	adjacency = make(map[string][]string, len(files))
	for _, path := range files {
		set := neighbors[path]
		list := make([]string, 0, len(set))
		for neighbor := range set {
			list = append(list, neighbor)
		}
		sort.Strings(list)
		adjacency[path] = list
	}
	return files, adjacency
}

func goPPRProjectionHash(graphDigest string, files []string, adjacency map[string][]string, config GoPPRConfig) (string, error) {
	edges := make([]string, 0)
	for _, path := range files {
		for _, neighbor := range adjacency[path] {
			if path < neighbor {
				edges = append(edges, path+"\x00"+neighbor)
			}
		}
	}
	sort.Strings(edges)
	return canonical.Hash("harness.ri.go-ppr-projection.v1", struct {
		GraphDigest string   `json:"graph_digest"`
		Files       []string `json:"files"`
		Edges       []string `json:"edges"`
		AlphaNum    int      `json:"alpha_num"`
		AlphaDen    int      `json:"alpha_den"`
		Scale       int      `json:"scale"`
	}{graphDigest, files, edges, config.AlphaNum, config.AlphaDen, config.Scale})
}

// goPPRSizeExhaustedHash binds an oversized-graph fallback to its exact
// inputs without fabricating a projection: it hashes the graph digest, the
// sorted eligible file list and the fixed parameters under a distinct domain
// that never collides with a real projection hash (which also covers edges).
// Recomputation from the same bound graph yields the same hash, so admission
// validation passes; a different graph yields a different hash and rejects.
func goPPRSizeExhaustedHash(graphDigest string, files []string, config GoPPRConfig) (string, error) {
	return canonical.Hash("harness.ri.go-ppr-projection-size-exhausted.v1", struct {
		GraphDigest string   `json:"graph_digest"`
		Files       []string `json:"files"`
		AlphaNum    int      `json:"alpha_num"`
		AlphaDen    int      `json:"alpha_den"`
		Scale       int      `json:"scale"`
	}{graphDigest, files, config.AlphaNum, config.AlphaDen, config.Scale})
}

func goPPRRankHash(ranks []GoPPRFileRank) string {
	hash, err := canonical.Hash("harness.ri.go-ppr-ranks.v1", ranks)
	if err != nil {
		// Canonical hashing of a bounded in-memory rank list cannot fail for
		// a JSON-marshalable value; an empty digest is never admitted because
		// validators compare it against a recomputed hash.
		return ""
	}
	return hash
}

// goPPRRestartDistribution spreads total mass uniformly over sorted seeds,
// dealing the integer remainder one unit at a time to the smallest paths so
// mass is conserved exactly.
func goPPRRestartDistribution(files, seeds []string, scale int) []int {
	restart := make([]int, len(files))
	if len(seeds) == 0 {
		return restart
	}
	sorted := append([]string(nil), seeds...)
	sort.Strings(sorted)
	base := scale / len(sorted)
	remainder := scale % len(sorted)
	index := make(map[string]int, len(files))
	for i, path := range files {
		index[path] = i
	}
	for i, path := range sorted {
		mass := base
		if i < remainder {
			mass++
		}
		restart[index[path]] = mass
	}
	return restart
}

// goPPRLazyWalk diffuses mass with the lazy undirected transition: every
// node keeps half its mass (floor) and spreads the rest uniformly over
// sorted neighbors with deterministic remainder placement; dangling nodes
// keep everything. The restart combination takes floor((3*s + 17*lazy)/20)
// per node and deals the global remainder to the smallest paths, so the
// returned distribution always sums to scale. It reports the used iteration
// count and whether L1 movement fell at or below the convergence epsilon:
// converged is that finite-threshold outcome, never an exact stationary
// proof. Repeated integer flooring across iterations carries no per-score
// error guarantee; the walk is validated by exact mass conservation and the
// analytical single-step reference, not by a convergence certificate.
func goPPRLazyWalk(files []string, adjacency map[string][]string, restart []int, config GoPPRConfig, iterations int) (scores []int, used int, converged bool) {
	current := make([]int, len(files))
	for i := range files {
		current[i] = restart[i]
	}
	index := make(map[string]int, len(files))
	for i, path := range files {
		index[path] = i
	}
	neighbors := make([][]int, len(files))
	for i, path := range files {
		list := adjacency[path]
		ids := make([]int, 0, len(list))
		for _, neighbor := range list {
			ids = append(ids, index[neighbor])
		}
		sort.Ints(ids)
		neighbors[i] = ids
	}
	lazy := make([]int, len(files))
	next := make([]int, len(files))
	for step := 0; step < iterations; step++ {
		for i := range files {
			lazy[i] = 0
		}
		for i := range files {
			mass := current[i]
			degree := len(neighbors[i])
			if degree == 0 {
				lazy[i] += mass
				continue
			}
			stay := mass / 2
			movable := mass - stay
			share := movable / degree
			remainder := movable % degree
			lazy[i] += stay
			for rank, target := range neighbors[i] {
				add := share
				if rank < remainder {
					add++
				}
				lazy[target] += add
			}
		}
		total := 0
		for i := range files {
			// (3*restart + 17*lazy)/20 with alpha = 17/20.
			value := (goPPRRestartNum*restart[i] + goPPRAlphaNum*lazy[i]) / goPPRAlphaDen
			next[i] = value
			total += value
		}
		for i := range files {
			if total >= config.Scale {
				break
			}
			next[i]++
			total++
		}
		movement := 0
		for i := range files {
			delta := next[i] - current[i]
			if delta < 0 {
				delta = -delta
			}
			movement += delta
		}
		copy(current, next)
		used = step + 1
		if movement <= goPPRConvergenceEpsilon {
			converged = true
			break
		}
	}
	return current, used, converged
}
