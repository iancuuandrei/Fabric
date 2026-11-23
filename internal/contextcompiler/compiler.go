package contextcompiler

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/iancuuandrei/fabric/internal/codegraph"
	"github.com/iancuuandrei/fabric/internal/gitexec"
	"github.com/iancuuandrei/fabric/internal/memory"
)

const (
	defaultBudgetTokens = 3200
	minBudgetTokens     = 400
	maxBudgetTokens     = 24000
	maxFocalBodies      = 3
)

// SkillHint provides candidate skill metadata for task-and-effect skill ranking.
type SkillHint struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Options configures a deterministic ContextCompiler pass.
type Options struct {
	Query        string
	SeedFiles    []string
	BudgetTokens int
	MaxHops      int
	SpecOnly     bool
	IncludeDiff  bool
	UseCache     bool
	Skills       []SkillHint
}

// CompiledItem is one ranked symbol, file, or doc packed into a ContextPack at
// an explicit fidelity tier (L3_BODY, L2_SPEC, L1_OUTLINE, or L0_POINTER).
type CompiledItem struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	Path       string   `json:"path"`
	StartLine  int      `json:"start_line,omitempty"`
	EndLine    int      `json:"end_line,omitempty"`
	Language   string   `json:"language,omitempty"`
	Resolution string   `json:"resolution"`
	Score      float64  `json:"score"`
	Reasons    []string `json:"reasons"`
	Tokens     int      `json:"tokens"`
	Content    string   `json:"content"`
	Effects    []string `json:"effects,omitempty"`
}

// CallEdgeSummary describes one relevant function-to-function or type relation
// linking selected context items.
type CallEdgeSummary struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Relation   string `json:"relation"`
	Provenance string `json:"provenance"`
}

// GuidanceSnippet holds one scoped repository instruction file (AGENTS.md or
// CLAUDE.md) relevant to the top matched directories.
type GuidanceSnippet struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ContextPack is the compiled, budget-bounded, KV-cache-friendly context
// payload returned to agents, CLI callers, HTTP clients, and MCP tools.
type ContextPack struct {
	Query             string            `json:"query"`
	BudgetTokens      int               `json:"budget_tokens"`
	UsedTokens        int               `json:"used_tokens"`
	CacheHit          bool              `json:"cache_hit"`
	SeedIDs           []string          `json:"seed_ids,omitempty"`
	Items             []CompiledItem    `json:"items"`
	CallGraph         []CallEdgeSummary `json:"call_graph,omitempty"`
	UntestedSymbols   []string          `json:"untested_symbols,omitempty"`
	ScopedGuidance    []GuidanceSnippet `json:"scoped_guidance,omitempty"`
	RecommendedSkills []string          `json:"recommended_skills,omitempty"`
	Lessons           []memory.Lesson   `json:"lessons,omitempty"`
}

var (
	backtickTokRe = regexp.MustCompile("`([^`\\s]+)`")
	fileLineRe    = regexp.MustCompile(`([A-Za-z0-9_./-]+\.[a-zA-Z0-9]{1,5})(?::(\d+))?`)
)

type querySignals struct {
	tokens    []string
	tokenSet  map[string]bool
	rawTerms  map[string]bool
	seedPaths map[string]bool
	diffPaths map[string]bool
}

type scoredCandidate struct {
	node    codegraph.Node
	score   float64
	reasons []string
}

