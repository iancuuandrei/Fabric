package codegraph

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
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

// Node represents one repository entity (file, symbol, or documentation artifact).
type Node struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"` // file, func, type, method, doc
	Name       string  `json:"name"`
	Path       string  `json:"path"`
	Line       int     `json:"line,omitempty"`
	Language   string  `json:"language,omitempty"`
	Cluster    string  `json:"cluster,omitempty"`
	Centrality float64 `json:"centrality,omitempty"`
	InDegree   int     `json:"in_degree,omitempty"`
	OutDegree  int     `json:"out_degree,omitempty"`
}

// Edge represents a typed directed relationship between two graph nodes.
type Edge struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Relation   string `json:"relation"`   // DEFINES, IMPORTS, CALLS, REFERENCES, DOCUMENTS
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
	Docs       []Node   `json:"docs,omitempty"`
}

var (
	tsFuncRe  = regexp.MustCompile(`(?m)^(?:export\s+)?(?:async\s+)?(?:function\s+([A-Za-z_]\w*)|(?:class|interface|type)\s+([A-Za-z_]\w*)|const\s+([A-Za-z_]\w*)\s*=\s*(?:async\s*)?\()`)
	pyDefRe   = regexp.MustCompile(`(?m)^(?:async\s+)?(?:def|class)\s+([A-Za-z_]\w*)`)
	rsDefRe   = regexp.MustCompile(`(?m)^(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?(?:fn|struct|enum|trait|impl)\s+([A-Za-z_]\w*)`)
	tsImpRe   = regexp.MustCompile(`(?m)(?:import\s+.*?from\s+|require\()['"]([^'"]+)['"]`)
	pyImpRe   = regexp.MustCompile(`(?m)^(?:from\s+([\w.]+)\s+import|import\s+([\w.]+))`)
	mdCodeRef = regexp.MustCompile("`([A-Za-z0-9_./-]+\\.[a-z]{1,4})`")
)

