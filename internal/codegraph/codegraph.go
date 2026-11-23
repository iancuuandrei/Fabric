package codegraph

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// ProvenanceExtracted marks edges derived deterministically from AST/syntax/imports.
	ProvenanceExtracted = "EXTRACTED"
	// ProvenanceInferred marks heuristic or cross-reference edges (e.g. symbol name mentions, doc links).
	ProvenanceInferred = "INFERRED"

	maxIndexedFiles = 2000
	maxFileBytes    = 512 << 10
)

// Node represents one repository entity (file, symbol, type, test, or doc artifact).
type Node struct {
	ID         string      `json:"id"`
	Kind       string      `json:"kind"` // file, func, type, method, test, doc
	Name       string      `json:"name"`
	Path       string      `json:"path"`
	Line       int         `json:"line,omitempty"`
	Language   string      `json:"language,omitempty"`
	Cluster    string      `json:"cluster,omitempty"`
	Centrality float64     `json:"centrality,omitempty"`
	InDegree   int         `json:"in_degree,omitempty"`
	OutDegree  int         `json:"out_degree,omitempty"`
	Spec       *SymbolSpec `json:"spec,omitempty"`
}

// Edge represents a typed directed relationship between two graph nodes.
type Edge struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Relation   string `json:"relation"`   // DEFINES, IMPORTS, CALLS, USES_TYPE, TESTS, REFERENCES, DOCUMENTS
	Provenance string `json:"provenance"` // EXTRACTED, INFERRED
}

// Graph is the complete local Code Knowledge Graph for a repository snapshot.
type Graph struct {
	Root        string    `json:"root"`
	GeneratedAt time.Time `json:"generated_at"`
	FilesCount  int       `json:"files_count"`
	Nodes       []Node    `json:"nodes"`
	Edges       []Edge    `json:"edges"`
	Clusters    []Cluster `json:"clusters"`
	TopHubs     []Node    `json:"top_hubs"`
}

// Cluster groups tightly related files/symbols by directory/package boundary.
type Cluster struct {
	Name      string `json:"name"`
	FileCount int    `json:"file_count"`
	NodeCount int    `json:"node_count"`
}

// ImpactReport summarizes multi-hop blast radius around a target file or symbol.
type ImpactReport struct {
	Target     string   `json:"target"`
	MatchedIDs []string `json:"matched_ids"`
	Depth      int      `json:"depth"`
	Callers    []Node   `json:"callers"`
	Callees    []Node   `json:"callees"`
	Types      []Node   `json:"types,omitempty"`
	Tests      []Node   `json:"tests,omitempty"`
	Docs       []Node   `json:"docs,omitempty"`
}