// Compile analyzes repoRoot using deterministic AST/Javadoc specs, BM25
// lexical scoring, task-seeded Personalized PageRank, bounded Git co-change
// history, scoped governance docs, and verified memory within BudgetTokens.
func Compile(ctx context.Context, repoRoot string, opts Options) (ContextPack, error) {
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return ContextPack{}, err
	}
	budget := clampBudget(opts.BudgetTokens)
	maxHops := opts.MaxHops
	if maxHops <= 0 || maxHops > 4 {
		maxHops = 2
	}

	g, cacheHit, err := LoadOrBuildGraph(absRoot, opts.UseCache)
	if err != nil {
		return ContextPack{}, err
	}

	sig := extractQuerySignals(ctx, absRoot, opts)
	bm25Scores := computeBM25(g.Nodes, sig.tokenSet)
	seedWeights, seedIDs, seedFilePaths := resolveSeeds(g, sig, bm25Scores)
	pprScores := g.PersonalizedRank(seedWeights)
	hopDist := computeHopDistances(g, seedWeights, maxHops)
	coChangeScores := computeGitCoChange(ctx, absRoot, seedFilePaths)

	candidates := selectCandidateNodes(g.Nodes)
	ranked := fuseCandidateScores(candidates, seedWeights, bm25Scores, pprScores, coChangeScores, hopDist, maxHops, sig)

	// Reserve budget proportionally for guidance, skills, call-graph summary, and memory.
	auxBudget := min(900, budget/3)
	codeBudget := budget - auxBudget
	if codeBudget < 250 {
		codeBudget = budget
	}

	items, usedCodeTokens := packMultiResolution(absRoot, g, ranked, codeBudget, opts.SpecOnly)
	callGraph, untested := collectCallGraphAndUntested(g, items)
	guidance := loadScopedGuidance(absRoot, items, auxBudget/2)
	skills := rankSkills(opts.Skills, sig.tokenSet, items, untested)
	lessons := memory.Relevant(absRoot, opts.Query, 5)

	pack := ContextPack{
		Query:             opts.Query,
		BudgetTokens:      budget,
		CacheHit:          cacheHit,
		SeedIDs:           seedIDs,
		Items:             items,
		CallGraph:         callGraph,
		UntestedSymbols:   untested,
		ScopedGuidance:    guidance,
		RecommendedSkills: skills,
		Lessons:           lessons,
	}
	mdTokens := estimateTokens(pack.Markdown())
	if mdTokens > usedCodeTokens {
		pack.UsedTokens = mdTokens
	} else {
		pack.UsedTokens = usedCodeTokens
	}
	return pack, nil
}

// Markdown renders the ContextPack cleanly and deterministically (without
// timestamps or random IDs) so Turn-1 prefixes remain KV-cache stable.
func (p ContextPack) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Compiled Context Pack (budget=%d tok, items=%d)\n", p.BudgetTokens, len(p.Items))
	if q := strings.TrimSpace(p.Query); q != "" {
		fmt.Fprintf(&b, "- **Objective:** %s\n", strings.Join(strings.Fields(q), " "))
	}
	if len(p.RecommendedSkills) > 0 {
		fmt.Fprintf(&b, "- **Recommended Skills (`load_skill`):** `%s`\n", strings.Join(p.RecommendedSkills, "`, `"))
	}
	if len(p.UntestedSymbols) > 0 {
		fmt.Fprintf(&b, "- **Untested Focal Symbols:** `%s`\n", strings.Join(p.UntestedSymbols, "`, `"))
	}
	b.WriteByte('\n')

	for _, it := range p.Items {
		reasons := strings.Join(it.Reasons, ",")
		if reasons != "" {
			fmt.Fprintf(&b, "<!-- [%s] score=%.4f via=%s -->\n", it.Resolution, it.Score, reasons)
		}
		b.WriteString(it.Content)
		if !strings.HasSuffix(it.Content, "\n") {
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}

	if len(p.CallGraph) > 0 {
		b.WriteString("### Function Call & Contract Graph\n")
		for _, e := range p.CallGraph {
			fmt.Fprintf(&b, "- `%s` --`%s` (%s)--> `%s`\n", e.From, e.Relation, e.Provenance, e.To)
		}
		b.WriteByte('\n')
	}

	if len(p.ScopedGuidance) > 0 {
		b.WriteString("### Scoped Repository Guidance\n")
		for _, g := range p.ScopedGuidance {
			fmt.Fprintf(&b, "#### `%s`\n%s\n\n", g.Path, strings.TrimSpace(g.Content))
		}
	}

	if len(p.Lessons) > 0 {
		b.WriteString(memory.FormatForPrompt(p.Lessons))
		b.WriteByte('\n')
	}
	return b.String()
}

func clampBudget(b int) int {
	if b <= 0 {
		return defaultBudgetTokens
	}
	if b < minBudgetTokens {
		return minBudgetTokens
	}
	if b > maxBudgetTokens {
		return maxBudgetTokens
	}
	return b
}

func estimateTokens(s string) int {
	if len(s) == 0 {
		return 0
	}
	return (len(s) + 3) / 4
}