// Build scans repoRoot deterministically (skipping .git, node_modules, vendor,
// target, .harness) and constructs a local Code Knowledge Graph with PageRank
// centrality, clusters, and doc-to-code edges.
func Build(repoRoot string) (*Graph, error) {
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, err
	}
	g := &Graph{
		Root:        absRoot,
		GeneratedAt: time.Now().UTC(),
	}

	nodeMap := map[string]Node{}
	edgeSet := map[Edge]struct{}{}
	addNode := func(n Node) {
		if _, ok := nodeMap[n.ID]; !ok {
			nodeMap[n.ID] = n
		}
	}
	addEdge := func(from, to, rel, prov string) {
		if from == "" || to == "" || from == to {
			return
		}
		edgeSet[Edge{From: from, To: to, Relation: rel, Provenance: prov}] = struct{}{}
	}

	// Track known files & exported symbol definitions for cross-file edge resolution.
	fileBySlash := map[string]string{}  // slash path -> node ID
	pkgFiles := map[string][]string{}   // dir slash -> []fileNodeID
	symOwners := map[string][]string{}  // symbolName -> []symbolNodeID
	fileContents := map[string]string{} // fileNodeID -> text (for lightweight call/ref linking)
	docContents := map[string]string{}  // docNodeID -> text
	goImports := map[string][]string{}  // fileNodeID -> imported package paths

	// Detect go.mod module path for exact internal Go import linking.
	goModule := readGoModulePath(filepath.Join(absRoot, "go.mod"))

	fileCount := 0
	_ = filepath.WalkDir(absRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			switch name {
			case ".git", ".harness", ".fabric", "node_modules", "target", "vendor", "dist", "build", ".next", "__pycache__", ".venv", "venv":
				return filepath.SkipDir
			}
			return nil
		}
		if fileCount >= maxIndexedFiles {
			return fs.SkipAll
		}
		info, err := d.Info()
		if err != nil || info.Size() == 0 || info.Size() > maxFileBytes {
			return nil
		}
		rel, err := filepath.Rel(absRoot, p)
		if err != nil {
			return nil
		}
		slash := filepath.ToSlash(rel)
		ext := strings.ToLower(filepath.Ext(name))
		lang := detectLang(ext)
		if lang == "" {
			return nil
		}

		raw, err := os.ReadFile(p)
		if err != nil || bytes.IndexByte(raw, 0) >= 0 {
			return nil
		}
		text := string(raw)
		fileCount++

		cluster := filepath.ToSlash(filepath.Dir(rel))
		if cluster == "." {
			cluster = "root"
		}
		kind := "file"
		if lang == "markdown" {
			kind = "doc"
		}
		fileID := kind + ":" + slash
		addNode(Node{
			ID:       fileID,
			Kind:     kind,
			Name:     filepath.Base(slash),
			Path:     slash,
			Language: lang,
			Cluster:  cluster,
		})
		fileBySlash[slash] = fileID
		pkgFiles[cluster] = append(pkgFiles[cluster], fileID)

		if lang == "markdown" {
			docContents[fileID] = text
			return nil
		}

		fileContents[fileID] = text
		switch lang {
		case "go":
			extractGoAST(slash, fileID, cluster, raw, addNode, addEdge, symOwners, goImports)
		case "typescript", "javascript":
			extractRegexSymbols(slash, fileID, cluster, lang, text, tsFuncRe, addNode, addEdge, symOwners)
			for _, m := range tsImpRe.FindAllStringSubmatch(text, -1) {
				if len(m) > 1 && strings.HasPrefix(m[1], ".") {
					resolved := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(slash), m[1])))
					goImports[fileID] = append(goImports[fileID], resolved)
				}
			}
		case "python":
			extractRegexSymbols(slash, fileID, cluster, lang, text, pyDefRe, addNode, addEdge, symOwners)
			for _, m := range pyImpRe.FindAllStringSubmatch(text, -1) {
				imp := m[1]
				if imp == "" {
					imp = m[2]
				}
				if imp != "" {
					goImports[fileID] = append(goImports[fileID], strings.ReplaceAll(imp, ".", "/"))
				}
			}
		case "rust":
			extractRegexSymbols(slash, fileID, cluster, lang, text, rsDefRe, addNode, addEdge, symOwners)
		}
		return nil
	})

	// Link internal imports deterministically (EXTRACTED).
	for fromFile, imps := range goImports {
		for _, imp := range imps {
			relPkg := imp
			if goModule != "" && strings.HasPrefix(imp, goModule+"/") {
				relPkg = strings.TrimPrefix(imp, goModule+"/")
			}
			// Match target directory package files or exact file path with extension.
			if targets, ok := pkgFiles[relPkg]; ok {
				for _, tf := range targets {
					if !strings.HasSuffix(tf, "_test.go") {
						addEdge(fromFile, tf, "IMPORTS", ProvenanceExtracted)
					}
				}
				continue
			}
			for _, ext := range []string{"", ".ts", ".tsx", ".js", ".jsx", ".py", "/index.ts", "/index.js"} {
				if tf, ok := fileBySlash[relPkg+ext]; ok {
					addEdge(fromFile, tf, "IMPORTS", ProvenanceExtracted)
					break
				}
			}
		}
	}

	// Link cross-file unique symbol calls/references (INFERRED, bounded to non-trivial unique names).
	uniqueSyms := map[string]string{}
	for name, ids := range symOwners {
		if len(ids) == 1 && len(name) >= 5 {
			uniqueSyms[name] = ids[0]
		}
	}
	for fromFile, text := range fileContents {
		for symName, symID := range uniqueSyms {
			if strings.HasPrefix(symID, "sym:"+strings.TrimPrefix(fromFile, "file:")+":") {
				continue
			}
			if strings.Contains(text, symName+"(") {
				addEdge(fromFile, symID, "CALLS", ProvenanceInferred)
			}
		}
	}

	// Link Markdown docs to referenced source files (INFERRED doc-to-code bridge like Graphify).
	for docID, text := range docContents {
		for _, m := range mdCodeRef.FindAllStringSubmatch(text, -1) {
			if len(m) < 2 {
				continue
			}
			ref := filepath.ToSlash(strings.TrimPrefix(m[1], "./"))
			if targetID, ok := fileBySlash[ref]; ok {
				addEdge(docID, targetID, "DOCUMENTS", ProvenanceInferred)
			}
		}
	}

	// Materialize edges and compute degrees + PageRank centrality.
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

	// 15-iteration damped PageRank over all nodes.
	nCount := len(nodeMap)
	rank := make(map[string]float64, nCount)
	if nCount > 0 {
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
	}

	clusterStats := map[string]*Cluster{}
	nodes := make([]Node, 0, nCount)
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

	// Top hub nodes (God Nodes) sorted by PageRank centrality then in-degree.
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
	if len(hubs) > 15 {
		hubs = hubs[:15]
	}

	g.FilesCount = fileCount
	g.Nodes = nodes
	g.Edges = edges
	g.Clusters = clusters
	g.TopHubs = hubs
	return g, nil
}