var (
	tsFuncRe  = regexp.MustCompile(`^(?:export\s+(?:default\s+)?)?(?:async\s+)?(?:function\s+([A-Za-z_]\w*)|(?:class|interface|type)\s+([A-Za-z_]\w*)|const\s+([A-Za-z_]\w*)\s*=\s*(?:async\s*)?\()`)
	pyDefRe   = regexp.MustCompile(`^(\s*)(?:async\s+)?(?:def|class)\s+([A-Za-z_]\w*)`)
	rsDefRe   = regexp.MustCompile(`^(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?(?:fn|struct|enum|trait|impl)\s+([A-Za-z_]\w*)`)
	jvmDefRe  = regexp.MustCompile(`^(?:(?:public|private|protected|internal|static|final|abstract|async|override|virtual|sealed)\s+)*(?:class|interface|enum|record|struct)?\s*(?:[A-Za-z0-9_<>,\[\].]+\s+)?([A-Za-z_]\w*)\s*(?:\(|[{:])`)
	tsImpRe   = regexp.MustCompile(`(?m)(?:import\s+.*?from\s+|require\()['"]([^'"]+)['"]`)
	pyImpRe   = regexp.MustCompile(`(?m)^(?:from\s+([\w.]+)\s+import|import\s+([\w.]+))`)
	callTokRe = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]{1,63})\s*\(`)
	mdCodeRef = regexp.MustCompile("`([A-Za-z0-9_./-]+(?:\\.[a-z]{1,4}|\\(\\))?)`")
)

type callRef struct {
	qualifier string // optional package/receiver qualifier
	name      string
}

type graphBuildState struct {
	nodeMap       map[string]Node
	edgeSet       map[Edge]struct{}
	fileBySlash   map[string]string
	pkgFiles      map[string][]string
	fileCluster   map[string]string
	fileImports   map[string][]string
	symByPkgName  map[string]string   // pkgSlash + "::" + symName -> symID
	typeByPkgName map[string]string   // pkgSlash + "::" + typeName -> typeID
	symOwners     map[string][]string // symbolName -> []symID
	funcCalls     map[string][]callRef
	funcTypes     map[string][]string
	docContents   map[string]string
	goModule      string
}

func newGraphBuildState(goModule string) *graphBuildState {
	return &graphBuildState{
		nodeMap:       map[string]Node{},
		edgeSet:       map[Edge]struct{}{},
		fileBySlash:   map[string]string{},
		pkgFiles:      map[string][]string{},
		fileCluster:   map[string]string{},
		fileImports:   map[string][]string{},
		symByPkgName:  map[string]string{},
		typeByPkgName: map[string]string{},
		symOwners:     map[string][]string{},
		funcCalls:     map[string][]callRef{},
		funcTypes:     map[string][]string{},
		docContents:   map[string]string{},
		goModule:      goModule,
	}
}

func (s *graphBuildState) addNode(n Node) {
	if _, ok := s.nodeMap[n.ID]; !ok {
		s.nodeMap[n.ID] = n
	}
}

func (s *graphBuildState) addEdge(from, to, rel, prov string) {
	if from == "" || to == "" || from == to {
		return
	}
	s.edgeSet[Edge{From: from, To: to, Relation: rel, Provenance: prov}] = struct{}{}
}

// Build scans repoRoot deterministically (skipping .git, node_modules, vendor,
// target, .harness) and constructs a function-level Code Knowledge Graph with
// Javadoc-style SymbolSpecs, PageRank centrality, clusters, and doc-to-code edges.
func Build(repoRoot string) (*Graph, error) {
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, err
	}
	st := newGraphBuildState(readGoModulePath(filepath.Join(absRoot, "go.mod")))
	fileCount := 0

	_ = filepath.WalkDir(absRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if fileCount >= maxIndexedFiles {
			return fs.SkipAll
		}
		if indexed := st.processFile(absRoot, p, d); indexed {
			fileCount++
		}
		return nil
	})

	fileResolvedPkg := st.resolveImports()
	st.resolveFunctionEdges(fileResolvedPkg)
	st.resolveDocEdges()

	g := &Graph{
		Root:        absRoot,
		GeneratedAt: time.Now().UTC(),
		FilesCount:  fileCount,
	}
	g.finalize(st.nodeMap, st.edgeSet)
	return g, nil
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".harness", ".fabric", "node_modules", "target", "vendor", "dist", "build", ".next", "__pycache__", ".venv", "venv":
		return true
	}
	return false
}

func (s *graphBuildState) processFile(absRoot, fullPath string, d fs.DirEntry) bool {
	info, err := d.Info()
	if err != nil || info.Size() == 0 || info.Size() > maxFileBytes {
		return false
	}
	rel, err := filepath.Rel(absRoot, fullPath)
	if err != nil {
		return false
	}
	slash := filepath.ToSlash(rel)
	lang := detectLang(strings.ToLower(filepath.Ext(d.Name())))
	if lang == "" {
		return false
	}
	raw, err := os.ReadFile(fullPath)
	if err != nil || bytes.IndexByte(raw, 0) >= 0 {
		return false
	}

	cluster := filepath.ToSlash(filepath.Dir(rel))
	if cluster == "." {
		cluster = "root"
	}
	kind := "file"
	if lang == "markdown" {
		kind = "doc"
	}
	fileID := kind + ":" + slash
	s.addNode(Node{
		ID:       fileID,
		Kind:     kind,
		Name:     filepath.Base(slash),
		Path:     slash,
		Language: lang,
		Cluster:  cluster,
	})
	s.fileBySlash[slash] = fileID
	s.pkgFiles[cluster] = append(s.pkgFiles[cluster], fileID)
	s.fileCluster[fileID] = cluster

	text := string(raw)
	if lang == "markdown" {
		s.docContents[fileID] = text
		return true
	}
	s.extractSource(slash, fileID, cluster, lang, raw, text)
	return true
}

func (s *graphBuildState) extractSource(slash, fileID, cluster, lang string, raw []byte, text string) {
	switch lang {
	case "go":
		s.extractGoAST(slash, fileID, cluster, raw)
	case "typescript", "javascript":
		s.extractScriptFile(slash, fileID, cluster, lang, text, tsFuncRe)
		for _, m := range tsImpRe.FindAllStringSubmatch(text, -1) {
			if len(m) > 1 && strings.HasPrefix(m[1], ".") {
				resolved := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(slash), m[1])))
				s.fileImports[fileID] = append(s.fileImports[fileID], resolved)
			}
		}
	case "python":
		s.extractPythonFile(slash, fileID, cluster, text)
		for _, m := range pyImpRe.FindAllStringSubmatch(text, -1) {
			imp := firstStr(m[1], m[2])
			if imp != "" {
				s.fileImports[fileID] = append(s.fileImports[fileID], strings.ReplaceAll(imp, ".", "/"))
			}
		}
	case "rust":
		s.extractScriptFile(slash, fileID, cluster, lang, text, rsDefRe)
	case "java", "csharp":
		s.extractScriptFile(slash, fileID, cluster, lang, text, jvmDefRe)
	}
}

func (s *graphBuildState) resolveImports() map[string][]string {
	resolvedByFile := map[string][]string{}
	for fromFile, imps := range s.fileImports {
		for _, imp := range imps {
			relPkg := imp
			if s.goModule != "" && strings.HasPrefix(imp, s.goModule+"/") {
				relPkg = strings.TrimPrefix(imp, s.goModule+"/")
			}
			if targets, ok := s.pkgFiles[relPkg]; ok {
				resolvedByFile[fromFile] = append(resolvedByFile[fromFile], relPkg)
				for _, tf := range targets {
					if !strings.HasSuffix(tf, "_test.go") {
						s.addEdge(fromFile, tf, "IMPORTS", ProvenanceExtracted)
					}
				}
				continue
			}
			for _, ext := range []string{"", ".ts", ".tsx", ".js", ".jsx", ".py", "/index.ts", "/index.js"} {
				if tf, ok := s.fileBySlash[relPkg+ext]; ok {
					s.addEdge(fromFile, tf, "IMPORTS", ProvenanceExtracted)
					if cl, hasCl := s.fileCluster[tf]; hasCl {
						resolvedByFile[fromFile] = append(resolvedByFile[fromFile], cl)
					}
					break
				}
			}
		}
	}
	return resolvedByFile
}

func (s *graphBuildState) resolveFunctionEdges(fileResolvedPkg map[string][]string) {
	uniqueSyms := map[string]string{}
	for name, ids := range s.symOwners {
		if len(ids) == 1 && len(name) >= 4 {
			uniqueSyms[name] = ids[0]
		}
	}
	symOwnerFile := map[string]string{}
	for id, n := range s.nodeMap {
		if n.Kind != "file" && n.Kind != "doc" {
			symOwnerFile[id] = "file:" + n.Path
		}
	}
	s.resolveCallEdges(symOwnerFile, fileResolvedPkg, uniqueSyms)
	s.resolveTypeUsageEdges(symOwnerFile, fileResolvedPkg)
	s.resolveSeeAlsoEdges(uniqueSyms)
}

func (s *graphBuildState) resolveCallEdges(symOwnerFile map[string]string, fileResolvedPkg map[string][]string, uniqueSyms map[string]string) {
	for symID, calls := range s.funcCalls {
		fromFile := symOwnerFile[symID]
		fromPkg := s.fileCluster[fromFile]
		callerNode := s.nodeMap[symID]
		for _, ref := range calls {
			targetID, prov := s.matchSymbolTarget(fromPkg, fileResolvedPkg[fromFile], uniqueSyms, ref)
			if targetID == "" || targetID == symID {
				continue
			}
			s.addEdge(symID, targetID, "CALLS", prov)
			if callerNode.Kind == "test" {
				s.addEdge(symID, targetID, "TESTS", prov)
			}
		}
	}
}

func (s *graphBuildState) resolveTypeUsageEdges(symOwnerFile map[string]string, fileResolvedPkg map[string][]string) {
	for symID, types := range s.funcTypes {
		fromFile := symOwnerFile[symID]
		fromPkg := s.fileCluster[fromFile]
		for _, tname := range types {
			if targetID, ok := s.matchTypeTarget(fromPkg, fileResolvedPkg[fromFile], tname); ok {
				s.addEdge(symID, targetID, "USES_TYPE", ProvenanceExtracted)
			}
		}
	}
}

func (s *graphBuildState) resolveSeeAlsoEdges(uniqueSyms map[string]string) {
	for id, n := range s.nodeMap {
		if n.Spec == nil || len(n.Spec.SeeAlso) == 0 {
			continue
		}
		for _, see := range n.Spec.SeeAlso {
			clean := strings.TrimPrefix(see, "#")
			if targetID, ok := uniqueSyms[clean]; ok {
				s.addEdge(id, targetID, "REFERENCES", ProvenanceInferred)
			} else if targetID, ok := s.fileBySlash[see]; ok {
				s.addEdge(id, targetID, "REFERENCES", ProvenanceInferred)
			}
		}
	}
}

func (s *graphBuildState) matchSymbolTarget(fromPkg string, importedPkgs []string, uniqueSyms map[string]string, ref callRef) (string, string) {
	if ref.qualifier == "" {
		if targetID, ok := s.symByPkgName[fromPkg+"::"+ref.name]; ok {
			return targetID, ProvenanceExtracted
		}
	}
	for _, pkg := range importedPkgs {
		if ref.qualifier == "" || strings.EqualFold(filepath.Base(pkg), ref.qualifier) {
			if targetID, ok := s.symByPkgName[pkg+"::"+ref.name]; ok {
				return targetID, ProvenanceExtracted
			}
		}
	}
	if len(ref.name) >= 5 {
		if targetID, ok := uniqueSyms[ref.name]; ok {
			return targetID, ProvenanceInferred
		}
	}
	return "", ""
}

func (s *graphBuildState) matchTypeTarget(fromPkg string, importedPkgs []string, typeName string) (string, bool) {
	if targetID, ok := s.typeByPkgName[fromPkg+"::"+typeName]; ok {
		return targetID, true
	}
	for _, pkg := range importedPkgs {
		if targetID, ok := s.typeByPkgName[pkg+"::"+typeName]; ok {
			return targetID, true
		}
	}
	return "", false
}

func (s *graphBuildState) resolveDocEdges() {
	uniqueByName := map[string]string{}
	for name, ids := range s.symOwners {
		if len(ids) == 1 && len(name) >= 5 {
			uniqueByName[name] = ids[0]
		}
	}
	for docID, text := range s.docContents {
		for _, m := range mdCodeRef.FindAllStringSubmatch(text, -1) {
			if len(m) < 2 {
				continue
			}
			rawRef := strings.TrimSuffix(m[1], "()")
			ref := filepath.ToSlash(strings.TrimPrefix(rawRef, "./"))
			if targetID, ok := s.fileBySlash[ref]; ok {
				s.addEdge(docID, targetID, "DOCUMENTS", ProvenanceInferred)
			} else if targetID, ok := uniqueByName[rawRef]; ok {
				s.addEdge(docID, targetID, "DOCUMENTS", ProvenanceInferred)
			}
		}
	}
}

func (g *Graph) finalize(nodeMap map[string]Node, edgeSet map[Edge]struct{}) {
	edges := make([]Edge, 0, len(edgeSet))
	inAdj := map[string][]string{}
	outDeg := map[string]int{}
	inDeg := map[string]int{}
	for e := range edgeSet {
		if _, ok1 := nodeMap[e.From]; !ok1 {
			continue
		}
		if _, ok2 := nodeMap[e.To]; !ok2 {
			continue
		}
		edges = append(edges, e)
		inAdj[e.To] = append(inAdj[e.To], e.From)
		outDeg[e.From]++
		inDeg[e.To]++
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		return edges[i].Relation < edges[j].Relation
	})

	rank := computeUniformPageRank(nodeMap, inAdj, outDeg)
	clusterStats := map[string]*Cluster{}
	nodes := make([]Node, 0, len(nodeMap))
	for id, n := range nodeMap {
		n.Centrality = math.Round(rank[id]*10000) / 10000
		n.InDegree = inDeg[id]
		n.OutDegree = outDeg[id]
		nodes = append(nodes, n)
		cs := clusterStats[n.Cluster]
		if cs == nil {
			cs = &Cluster{Name: n.Cluster}
			clusterStats[n.Cluster] = cs
		}
		cs.NodeCount++
		if n.Kind == "file" || n.Kind == "doc" {
			cs.FileCount++
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	clusters := make([]Cluster, 0, len(clusterStats))
	for _, c := range clusterStats {
		clusters = append(clusters, *c)
	}
	sort.Slice(clusters, func(i, j int) bool {
		if clusters[i].FileCount != clusters[j].FileCount {
			return clusters[i].FileCount > clusters[j].FileCount
		}
		return clusters[i].Name < clusters[j].Name
	})

	hubs := selectTopHubs(nodes, 15)
	g.Nodes = nodes
	g.Edges = edges
	g.Clusters = clusters
	g.TopHubs = hubs
}

func computeUniformPageRank(nodeMap map[string]Node, inAdj map[string][]string, outDeg map[string]int) map[string]float64 {
	nCount := len(nodeMap)
	rank := make(map[string]float64, nCount)
	if nCount == 0 {
		return rank
	}
	initVal := 1.0 / float64(nCount)
	for id := range nodeMap {
		rank[id] = initVal
	}
	const d = 0.85
	for iter := 0; iter < 15; iter++ {
		next := make(map[string]float64, nCount)
		base := (1.0 - d) / float64(nCount)
		for id := range nodeMap {
			sum := 0.0
			for _, src := range inAdj[id] {
				if deg := outDeg[src]; deg > 0 {
					sum += rank[src] / float64(deg)
				}
			}
			next[id] = base + d*sum
		}
		rank = next
	}
	return rank
}

func selectTopHubs(nodes []Node, limit int) []Node {
	hubs := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if n.Kind == "file" || n.InDegree > 0 {
			hubs = append(hubs, n)
		}
	}
	sort.Slice(hubs, func(i, j int) bool {
		if hubs[i].Centrality != hubs[j].Centrality {
			return hubs[i].Centrality > hubs[j].Centrality
		}
		if hubs[i].InDegree != hubs[j].InDegree {
			return hubs[i].InDegree > hubs[j].InDegree
		}
		return hubs[i].ID < hubs[j].ID
	})
	if len(hubs) > limit {
		hubs = hubs[:limit]
	}
	return hubs
}

// Impact computes the multi-hop blast radius (upstream callers, downstream
// callees/imports, referenced types, covering tests, and linked docs) for any
// file path or symbol substring.
func (g *Graph) Impact(target string, maxDepth int) ImpactReport {
	if maxDepth <= 0 {
		maxDepth = 2
	}
	if maxDepth > 4 {
		maxDepth = 4
	}
	q := strings.ToLower(strings.TrimSpace(target))
	nodeByID := make(map[string]Node, len(g.Nodes))
	for _, n := range g.Nodes {
		nodeByID[n.ID] = n
	}

	var matched []string
	for _, n := range g.Nodes {
		if q != "" && (strings.EqualFold(n.ID, q) || strings.EqualFold(n.Path, q) || strings.EqualFold(n.Name, q) || strings.Contains(strings.ToLower(n.Path), q) || strings.Contains(strings.ToLower(n.Name), q)) {
			matched = append(matched, n.ID)
		}
	}
	if len(matched) > 12 {
		matched = matched[:12]
	}

	outAdj := map[string][]Edge{}
	inAdj := map[string][]Edge{}
	for _, e := range g.Edges {
		outAdj[e.From] = append(outAdj[e.From], e)
		inAdj[e.To] = append(inAdj[e.To], e)
	}

	callerIDs := bfsGraph(inAdj, matched, maxDepth, true)
	calleeIDs := bfsGraph(outAdj, matched, maxDepth, false)

	rep := ImpactReport{Target: target, MatchedIDs: matched, Depth: maxDepth}
	for id := range callerIDs {
		n := nodeByID[id]
		switch n.Kind {
		case "doc":
			rep.Docs = append(rep.Docs, n)
		case "test":
			rep.Tests = append(rep.Tests, n)
		default:
			rep.Callers = append(rep.Callers, n)
		}
	}
	for id := range calleeIDs {
		n := nodeByID[id]
		switch n.Kind {
		case "doc":
			rep.Docs = append(rep.Docs, n)
		case "type":
			rep.Types = append(rep.Types, n)
		default:
			rep.Callees = append(rep.Callees, n)
		}
	}
	sortAndCapNodes(&rep.Callers, 30)
	sortAndCapNodes(&rep.Callees, 30)
	sortAndCapNodes(&rep.Types, 20)
	sortAndCapNodes(&rep.Tests, 20)
	sortAndCapNodes(&rep.Docs, 20)
	return rep
}

func bfsGraph(adj map[string][]Edge, matched []string, maxDepth int, reverse bool) map[string]bool {
	visited := map[string]bool{}
	frontier := append([]string(nil), matched...)
	for _, id := range frontier {
		visited[id] = true
	}
	for d := 0; d < maxDepth && len(frontier) > 0; d++ {
		var next []string
		for _, cur := range frontier {
			for _, e := range adj[cur] {
				nbr := e.To
				if reverse {
					nbr = e.From
				}
				if !visited[nbr] {
					visited[nbr] = true
					next = append(next, nbr)
				}
			}
		}
		frontier = next
	}
	for _, id := range matched {
		delete(visited, id)
	}
	return visited
}

func sortAndCapNodes(slice *[]Node, capLen int) {
	s := *slice
	sort.Slice(s, func(i, j int) bool {
		if s[i].Centrality != s[j].Centrality {
			return s[i].Centrality > s[j].Centrality
		}
		return s[i].ID < s[j].ID
	})
	if len(s) > capLen {
		s = s[:capLen]
	}
	*slice = s
}

// ReportMarkdown renders a compact, LLM-ready GRAPH_REPORT.md summary (top hub
// nodes, module clusters, and symbol outlines) for tiered context engineering.
func (g *Graph) ReportMarkdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Fabric Code Knowledge Graph Report\n\n")
	fmt.Fprintf(&b, "- **Files indexed:** %d · **Nodes:** %d · **Edges:** %d · **Clusters:** %d\n\n",
		g.FilesCount, len(g.Nodes), len(g.Edges), len(g.Clusters))

	if len(g.TopHubs) > 0 {
		b.WriteString("## Top Hub Nodes (Highest Structural Centrality)\n\n")
		b.WriteString("| Node | Kind | Path | In | Out | Centrality |\n| --- | --- | --- | ---: | ---: | ---: |\n")
		for _, h := range g.TopHubs {
			fmt.Fprintf(&b, "| `%s` | %s | `%s` | %d | %d | %.4f |\n", h.Name, h.Kind, h.Path, h.InDegree, h.OutDegree, h.Centrality)
		}
		b.WriteString("\n")
	}

	if len(g.Clusters) > 0 {
		b.WriteString("## Top Module Clusters\n\n")
		limit := min(15, len(g.Clusters))
		for _, c := range g.Clusters[:limit] {
			fmt.Fprintf(&b, "- `%s` — %d files, %d symbols/nodes\n", c.Name, c.FileCount, c.NodeCount)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// RepoMap renders a concise Aider/Graphify-style structural map capped at maxChars.
func (g *Graph) RepoMap(maxChars int) string {
	if maxChars <= 0 {
		maxChars = 6000
	}
	var b strings.Builder
	b.WriteString("Repository Structural Map (top hub files & symbols):\n")
	symsByFile := map[string][]string{}
	for _, n := range g.Nodes {
		if n.Kind != "file" && n.Kind != "doc" {
			symsByFile[n.Path] = append(symsByFile[n.Path], fmt.Sprintf("%s:%d", n.Name, n.Line))
		}
	}
	seen := map[string]bool{}
	for _, h := range g.TopHubs {
		if seen[h.Path] {
			continue
		}
		seen[h.Path] = true
		syms := symsByFile[h.Path]
		if len(syms) > 8 {
			syms = syms[:8]
		}
		line := fmt.Sprintf("- %s (in=%d, out=%d)", h.Path, h.InDegree, h.OutDegree)
		if len(syms) > 0 {
			line += " -> " + strings.Join(syms, ", ")
		}
		line += "\n"
		if b.Len()+len(line) > maxChars {
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

// Save persists graph.json and GRAPH_REPORT.md under <repoRoot>/.harness/graph/.
func (g *Graph) Save(repoRoot string) (string, string, error) {
	dir := filepath.Join(repoRoot, ".harness", "graph")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	jsonPath := filepath.Join(dir, "graph.json")
	mdPath := filepath.Join(dir, "GRAPH_REPORT.md")
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(jsonPath, b, 0o600); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(mdPath, []byte(g.ReportMarkdown()), 0o600); err != nil {
		return "", "", err
	}
	return jsonPath, mdPath, nil
}

func (s *graphBuildState) extractGoAST(slash, fileID, cluster string, raw []byte) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, slash, raw, parser.ParseComments)
	if err != nil {
		return
	}
	isTestFile := strings.HasSuffix(slash, "_test.go")
	for _, imp := range f.Imports {
		s.fileImports[fileID] = append(s.fileImports[fileID], strings.Trim(imp.Path.Value, `"`))
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			s.recordGoFuncDecl(fset, slash, fileID, cluster, isTestFile, d, raw)
		case *ast.GenDecl:
			s.recordGoGenDecl(fset, slash, fileID, cluster, d)
		}
	}
}