func extractQuerySignals(ctx context.Context, absRoot string, opts Options) querySignals {
	tokenSet := map[string]bool{}
	rawTerms := map[string]bool{}
	seedPaths := map[string]bool{}
	diffPaths := map[string]bool{}

	for _, f := range opts.SeedFiles {
		clean := filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(f), "./"))
		if clean != "" {
			seedPaths[clean] = true
		}
	}
	for _, m := range fileLineRe.FindAllStringSubmatch(opts.Query, -1) {
		if len(m) > 1 {
			clean := filepath.ToSlash(strings.TrimPrefix(m[1], "./"))
			seedPaths[clean] = true
		}
	}
	for _, m := range backtickTokRe.FindAllStringSubmatch(opts.Query, -1) {
		if len(m) > 1 {
			t := strings.ToLower(strings.Trim(m[1], "()"))
			if len(t) >= 2 {
				rawTerms[t] = true
			}
		}
	}

	tokens := splitIdentifiers(opts.Query)
	for _, tok := range tokens {
		tokenSet[tok] = true
	}
	if opts.IncludeDiff {
		for _, df := range collectWorkingDiffFiles(ctx, absRoot) {
			diffPaths[df] = true
		}
	}
	if len(tokens) == 0 && len(seedPaths) == 0 {
		for df := range diffPaths {
			seedPaths[df] = true
		}
	}
	for sp := range seedPaths {
		for _, tok := range splitIdentifiers(sp) {
			tokenSet[tok] = true
		}
	}
	return querySignals{
		tokens:    tokens,
		tokenSet:  tokenSet,
		rawTerms:  rawTerms,
		seedPaths: seedPaths,
		diffPaths: diffPaths,
	}
}

func collectWorkingDiffFiles(ctx context.Context, absRoot string) []string {
	seen := map[string]bool{}
	var out []string
	cmds := [][]string{
		{"diff", "--name-only"},
		{"diff", "--cached", "--name-only"},
		{"ls-files", "--others", "--exclude-standard"},
	}
	for _, args := range cmds {
		b, err := gitexec.CommandContext(ctx, append([]string{"-C", absRoot}, args...)...).Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
			slash := filepath.ToSlash(strings.TrimSpace(line))
			if slash != "" && !seen[slash] {
				seen[slash] = true
				out = append(out, slash)
			}
		}
	}
	sort.Strings(out)
	return out
}

func resolveSeeds(g *codegraph.Graph, sig querySignals, bm25Scores map[string]float64) (map[string]float64, []string, map[string]bool) {
	seedWeights := map[string]float64{}
	seedPaths := map[string]bool{}
	for _, n := range g.Nodes {
		if n.Kind == "doc" {
			continue
		}
		weight := 0.0
		lowerPath := strings.ToLower(n.Path)
		lowerName := strings.ToLower(n.Name)

		for sp := range sig.seedPaths {
			lsp := strings.ToLower(sp)
			if lowerPath == lsp || strings.HasSuffix(lowerPath, "/"+lsp) {
				weight += 3.0
				seedPaths[n.Path] = true
			}
		}
		if sig.rawTerms[lowerName] {
			weight += 3.5
			seedPaths[n.Path] = true
		}
		if len(lowerName) >= 4 && sig.tokenSet[lowerName] && (len(sig.tokenSet) == 1 || len(lowerName) >= 8) {
			weight += 2.0
			seedPaths[n.Path] = true
		}
		if weight > 0 {
			seedWeights[n.ID] = weight
		}
	}

	seedTopBM25Symbols(g.Nodes, bm25Scores, seedWeights, seedPaths)

	seedIDs := make([]string, 0, len(seedWeights))
	for id := range seedWeights {
		seedIDs = append(seedIDs, id)
	}
	sort.Strings(seedIDs)
	return seedWeights, seedIDs, seedPaths
}

func seedTopBM25Symbols(nodes []codegraph.Node, bm25Scores map[string]float64, seedWeights map[string]float64, seedPaths map[string]bool) {
	type bmHit struct {
		id    string
		path  string
		score float64
	}
	var hits []bmHit
	for _, n := range nodes {
		if n.Kind == "file" || n.Kind == "doc" || n.Kind == "test" {
			continue
		}
		if sc := bm25Scores[n.ID]; sc > 0 {
			hits = append(hits, bmHit{id: n.ID, path: n.Path, score: sc})
		}
	}
	if len(hits) == 0 {
		return
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].id < hits[j].id
	})
	maxBM := hits[0].score
	limit := min(5, len(hits))
	for i := 0; i < limit; i++ {
		if hits[i].score < maxBM*0.65 {
			break
		}
		seedWeights[hits[i].id] += 2.5 * (hits[i].score / maxBM)
		seedPaths[hits[i].path] = true
	}
}