// Impact computes the multi-hop blast radius (upstream callers, downstream
// callees/imports, and linked docs) for any file path or symbol substring.
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

	walk := func(adj map[string][]Edge, reverse bool) map[string]bool {
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

	callerIDs := walk(inAdj, true)
	calleeIDs := walk(outAdj, false)

	rep := ImpactReport{Target: target, MatchedIDs: matched, Depth: maxDepth}
	for id := range callerIDs {
		n := nodeByID[id]
		if n.Kind == "doc" {
			rep.Docs = append(rep.Docs, n)
		} else {
			rep.Callers = append(rep.Callers, n)
		}
	}
	for id := range calleeIDs {
		n := nodeByID[id]
		if n.Kind == "doc" {
			rep.Docs = append(rep.Docs, n)
		} else {
			rep.Callees = append(rep.Callees, n)
		}
	}
	sort.Slice(rep.Callers, func(i, j int) bool { return rep.Callers[i].Centrality > rep.Callers[j].Centrality })
	sort.Slice(rep.Callees, func(i, j int) bool { return rep.Callees[i].Centrality > rep.Callees[j].Centrality })
	if len(rep.Callers) > 30 {
		rep.Callers = rep.Callers[:30]
	}
	if len(rep.Callees) > 30 {
		rep.Callees = rep.Callees[:30]
	}
	return rep
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

func extractGoAST(slash, fileID, cluster string, raw []byte, addNode func(Node), addEdge func(string, string, string, string), symOwners map[string][]string, goImports map[string][]string) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, slash, raw, 0)
	if err != nil {
		return
	}
	for _, imp := range f.Imports {
		val := strings.Trim(imp.Path.Value, `"`)
		goImports[fileID] = append(goImports[fileID], val)
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			kind := "func"
			name := d.Name.Name
			if d.Recv != nil {
				kind = "method"
			}
			line := fset.Position(d.Pos()).Line
			symID := fmt.Sprintf("sym:%s:%s:%d", slash, name, line)
			addNode(Node{ID: symID, Kind: kind, Name: name, Path: slash, Line: line, Language: "go", Cluster: cluster})
			addEdge(fileID, symID, "DEFINES", ProvenanceExtracted)
			if ast.IsExported(name) {
				symOwners[name] = append(symOwners[name], symID)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					name := ts.Name.Name
					line := fset.Position(ts.Pos()).Line
					symID := fmt.Sprintf("sym:%s:%s:%d", slash, name, line)
					addNode(Node{ID: symID, Kind: "type", Name: name, Path: slash, Line: line, Language: "go", Cluster: cluster})
					addEdge(fileID, symID, "DEFINES", ProvenanceExtracted)
					if ast.IsExported(name) {
						symOwners[name] = append(symOwners[name], symID)
					}
				}
			}
		}
	}
}

func extractRegexSymbols(slash, fileID, cluster, lang, text string, re *regexp.Regexp, addNode func(Node), addEdge func(string, string, string, string), symOwners map[string][]string) {
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		m := re.FindStringSubmatch(sc.Text())
		if len(m) < 2 {
			continue
		}
		name := ""
		for _, grp := range m[1:] {
			if grp != "" {
				name = grp
				break
			}
		}
		if name == "" || name == "if" || name == "for" || name == "while" {
			continue
		}
		symID := fmt.Sprintf("sym:%s:%s:%d", slash, name, lineNo)
		addNode(Node{ID: symID, Kind: "func", Name: name, Path: slash, Line: lineNo, Language: lang, Cluster: cluster})
		addEdge(fileID, symID, "DEFINES", ProvenanceExtracted)
		symOwners[name] = append(symOwners[name], symID)
	}
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