func (s *graphBuildState) recordGoFuncDecl(fset *token.FileSet, slash, fileID, cluster string, isTestFile bool, d *ast.FuncDecl, raw []byte) {
	name := d.Name.Name
	line := fset.Position(d.Pos()).Line
	endLine := fset.Position(d.End()).Line
	recv := formatGoReceiver(fset, d.Recv)
	kind := "func"
	if recv != "" {
		kind = "method"
	}
	isTest := isTestFile && isGoTestName(name)
	if isTest {
		kind = "test"
	}
	symID := fmt.Sprintf("sym:%s:%s:%d", slash, name, line)
	sig := formatGoFuncSignature(fset, d)

	var docText string
	if d.Doc != nil {
		docText = d.Doc.Text()
	}
	parsed := parseDocSpec(docText)
	calls, typeRefs, returnsErr, effects := inspectGoFuncSpec(fset, d, raw)
	if !isTest {
		s.symByPkgName[cluster+"::"+name] = symID
	}
	if ast.IsExported(name) && !isTest {
		s.symOwners[name] = append(s.symOwners[name], symID)
	}
	if returnsErr && len(parsed.Errors) == 0 {
		parsed.Errors = []string{"returns error"}
	}
	spec := &SymbolSpec{
		Signature:  sig,
		EndLine:    endLine,
		Receiver:   recv,
		Exported:   ast.IsExported(name),
		IsTest:     isTest,
		Summary:    parsed.Summary,
		Params:     parsed.Params,
		Returns:    parsed.Returns,
		Errors:     parsed.Errors,
		SeeAlso:    parsed.SeeAlso,
		Effects:    effects,
		Deprecated: parsed.Deprecated,
	}
	s.addNode(Node{ID: symID, Kind: kind, Name: name, Path: slash, Line: line, Language: "go", Cluster: cluster, Spec: spec})
	s.addEdge(fileID, symID, "DEFINES", ProvenanceExtracted)
	if len(calls) > 0 {
		s.funcCalls[symID] = calls
	}
	if len(typeRefs) > 0 {
		s.funcTypes[symID] = typeRefs
	}
}