func selectCandidateNodes(nodes []codegraph.Node) []codegraph.Node {
	filesWithSymbols := map[string]bool{}
	for _, n := range nodes {
		if n.Kind != "file" && n.Kind != "doc" {
			filesWithSymbols[n.Path] = true
		}
	}
	var out []codegraph.Node
	for _, n := range nodes {
		if n.Kind == "doc" {
			continue
		}
		if n.Kind == "file" && filesWithSymbols[n.Path] {
			continue
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func computeBM25(nodes []codegraph.Node, qTokens map[string]bool) map[string]float64 {
	out := make(map[string]float64, len(nodes))
	if len(nodes) == 0 || len(qTokens) == 0 {
		return out
	}
	docs := make(map[string]map[string]int, len(nodes))
	lengths := make(map[string]int, len(nodes))
	df := map[string]int{}
	totalLen := 0

	for _, n := range nodes {
		tf := map[string]int{}
		text := nodeSearchText(n)
		toks := splitIdentifiers(text)
		lengths[n.ID] = max(1, len(toks))
		totalLen += lengths[n.ID]
		seen := map[string]bool{}
		for _, tok := range toks {
			tf[tok]++
			if !seen[tok] {
				seen[tok] = true
				df[tok]++
			}
		}
		docs[n.ID] = tf
	}
	avgDL := float64(totalLen) / float64(len(nodes))
	const k1, b = 1.5, 0.75
	nDocs := float64(len(nodes))

	for _, n := range nodes {
		tf := docs[n.ID]
		dl := float64(lengths[n.ID])
		score := 0.0
		for q := range qTokens {
			freq := float64(tf[q])
			if freq == 0 {
				continue
			}
			docFreq := float64(df[q])
			idf := math.Log(1.0 + (nDocs-docFreq+0.5)/(docFreq+0.5))
			denom := freq + k1*(1.0-b+b*(dl/avgDL))
			score += idf * ((freq * (k1 + 1.0)) / denom)
		}
		if score > 0 {
			out[n.ID] = score
		}
	}
	return out
}

func nodeSearchText(n codegraph.Node) string {
	var b strings.Builder
	b.WriteString(n.Name)
	b.WriteByte(' ')
	b.WriteString(n.Name)
	b.WriteByte(' ')
	b.WriteString(n.Path)
	if n.Spec != nil {
		b.WriteByte(' ')
		b.WriteString(n.Spec.Signature)
		b.WriteByte(' ')
		b.WriteString(n.Spec.Summary)
		b.WriteByte(' ')
		b.WriteString(n.Spec.Returns)
		for _, p := range n.Spec.Params {
			b.WriteByte(' ')
			b.WriteString(p.Name)
			b.WriteByte(' ')
			b.WriteString(p.Desc)
		}
		for _, e := range n.Spec.Errors {
			b.WriteByte(' ')
			b.WriteString(e)
		}
	}
	return b.String()
}

func computeGitCoChange(ctx context.Context, absRoot string, seedFiles map[string]bool) map[string]float64 {
	out := map[string]float64{}
	if len(seedFiles) == 0 {
		return out
	}
	coCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := gitexec.CommandContext(coCtx, "-C", absRoot, "log", "-n", "40", "--name-only", "--pretty=format:__COMMIT__").Output()
	if err != nil {
		return out
	}

	var cur []string
	flushCommit := func() {
		if len(cur) == 0 || len(cur) > 35 {
			cur = nil
			return
		}
		touchesSeed := false
		for _, p := range cur {
			if seedFiles[p] {
				touchesSeed = true
				break
			}
		}
		if touchesSeed {
			w := 1.0 / math.Sqrt(float64(len(cur)))
			for _, p := range cur {
				if !seedFiles[p] {
					out[p] += w
				}
			}
		}
		cur = nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		t := filepath.ToSlash(strings.TrimSpace(line))
		if t == "" {
			continue
		}
		if t == "__COMMIT__" {
			flushCommit()
			continue
		}
		cur = append(cur, t)
	}
	flushCommit()
	return out
}

func computeHopDistances(g *codegraph.Graph, seeds map[string]float64, maxHops int) map[string]int {
	dist := make(map[string]int)
	if maxHops <= 0 || len(seeds) == 0 {
		return dist
	}
	adj, valid := buildNonDocAdjacency(g)
	var frontier []string
	for id, w := range seeds {
		if w > 0 && valid[id] {
			dist[id] = 0
			frontier = append(frontier, id)
		}
	}
	sort.Strings(frontier)
	for hop := 1; hop <= maxHops && len(frontier) > 0; hop++ {
		frontier = expandHopFrontier(frontier, adj, dist, hop)
	}
	return dist
}

func buildNonDocAdjacency(g *codegraph.Graph) (map[string][]string, map[string]bool) {
	valid := make(map[string]bool, len(g.Nodes))
	for _, n := range g.Nodes {
		if n.Kind != "doc" {
			valid[n.ID] = true
		}
	}
	adj := make(map[string][]string, len(g.Nodes))
	for _, e := range g.Edges {
		if valid[e.From] && valid[e.To] && e.From != e.To {
			adj[e.From] = append(adj[e.From], e.To)
			adj[e.To] = append(adj[e.To], e.From)
		}
	}
	return adj, valid
}

func expandHopFrontier(frontier []string, adj map[string][]string, dist map[string]int, hop int) []string {
	var next []string
	for _, u := range frontier {
		for _, v := range adj[u] {
			if _, seen := dist[v]; !seen {
				dist[v] = hop
				next = append(next, v)
			}
		}
	}
	return next
}

func fuseCandidateScores(candidates []codegraph.Node, seeds, bm25, ppr, coChange map[string]float64, hopDist map[string]int, maxHops int, sig querySignals) []scoredCandidate {
	seedRanks := rankMap(seeds)
	bm25Ranks := rankMap(bm25)
	pprRanks := rankMap(ppr)
	coByNode := make(map[string]float64, len(candidates))
	for _, n := range candidates {
		if w := coChange[n.Path]; w > 0 {
			coByNode[n.ID] = w
		}
	}
	coRanks := rankMap(coByNode)
	pprLimit := max(1, maxHops) * 20

	out := make([]scoredCandidate, 0, len(candidates))
	for _, n := range candidates {
		score := 0.0
		var reasons []string
		if r, ok := seedRanks[n.ID]; ok {
			score += 2.2 / float64(60+r)
			reasons = append(reasons, "SEED")
		}
		if r, ok := bm25Ranks[n.ID]; ok {
			score += 1.6 / float64(60+r)
			reasons = append(reasons, "BM25")
		}
		if r, ok := pprRanks[n.ID]; ok && r <= pprLimit {
			score += 1.3 / float64(60+r)
			reasons = append(reasons, "PPR")
		}
		if d, ok := hopDist[n.ID]; ok && d > 0 {
			score += 0.6 / float64(60+d*10)
			reasons = append(reasons, fmt.Sprintf("HOP_%d", d))
		}
		if r, ok := coRanks[n.ID]; ok {
			score += 1.1 / float64(60+r)
			reasons = append(reasons, "CO_CHANGE")
		}
		if sig.seedPaths[n.Path] && !containsStr(reasons, "SEED") {
			score += 0.015
			reasons = append(reasons, "SEED_FILE")
		} else if sig.diffPaths[n.Path] && !containsStr(reasons, "SEED") {
			score += 0.008
			reasons = append(reasons, "DIFF")
		}
		if n.Kind == "test" && len(reasons) > 0 {
			score *= 0.85
		}
		if len(reasons) == 0 && len(sig.tokenSet) > 0 {
			continue
		}
		if len(reasons) == 0 {
			score = n.Centrality
			reasons = append(reasons, "HUB")
		}
		out = append(out, scoredCandidate{node: n, score: score, reasons: reasons})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].node.ID < out[j].node.ID
	})
	return out
}

func rankMap(scores map[string]float64) map[string]int {
	type pair struct {
		id string
		v  float64
	}
	items := make([]pair, 0, len(scores))
	for id, v := range scores {
		if v > 0 {
			items = append(items, pair{id: id, v: v})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].v != items[j].v {
			return items[i].v > items[j].v
		}
		return items[i].id < items[j].id
	})
	ranks := make(map[string]int, len(items))
	for i, it := range items {
		ranks[it.id] = i + 1
	}
	return ranks
}

func packMultiResolution(absRoot string, g *codegraph.Graph, ranked []scoredCandidate, codeBudget int, specOnly bool) ([]CompiledItem, int) {
	used := 0
	l3Count := 0
	l3Cap := max(250, (codeBudget*35)/100)
	l3Used := 0
	items := make([]CompiledItem, 0, min(len(ranked), 24))

	for i, cand := range ranked {
		if len(items) >= 24 || used >= codeBudget {
			break
		}
		n := cand.node
		resLevel, content := renderCandidateResolution(absRoot, g, n, i, specOnly, l3Count, l3Used, l3Cap, codeBudget-used)
		if content == "" {
			continue
		}
		toks := estimateTokens(content)
		if used+toks > codeBudget && i > 0 {
			// Try demoting cleanly to outline or pointer before stopping.
			resLevel = "L0_POINTER"
			content = renderPointer(n)
			toks = estimateTokens(content)
			if used+toks > codeBudget {
				continue
			}
		}
		if resLevel == "L3_BODY" {
			l3Count++
			l3Used += toks
		}
		var effects []string
		endLine := n.Line
		if n.Spec != nil {
			effects = append([]string(nil), n.Spec.Effects...)
			if n.Spec.EndLine > endLine {
				endLine = n.Spec.EndLine
			}
		}
		items = append(items, CompiledItem{
			ID:         n.ID,
			Kind:       n.Kind,
			Name:       n.Name,
			Path:       n.Path,
			StartLine:  n.Line,
			EndLine:    endLine,
			Language:   n.Language,
			Resolution: resLevel,
			Score:      math.Round(cand.score*10000) / 10000,
			Reasons:    cand.reasons,
			Tokens:     toks,
			Content:    content,
			Effects:    effects,
		})
		used += toks
	}
	return items, used
}