func (s *graphBuildState) recordGoGenDecl(fset *token.FileSet, slash, fileID, cluster string, d *ast.GenDecl) {
	groupDoc := ""
	if d.Doc != nil {
		groupDoc = d.Doc.Text()
	}
	for _, spec := range d.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok {
			continue
		}
		name := ts.Name.Name
		line := fset.Position(ts.Pos()).Line
		endLine := fset.Position(ts.End()).Line
		symID := fmt.Sprintf("sym:%s:%s:%d", slash, name, line)
		docText := groupDoc
		if ts.Doc != nil {
			docText = ts.Doc.Text()
		}
		parsed := parseDocSpec(docText)
		symSpec := &SymbolSpec{
			Signature:  formatGoTypeSignature(fset, ts),
			EndLine:    endLine,
			Exported:   ast.IsExported(name),
			Summary:    parsed.Summary,
			SeeAlso:    parsed.SeeAlso,
			Deprecated: parsed.Deprecated,
		}
		s.addNode(Node{ID: symID, Kind: "type", Name: name, Path: slash, Line: line, Language: "go", Cluster: cluster, Spec: symSpec})
		s.addEdge(fileID, symID, "DEFINES", ProvenanceExtracted)
		s.typeByPkgName[cluster+"::"+name] = symID
		if ast.IsExported(name) {
			s.symOwners[name] = append(s.symOwners[name], symID)
		}
	}
}

func inspectGoFuncSpec(fset *token.FileSet, d *ast.FuncDecl, raw []byte) ([]callRef, []string, bool, []string) {
	returnsErr := funcReturnsError(d.Type)
	seenTypes := map[string]bool{}
	typeRefs := collectGoSignatureTypes(d, seenTypes)
	if d.Body == nil {
		return nil, typeRefs, returnsErr, detectBodyEffects("", returnsErr)
	}

	var calls []callRef
	seenCalls := map[string]bool{}
	effectSet := map[string]bool{}
	if returnsErr {
		effectSet["returns_error"] = true
	}
	ast.Inspect(d.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.GoStmt:
			effectSet["spawns_async"] = true
		case *ast.DeferStmt:
			effectSet["uses_defer"] = true
		case *ast.CompositeLit:
			appendUniqueASTTypes(node.Type, seenTypes, &typeRefs)
		case *ast.CallExpr:
			recordGoCallExpr(node, seenCalls, &calls, effectSet)
		}
		return true
	})

	bodySrc := sliceFuncBodySource(fset, d.Body, raw)
	for _, ef := range detectBodyEffects(bodySrc, returnsErr) {
		effectSet[ef] = true
	}
	return calls, typeRefs, returnsErr, sortedMapKeys(effectSet)
}