func renderCandidateResolution(absRoot string, g *codegraph.Graph, n codegraph.Node, rankIdx int, specOnly bool, l3Count, l3Used, l3Cap, remaining int) (string, string) {
	if !specOnly && l3Count < maxFocalBodies && n.Line > 0 && n.Spec != nil && n.Spec.EndLine >= n.Line {
		if body, ok := readSpanSnippet(absRoot, n); ok {
			bt := estimateTokens(body)
			if l3Used+bt <= l3Cap && bt <= remaining {
				return "L3_BODY", body
			}
		}
	}
	if rankIdx < 12 {
		spec := g.SpecCard(n.ID)
		if spec != "" && (estimateTokens(spec) <= remaining || rankIdx == 0) {
			return "L2_SPEC", spec
		}
	}
	outline := renderOutline(n)
	if estimateTokens(outline) <= remaining {
		return "L1_OUTLINE", outline
	}
	return "L0_POINTER", renderPointer(n)
}

func readSpanSnippet(absRoot string, n codegraph.Node) (string, bool) {
	if n.Spec == nil || n.Line <= 0 || n.Spec.EndLine < n.Line {
		return "", false
	}
	spanLines := n.Spec.EndLine - n.Line + 1
	if spanLines > 85 {
		return "", false
	}
	raw, err := os.ReadFile(filepath.Join(absRoot, filepath.FromSlash(n.Path)))
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	if n.Line > len(lines) {
		return "", false
	}
	end := min(len(lines), n.Spec.EndLine)
	var b strings.Builder
	fmt.Fprintf(&b, "### `%s` (`%s:%d-%d`)\n```%s\n", n.Name, n.Path, n.Line, end, n.Language)
	for ln := n.Line; ln <= end; ln++ {
		fmt.Fprintf(&b, "%4d | %s\n", ln, lines[ln-1])
	}
	b.WriteString("```\n")
	return b.String(), true
}

func renderOutline(n codegraph.Node) string {
	sig := n.Name
	summary := ""
	if n.Spec != nil {
		if n.Spec.Signature != "" {
			sig = n.Spec.Signature
		}
		summary = n.Spec.Summary
	}
	if summary != "" {
		return fmt.Sprintf("- `%s:%d` `%s` — %s", n.Path, n.Line, sig, summary)
	}
	return fmt.Sprintf("- `%s:%d` `%s` (%s)", n.Path, n.Line, sig, n.Kind)
}

func renderPointer(n codegraph.Node) string {
	return fmt.Sprintf("- `%s:%d` `%s`", n.Path, n.Line, n.Name)
}

func collectCallGraphAndUntested(g *codegraph.Graph, items []CompiledItem) ([]CallEdgeSummary, []string) {
	selected := map[string]CompiledItem{}
	for _, it := range items {
		selected[it.ID] = it
	}
	nodeByID := make(map[string]codegraph.Node, len(g.Nodes))
	for _, n := range g.Nodes {
		nodeByID[n.ID] = n
	}

	edges, tested := collectSelectedEdges(g, selected, nodeByID)
	sortCallEdgeSummaries(edges)
	if len(edges) > 24 {
		edges = edges[:24]
	}

	var untested []string
	for _, it := range items {
		if (it.Kind == "func" || it.Kind == "method") && !tested[it.ID] {
			untested = append(untested, fmt.Sprintf("%s (%s:%d)", it.Name, it.Path, it.StartLine))
			if len(untested) >= 10 {
				break
			}
		}
	}
	return edges, untested
}