func collectGoSignatureTypes(d *ast.FuncDecl, seen map[string]bool) []string {
	var refs []string
	addList := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, fld := range fl.List {
			appendUniqueASTTypes(fld.Type, seen, &refs)
		}
	}
	addList(d.Recv)
	if d.Type != nil {
		addList(d.Type.Params)
		addList(d.Type.Results)
	}
	return refs
}

func appendUniqueASTTypes(expr ast.Expr, seen map[string]bool, dst *[]string) {
	for _, tn := range collectExprTypeNames(expr) {
		if !seen[tn] {
			seen[tn] = true
			*dst = append(*dst, tn)
		}
	}
}

func recordGoCallExpr(node *ast.CallExpr, seen map[string]bool, calls *[]callRef, effects map[string]bool) {
	ref := callRefFromExpr(node.Fun)
	if ref.name == "" || isGoBuiltin(ref.name) {
		return
	}
	key := ref.qualifier + "." + ref.name
	if !seen[key] {
		seen[key] = true
		*calls = append(*calls, ref)
	}
	if ref.name == "panic" {
		effects["can_panic"] = true
	}
}

func sliceFuncBodySource(fset *token.FileSet, body *ast.BlockStmt, raw []byte) string {
	startOff := fset.Position(body.Pos()).Offset
	endOff := fset.Position(body.End()).Offset
	if startOff >= 0 && endOff > startOff && endOff <= len(raw) {
		return string(raw[startOff:endOff])
	}
	return ""
}

func sortedMapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func formatGoReceiver(fset *token.FileSet, recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	return renderASTNode(fset, recv.List[0].Type)
}

func formatGoFuncSignature(fset *token.FileSet, d *ast.FuncDecl) string {
	clone := *d
	clone.Body = nil
	clone.Doc = nil
	s := strings.Join(strings.Fields(renderASTNode(fset, &clone)), " ")
	return truncateSummary(s, 220)
}

func formatGoTypeSignature(fset *token.FileSet, ts *ast.TypeSpec) string {
	switch t := ts.Type.(type) {
	case *ast.StructType:
		var fields []string
		if t.Fields != nil {
			for _, f := range t.Fields.List {
				ft := renderASTNode(fset, f.Type)
				if len(f.Names) == 0 {
					fields = append(fields, ft)
				} else {
					for _, nm := range f.Names {
						fields = append(fields, nm.Name+" "+ft)
					}
				}
				if len(fields) >= 5 {
					break
				}
			}
		}
		return truncateSummary(fmt.Sprintf("type %s struct { %s }", ts.Name.Name, strings.Join(fields, "; ")), 220)
	case *ast.InterfaceType:
		var meths []string
		if t.Methods != nil {
			for _, m := range t.Methods.List {
				if len(m.Names) > 0 {
					meths = append(meths, m.Names[0].Name)
				} else {
					meths = append(meths, renderASTNode(fset, m.Type))
				}
				if len(meths) >= 6 {
					break
				}
			}
		}
		return truncateSummary(fmt.Sprintf("type %s interface { %s }", ts.Name.Name, strings.Join(meths, "; ")), 220)
	default:
		return truncateSummary("type "+ts.Name.Name+" "+renderASTNode(fset, ts.Type), 200)
	}
}

func renderASTNode(fset *token.FileSet, node any) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, node); err != nil {
		return ""
	}
	return buf.String()
}

func funcReturnsError(ft *ast.FuncType) bool {
	if ft == nil || ft.Results == nil {
		return false
	}
	for _, r := range ft.Results.List {
		if id, ok := r.Type.(*ast.Ident); ok && id.Name == "error" {
			return true
		}
	}
	return false
}

func collectExprTypeNames(expr ast.Expr) []string {
	switch t := expr.(type) {
	case *ast.Ident:
		if ast.IsExported(t.Name) {
			return []string{t.Name}
		}
	case *ast.StarExpr:
		return collectExprTypeNames(t.X)
	case *ast.ArrayType:
		return collectExprTypeNames(t.Elt)
	case *ast.MapType:
		out := collectExprTypeNames(t.Key)
		return append(out, collectExprTypeNames(t.Value)...)
	case *ast.SelectorExpr:
		return []string{t.Sel.Name}
	}
	return nil
}

func callRefFromExpr(expr ast.Expr) callRef {
	switch fn := expr.(type) {
	case *ast.Ident:
		return callRef{name: fn.Name}
	case *ast.SelectorExpr:
		qual := ""
		if xid, ok := fn.X.(*ast.Ident); ok {
			qual = xid.Name
		}
		return callRef{qualifier: qual, name: fn.Sel.Name}
	}
	return callRef{}
}

func isGoBuiltin(name string) bool {
	switch name {
	case "len", "cap", "make", "new", "append", "copy", "close", "delete", "complex", "real", "imag", "clear", "min", "max", "print", "println", "recover":
		return true
	}
	return false
}

func isGoTestName(name string) bool {
	return strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Benchmark") || strings.HasPrefix(name, "Fuzz") || strings.HasPrefix(name, "Example")
}

func (s *graphBuildState) extractScriptFile(slash, fileID, cluster, lang, text string, re *regexp.Regexp) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	isTestFile := isTestFilePath(slash)
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		m := re.FindStringSubmatch(trimmed)
		if len(m) < 2 {
			continue
		}
		name := firstStr(m[1:]...)
		if name == "" || name == "if" || name == "for" || name == "while" || name == "switch" || name == "return" {
			continue
		}
		lineNo := i + 1
		endLine := scanBraceEndLine(lines, i)
		docText := collectPrecedingComments(lines, i)
		parsed := parseDocSpec(docText)
		kind := classifyScriptSymbolKind(trimmed, name, isTestFile)
		symID := fmt.Sprintf("sym:%s:%s:%d", slash, name, lineNo)
		body := strings.Join(lines[i:min(len(lines), endLine)], "\n")
		effects := detectBodyEffects(body, false)

		if kind != "test" {
			s.symByPkgName[cluster+"::"+name] = symID
			s.symOwners[name] = append(s.symOwners[name], symID)
		}
		if kind == "type" {
			s.typeByPkgName[cluster+"::"+name] = symID
		}
		s.addNode(Node{
			ID: symID, Kind: kind, Name: name, Path: slash, Line: lineNo, Language: lang, Cluster: cluster,
			Spec: &SymbolSpec{
				Signature:  truncateSummary(trimmed, 180),
				EndLine:    endLine,
				Exported:   strings.Contains(trimmed, "export ") || strings.Contains(trimmed, "pub ") || strings.Contains(trimmed, "public "),
				IsTest:     kind == "test",
				Summary:    parsed.Summary,
				Params:     parsed.Params,
				Returns:    parsed.Returns,
				Errors:     parsed.Errors,
				SeeAlso:    parsed.SeeAlso,
				Effects:    effects,
				Deprecated: parsed.Deprecated,
			},
		})
		s.addEdge(fileID, symID, "DEFINES", ProvenanceExtracted)
		s.recordBodyCallRefs(symID, name, body)
	}
}