func collectSelectedEdges(g *codegraph.Graph, selected map[string]CompiledItem, nodeByID map[string]codegraph.Node) ([]CallEdgeSummary, map[string]bool) {
	tested := map[string]bool{}
	var edges []CallEdgeSummary
	seenEdge := map[string]bool{}
	for _, e := range g.Edges {
		if e.Relation == "TESTS" || (e.Relation == "CALLS" && nodeByID[e.From].Kind == "test") {
			tested[e.To] = true
		}
		if e.Relation != "CALLS" && e.Relation != "USES_TYPE" && e.Relation != "TESTS" {
			continue
		}
		_, fromSel := selected[e.From]
		_, toSel := selected[e.To]
		if !fromSel && !toSel {
			continue
		}
		fromN, okF := nodeByID[e.From]
		toN, okT := nodeByID[e.To]
		if !okF || !okT {
			continue
		}
		fromLabel := fmt.Sprintf("%s:%s", filepath.Base(fromN.Path), fromN.Name)
		toLabel := fmt.Sprintf("%s:%s", filepath.Base(toN.Path), toN.Name)
		key := fromLabel + "|" + e.Relation + "|" + toLabel
		if seenEdge[key] {
			continue
		}
		seenEdge[key] = true
		edges = append(edges, CallEdgeSummary{
			From:       fromLabel,
			To:         toLabel,
			Relation:   e.Relation,
			Provenance: e.Provenance,
		})
	}
	return edges, tested
}

func sortCallEdgeSummaries(edges []CallEdgeSummary) {
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		if edges[i].Relation != edges[j].Relation {
			return edges[i].Relation < edges[j].Relation
		}
		return edges[i].To < edges[j].To
	})
}

func loadScopedGuidance(absRoot string, items []CompiledItem, maxTokens int) []GuidanceSnippet {
	dirs := []string{"."}
	seenDir := map[string]bool{".": true}
	for _, it := range items {
		dir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(it.Path)))
		for cur := dir; cur != "" && cur != "."; cur = filepath.ToSlash(filepath.Dir(filepath.FromSlash(cur))) {
			if !seenDir[cur] {
				seenDir[cur] = true
				dirs = append(dirs, cur)
			}
		}
		if len(dirs) >= 8 {
			break
		}
	}
	// Parent directories first, then alphabetical.
	sort.Slice(dirs, func(i, j int) bool {
		if dirs[i] == "." {
			return true
		}
		if dirs[j] == "." {
			return false
		}
		di, dj := strings.Count(dirs[i], "/"), strings.Count(dirs[j], "/")
		if di != dj {
			return di < dj
		}
		return dirs[i] < dirs[j]
	})

	used := 0
	var out []GuidanceSnippet
	for _, d := range dirs {
		for _, fname := range []string{"AGENTS.md", "CLAUDE.md"} {
			rel := fname
			if d != "." {
				rel = d + "/" + fname
			}
			raw, err := os.ReadFile(filepath.Join(absRoot, filepath.FromSlash(rel)))
			if err != nil || len(raw) == 0 {
				continue
			}
			// Keep root concise when nested guidance is present.
			txt := strings.TrimSpace(string(raw))
			capChars := 1200
			if d != "." {
				capChars = 1600
			}
			if len(txt) > capChars {
				txt = txt[:capChars] + "\n... [truncated]"
			}
			toks := estimateTokens(txt)
			if maxTokens > 0 && used+toks > maxTokens {
				return out
			}
			used += toks
			out = append(out, GuidanceSnippet{Path: rel, Content: txt})
		}
	}
	return out
}

type skillBoostRule struct {
	effects   []string
	tokens    []string
	minUntest bool
	grants    map[string]float64
}