func (s *graphBuildState) extractPythonFile(slash, fileID, cluster, text string) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	isTestFile := isTestFilePath(slash)
	for i := 0; i < len(lines); i++ {
		m := pyDefRe.FindStringSubmatch(lines[i])
		if len(m) < 3 {
			continue
		}
		indent := len(m[1])
		name := m[2]
		lineNo := i + 1
		endLine := scanPythonEndLine(lines, i, indent)
		docText := collectPythonDocstring(lines, i+1)
		parsed := parseDocSpec(docText)
		kind := "func"
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "class ") {
			kind = "type"
		} else if isTestFile && strings.HasPrefix(name, "test_") {
			kind = "test"
		}
		symID := fmt.Sprintf("sym:%s:%s:%d", slash, name, lineNo)
		body := strings.Join(lines[i:min(len(lines), endLine)], "\n")
		effects := detectBodyEffects(body, false)

		if kind != "test" {
			s.symByPkgName[cluster+"::"+name] = symID
			s.symOwners[name] = append(s.symOwners[name], symID)
		}
		if kind == "type" {
			s.typeByPkgName[cluster+"::"+name] = symID
		}
		s.addNode(Node{
			ID: symID, Kind: kind, Name: name, Path: slash, Line: lineNo, Language: "python", Cluster: cluster,
			Spec: &SymbolSpec{
				Signature: truncateSummary(trimmed, 180),
				EndLine:   endLine,
				Exported:  !strings.HasPrefix(name, "_"),
				IsTest:    kind == "test",
				Summary:   parsed.Summary,
				Params:    parsed.Params,
				Returns:   parsed.Returns,
				Errors:    parsed.Errors,
				SeeAlso:   parsed.SeeAlso,
				Effects:   effects,
			},
		})
		s.addEdge(fileID, symID, "DEFINES", ProvenanceExtracted)
		s.recordBodyCallRefs(symID, name, body)
	}
}

func (s *graphBuildState) recordBodyCallRefs(symID, selfName, body string) {
	var calls []callRef
	seen := map[string]bool{}
	for _, m := range callTokRe.FindAllStringSubmatch(body, -1) {
		cn := m[1]
		if cn == selfName || len(cn) < 4 || isCommonKeyword(cn) || seen[cn] {
			continue
		}
		seen[cn] = true
		calls = append(calls, callRef{name: cn})
	}
	if len(calls) > 0 {
		s.funcCalls[symID] = calls
	}
}

func isCommonKeyword(s string) bool {
	switch s {
	case "return", "switch", "typeof", "sizeof", "catch", "throw", "await", "super", "while", "print":
		return true
	}
	return false
}

func classifyScriptSymbolKind(trimmed, name string, isTestFile bool) string {
	if strings.Contains(trimmed, "class ") || strings.Contains(trimmed, "interface ") ||
		strings.Contains(trimmed, "type ") || strings.Contains(trimmed, "struct ") ||
		strings.Contains(trimmed, "enum ") || strings.Contains(trimmed, "trait ") ||
		strings.Contains(trimmed, "record ") {
		return "type"
	}
	if isTestFile && (strings.HasPrefix(strings.ToLower(name), "test") || name == "it") {
		return "test"
	}
	return "func"
}

func isTestFilePath(slash string) bool {
	base := strings.ToLower(filepath.Base(slash))
	return strings.Contains(base, "_test.") || strings.Contains(base, ".test.") ||
		strings.Contains(base, ".spec.") || strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "test.java")
}

func collectPrecedingComments(lines []string, idx int) string {
	var docLines []string
	inBlock := false
	for j := idx - 1; j >= 0 && idx-j <= 40; j-- {
		t := strings.TrimSpace(lines[j])
		if t == "" && !inBlock && len(docLines) == 0 {
			continue
		}
		if strings.HasSuffix(t, "*/") {
			inBlock = true
		}
		if inBlock {
			docLines = append([]string{t}, docLines...)
			if strings.HasPrefix(t, "/*") {
				break
			}
			continue
		}
		if strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#[") || strings.HasPrefix(t, "@") {
			if strings.HasPrefix(t, "//") {
				docLines = append([]string{t}, docLines...)
			}
			continue
		}
		break
	}
	return strings.Join(docLines, "\n")
}

func collectPythonDocstring(lines []string, startIdx int) string {
	if startIdx >= len(lines) {
		return ""
	}
	first := strings.TrimSpace(lines[startIdx])
	quote := ""
	switch {
	case strings.HasPrefix(first, `"""`):
		quote = `"""`
	case strings.HasPrefix(first, `'''`):
		quote = `'''`
	default:
		return ""
	}
	rest := strings.TrimPrefix(first, quote)
	if strings.Contains(rest, quote) {
		before, _, _ := strings.Cut(rest, quote)
		return before
	}
	var doc []string
	if rest != "" {
		doc = append(doc, rest)
	}
	for k := startIdx + 1; k < len(lines) && k-startIdx < 40; k++ {
		if idx := strings.Index(lines[k], quote); idx >= 0 {
			doc = append(doc, lines[k][:idx])
			break
		}
		doc = append(doc, lines[k])
	}
	return strings.Join(doc, "\n")
}

func scanBraceEndLine(lines []string, start int) int {
	depth := 0
	opened := false
	for i := start; i < len(lines) && i-start < 250; i++ {
		for _, ch := range lines[i] {
			if ch == '{' {
				depth++
				opened = true
			} else if ch == '}' && opened {
				depth--
				if depth <= 0 {
					return i + 1
				}
			}
		}
	}
	return min(len(lines), start+1)
}

func scanPythonEndLine(lines []string, start, defIndent int) int {
	last := start + 1
	for i := start + 1; i < len(lines) && i-start < 250; i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			continue
		}
		curIndent := len(lines[i]) - len(strings.TrimLeft(lines[i], " \t"))
		if curIndent <= defIndent {
			break
		}
		last = i + 1
	}
	return last
}

func detectLang(ext string) string {
	switch ext {
	case ".go":
		return "go"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx", ".mjs":
		return "javascript"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".java":
		return "java"
	case ".cs":
		return "csharp"
	case ".md":
		return "markdown"
	default:
		return ""
	}
}

func readGoModulePath(goModPath string) string {
	b, err := os.ReadFile(goModPath)
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}