func computeSkillBoosts(items []CompiledItem, qTokens map[string]bool, untested []string) map[string]float64 {
	effectSet := map[string]bool{}
	for _, it := range items {
		for _, ef := range it.Effects {
			effectSet[ef] = true
		}
	}

	rules := []skillBoostRule{
		{effects: []string{"uses_locks", "spawns_async"}, tokens: []string{"race", "mutex", "concurrent"}, grants: map[string]float64{"concurrency-resource-safety": 4.0}},
		{effects: []string{"io_exec", "reads_env"}, tokens: []string{"webhook", "hmac", "auth", "workflow"}, grants: map[string]float64{"ci-and-supply-chain-hygiene": 3.5, "security-audit": 3.5}},
		{effects: []string{"returns_error", "io_http", "can_panic"}, tokens: []string{"error", "retry"}, grants: map[string]float64{"error-handling-and-observability": 3.0}},
		{tokens: []string{"test", "regression"}, minUntest: true, grants: map[string]float64{"tdd": 2.5, "property-differential-testing": 2.0}},
		{tokens: []string{"refactor", "rename", "interface", "split"}, grants: map[string]float64{"structural-refactoring": 3.5, "modular-design-and-boundaries": 3.0}},
		{tokens: []string{"bug", "fix", "crash", "fail"}, grants: map[string]float64{"systematic-debugging": 3.0, "fault-localization": 2.5}},
		{tokens: []string{"api", "openapi", "schema", "config"}, grants: map[string]float64{"safe-migrations-and-contracts": 3.5, "api-and-interface-ergonomics": 2.5}},
	}

	boosts := map[string]float64{
		"brownfield-convention-matching": 1.5,
		"code-hygiene":                   1.5,
		"verification-before-completion": 1.0,
	}
	for _, r := range rules {
		matched := (r.minUntest && len(untested) > 0) || hasAnyKey(effectSet, r.effects...) || hasAnyKey(qTokens, r.tokens...)
		if !matched {
			continue
		}
		for skill, delta := range r.grants {
			boosts[skill] += delta
		}
	}
	return boosts
}

func hasAnyKey(m map[string]bool, keys ...string) bool {
	for _, k := range keys {
		if m[k] {
			return true
		}
	}
	return false
}

func rankSkills(skills []SkillHint, qTokens map[string]bool, items []CompiledItem, untested []string) []string {
	if len(skills) == 0 {
		return nil
	}
	boosts := computeSkillBoosts(items, qTokens, untested)

	type scored struct {
		name  string
		score float64
	}
	list := make([]scored, 0, len(skills))
	for _, sk := range skills {
		sc := boosts[sk.Name]
		seenSk := map[string]bool{}
		for _, tok := range splitIdentifiers(sk.Name + " " + sk.Description) {
			if !seenSk[tok] && qTokens[tok] {
				sc += 1.2
			}
			seenSk[tok] = true
		}
		if sc > 0 {
			list = append(list, scored{name: sk.Name, score: sc})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].name < list[j].name
	})
	limit := min(5, len(list))
	out := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, list[i].name)
	}
	return out
}

func splitIdentifiers(s string) []string {
	var tokens []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() >= 2 {
			tok := normalizeToken(strings.ToLower(cur.String()))
			if !isStopToken(tok) && len(tok) >= 2 {
				tokens = append(tokens, tok)
			}
		}
		cur.Reset()
	}
	runes := []rune(s)
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		prevLower := i > 0 && unicode.IsLower(runes[i-1])
		nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
		if cur.Len() > 0 && unicode.IsUpper(r) && (prevLower || (cur.Len() > 1 && nextLower)) {
			flush()
		}
		cur.WriteRune(r)
	}
	flush()
	return tokens
}

func normalizeToken(tok string) string {
	if len(tok) >= 5 && strings.HasSuffix(tok, "s") &&
		!strings.HasSuffix(tok, "ss") && !strings.HasSuffix(tok, "us") && !strings.HasSuffix(tok, "is") {
		return tok[:len(tok)-1]
	}
	return tok
}

func isStopToken(tok string) bool {
	switch tok {
	case "and", "the", "for", "with", "from", "into", "that", "this", "when", "use", "all", "not", "in", "to", "of", "on", "or", "by", "as", "is", "be", "an", "are", "via", "whens":
		return true
	}
	return false
}

func containsStr(slice []string, want string) bool {
	for _, s := range slice {
		if s == want {
			return true
		}
	}
	return false
}

// ParseLineNumber extracts an optional file:line suffix when present in rawRef.
func ParseLineNumber(rawRef string) (string, int) {
	if idx := strings.LastIndex(rawRef, ":"); idx > 0 {
		if ln, err := strconv.Atoi(rawRef[idx+1:]); err == nil && ln > 0 {
			return rawRef[:idx], ln
		}
	}
	return rawRef, 0
}
