package codegraph

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ParamSpec describes one documented parameter extracted from a function or
// method docstring/Javadoc comment.
type ParamSpec struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	Desc string `json:"desc,omitempty"`
}

// SymbolSpec holds structured Javadoc/GoDoc/Python/Rust contract metadata and
// statically inferred behavioral effects for a symbol node.
type SymbolSpec struct {
	Signature  string      `json:"signature,omitempty"`
	EndLine    int         `json:"end_line,omitempty"`
	Receiver   string      `json:"receiver,omitempty"`
	Exported   bool        `json:"exported,omitempty"`
	IsTest     bool        `json:"is_test,omitempty"`
	Summary    string      `json:"summary,omitempty"`
	Params     []ParamSpec `json:"params,omitempty"`
	Returns    string      `json:"returns,omitempty"`
	Errors     []string    `json:"errors,omitempty"`
	SeeAlso    []string    `json:"see_also,omitempty"`
	Effects    []string    `json:"effects,omitempty"`
	Deprecated bool        `json:"deprecated,omitempty"`
}

type parsedDoc struct {
	Summary    string
	Params     []ParamSpec
	Returns    string
	Errors     []string
	SeeAlso    []string
	Deprecated bool
}

var (
	javadocParamRe  = regexp.MustCompile(`^@(?:param|arg|argument)\s+(?:\{([^}]+)\}\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*(?:[-–:]\s*)?(.*)$`)
	javadocReturnRe = regexp.MustCompile(`^@returns?\s+(?:\{([^}]+)\}\s+)?(?:[-–:]\s*)?(.*)$`)
	javadocThrowsRe = regexp.MustCompile(`^@(?:throws?|exception|errors?)\s+(?:\{([^}]+)\}\s+)?(.*)$`)
	javadocSeeRe    = regexp.MustCompile(`^@(?:see|link)\s+(.+)$`)
	sphinxParamRe   = regexp.MustCompile(`^:param(?:\s+([A-Za-z0-9_.[\]]+))?\s+([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(.*)$`)
	sphinxReturnRe  = regexp.MustCompile(`^:returns?\s*:\s*(.*)$`)
	sphinxRaisesRe  = regexp.MustCompile(`^:raises?\s+([A-Za-z0-9_.]+)\s*:\s*(.*)$`)
	pyNamedItemRe   = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*(?:\(([^)]+)\))?\s*:\s*(.*)$`)
	docInlineSeeRe  = regexp.MustCompile(`\{@link\s+([^}]+)\}|\[([^\]]+)\]`)
)

// parseDocSpec extracts structured Javadoc, GoDoc, Python (Google/Sphinx), and
// Rustdoc annotations cleanly from raw comment text.
func parseDocSpec(rawDoc string) parsedDoc {
	cleaned := cleanDocLines(rawDoc)
	if len(cleaned) == 0 {
		return parsedDoc{}
	}
	var (
		out         parsedDoc
		summaryLine []string
		section     string
	)
	for _, line := range cleaned {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(summaryLine) > 0 && section == "" {
				section = "body"
			}
			continue
		}
		if sec := detectDocSectionHeader(trimmed); sec != "" {
			section = sec
			continue
		}
		if consumeDocTag(trimmed, &out) {
			continue
		}
		if consumeDocSectionLine(section, trimmed, &out) {
			continue
		}
		if section == "" {
			summaryLine = append(summaryLine, trimmed)
		}
	}
	out.Summary = truncateSummary(firstSentence(strings.Join(summaryLine, " ")), 240)
	return out
}

func cleanDocLines(raw string) []string {
	norm := strings.ReplaceAll(strings.TrimSpace(raw), "\r\n", "\n")
	if norm == "" {
		return nil
	}
	lines := strings.Split(norm, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		s := strings.TrimSpace(line)
		s = strings.TrimPrefix(s, "/**")
		s = strings.TrimPrefix(s, "/*")
		s = strings.TrimSuffix(s, "*/")
		s = strings.TrimPrefix(s, `"""`)
		s = strings.TrimSuffix(s, `"""`)
		s = strings.TrimPrefix(s, `'''`)
		s = strings.TrimSuffix(s, `'''`)
		s = strings.TrimPrefix(s, "///")
		s = strings.TrimPrefix(s, "//!")
		s = strings.TrimPrefix(s, "//")
		s = strings.TrimPrefix(s, "*")
		out = append(out, strings.TrimSpace(s))
	}
	return out
}

func detectDocSectionHeader(line string) string {
	lower := strings.ToLower(strings.TrimSpace(line))
	switch lower {
	case "args:", "arguments:", "parameters:", "# arguments", "# parameters":
		return "args"
	case "returns:", "return:", "# returns":
		return "returns"
	case "raises:", "throws:", "errors:", "# errors", "# panics":
		return "errors"
	}
	if strings.HasPrefix(lower, "deprecated:") || lower == "@deprecated" {
		return "deprecated"
	}
	return ""
}

func consumeDocTag(line string, out *parsedDoc) bool {
	if m := javadocParamRe.FindStringSubmatch(line); len(m) == 4 {
		out.Params = append(out.Params, ParamSpec{Name: m[2], Type: strings.TrimSpace(m[1]), Desc: strings.TrimSpace(m[3])})
		return true
	}
	if m := sphinxParamRe.FindStringSubmatch(line); len(m) == 4 {
		out.Params = append(out.Params, ParamSpec{Name: m[2], Type: strings.TrimSpace(m[1]), Desc: strings.TrimSpace(m[3])})
		return true
	}
	if m := javadocReturnRe.FindStringSubmatch(line); len(m) == 3 {
		out.Returns = joinParts(m[1], m[2])
		return true
	}
	if m := sphinxReturnRe.FindStringSubmatch(line); len(m) == 2 {
		out.Returns = strings.TrimSpace(m[1])
		return true
	}
	if m := javadocThrowsRe.FindStringSubmatch(line); len(m) == 3 {
		out.Errors = appendUniqueString(out.Errors, joinParts(m[1], m[2]))
		return true
	}
	if m := sphinxRaisesRe.FindStringSubmatch(line); len(m) == 3 {
		out.Errors = appendUniqueString(out.Errors, joinParts(m[1], m[2]))
		return true
	}
	if m := javadocSeeRe.FindStringSubmatch(line); len(m) == 2 {
		for _, ref := range extractSeeRefs(m[1]) {
			out.SeeAlso = appendUniqueString(out.SeeAlso, ref)
		}
		return true
	}
	lower := strings.ToLower(line)
	if strings.HasPrefix(lower, "@deprecated") || strings.HasPrefix(lower, "deprecated:") {
		out.Deprecated = true
		return true
	}
	return false
}

func consumeDocSectionLine(section, line string, out *parsedDoc) bool {
	trimmed := strings.TrimSpace(strings.TrimLeft(line, "-*"))
	if trimmed == "" {
		return section != ""
	}
	switch section {
	case "args":
		if m := pyNamedItemRe.FindStringSubmatch(trimmed); len(m) == 4 {
			out.Params = append(out.Params, ParamSpec{Name: m[1], Type: strings.TrimSpace(m[2]), Desc: strings.TrimSpace(m[3])})
		}
		return true
	case "returns":
		if out.Returns == "" {
			out.Returns = trimmed
		} else {
			out.Returns += " " + trimmed
		}
		return true
	case "errors":
		out.Errors = appendUniqueString(out.Errors, trimmed)
		return true
	case "deprecated":
		out.Deprecated = true
		return true
	default:
		return false
	}
}

func detectBodyEffects(body string, returnsErr bool) []string {
	var effects []string
	if returnsErr {
		effects = append(effects, "returns_error")
	}
	rules := []struct {
		tag      string
		patterns []string
	}{
		{"can_panic", []string{"panic(", ".unwrap()", "unwrap_or_else", "raise "}},
		{"uses_defer", []string{"defer "}},
		{"spawns_async", []string{"go func", "\tgo ", "async fn", "async def", "asyncio.", "tokio::spawn", "Promise.", "new Thread"}},
		{"uses_locks", []string{"sync.Mutex", "sync.RWMutex", "sync.Map", ".Lock()", ".RLock()", "Mutex::", "RwLock::"}},
		{"io_http", []string{"net/http", "http.Get", "http.Post", "http.NewRequest", "fetch(", "axios.", "reqwest::", "requests."}},
		{"io_fs", []string{"os.ReadFile", "os.WriteFile", "os.Open", "os.Create", "os.MkdirAll", "os.Rename", "fs.Read", "std::fs::", "fs.promises"}},
		{"io_exec", []string{"exec.Command", "gitexec.Command", "subprocess.", "std::process::Command", "child_process"}},
		{"reads_env", []string{"os.Getenv", "os.LookupEnv", "process.env", "env::var", "os.environ"}},
	}
	for _, r := range rules {
		for _, pat := range r.patterns {
			if strings.Contains(body, pat) {
				effects = append(effects, r.tag)
				break
			}
		}
	}
	sort.Strings(effects)
	return effects
}

// SpecCard renders one symbol (or file) node as a concise Javadoc-style
// specification card with signature, parameters, return/error contract,
// behavioral effects, and linked callers/callees/types/tests/docs.
func (g *Graph) SpecCard(nodeID string) string {
	var target *Node
	for i := range g.Nodes {
		if g.Nodes[i].ID == nodeID {
			target = &g.Nodes[i]
			break
		}
	}
	if target == nil {
		return ""
	}
	var b strings.Builder
	loc := target.Path
	if target.Line > 0 {
		if target.Spec != nil && target.Spec.EndLine > target.Line {
			loc = fmt.Sprintf("%s:%d-%d", target.Path, target.Line, target.Spec.EndLine)
		} else {
			loc = fmt.Sprintf("%s:%d", target.Path, target.Line)
		}
	}
	headerSig := target.Name
	if target.Spec != nil && target.Spec.Signature != "" {
		headerSig = target.Spec.Signature
	}
	fmt.Fprintf(&b, "### `%s` (%s · `%s`)\n", headerSig, target.Kind, loc)

	if sp := target.Spec; sp != nil {
		if sp.Deprecated {
			b.WriteString("- **@deprecated**\n")
		}
		if sp.Summary != "" {
			fmt.Fprintf(&b, "- **Spec:** %s\n", sp.Summary)
		}
		for _, p := range sp.Params {
			if p.Type != "" {
				fmt.Fprintf(&b, "- **@param** `%s` (`%s`): %s\n", p.Name, p.Type, p.Desc)
			} else {
				fmt.Fprintf(&b, "- **@param** `%s`: %s\n", p.Name, p.Desc)
			}
		}
		if sp.Returns != "" {
			fmt.Fprintf(&b, "- **@returns:** %s\n", sp.Returns)
		}
		if len(sp.Errors) > 0 {
			fmt.Fprintf(&b, "- **@throws:** %s\n", strings.Join(sp.Errors, "; "))
		}
		if len(sp.Effects) > 0 {
			fmt.Fprintf(&b, "- **@effects:** `%s`\n", strings.Join(sp.Effects, "`, `"))
		}
	}

	g.appendSpecLinks(&b, target.ID)
	return b.String()
}

// SpecCardsForTarget finds up to limit nodes matching target (by ID, path, or
// symbol name) and returns rendered Javadoc spec cards ordered by centrality.
func (g *Graph) SpecCardsForTarget(target string, limit int) []string {
	if limit <= 0 {
		limit = 8
	}
	q := strings.ToLower(strings.TrimSpace(target))
	if q == "" {
		return nil
	}
	var matches []Node
	for _, n := range g.Nodes {
		if n.Kind == "doc" {
			continue
		}
		if strings.EqualFold(n.ID, q) || strings.EqualFold(n.Name, q) || strings.EqualFold(n.Path, q) ||
			strings.Contains(strings.ToLower(n.Name), q) || strings.Contains(strings.ToLower(n.Path), q) {
			matches = append(matches, n)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		// Exact name match first, then symbols over files, then higher centrality.
		ei := strings.EqualFold(matches[i].Name, q)
		ej := strings.EqualFold(matches[j].Name, q)
		if ei != ej {
			return ei
		}
		if (matches[i].Kind != "file") != (matches[j].Kind != "file") {
			return matches[i].Kind != "file"
		}
		if matches[i].Centrality != matches[j].Centrality {
			return matches[i].Centrality > matches[j].Centrality
		}
		return matches[i].ID < matches[j].ID
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	cards := make([]string, 0, len(matches))
	for _, m := range matches {
		if c := g.SpecCard(m.ID); c != "" {
			cards = append(cards, c)
		}
	}
	return cards
}

type specLinkBuckets struct {
	calls     []string
	calledBy  []string
	usesTypes []string
	testedBy  []string
	docs      []string
	seen      map[string]bool
}

func (s *specLinkBuckets) add(dst *[]string, bucket string, n Node) {
	key := bucket + ":" + n.ID
	if n.ID == "" || s.seen[key] {
		return
	}
	s.seen[key] = true
	label := n.Name
	if n.Path != "" && n.Line > 0 {
		label = fmt.Sprintf("%s (%s:%d)", n.Name, n.Path, n.Line)
	}
	*dst = append(*dst, label)
}

func (s *specLinkBuckets) recordEdge(e Edge, nodeID string, nodeByID map[string]Node) {
	if e.From == nodeID {
		if target, ok := nodeByID[e.To]; ok {
			if e.Relation == "CALLS" {
				s.add(&s.calls, "calls", target)
			} else if e.Relation == "USES_TYPE" {
				s.add(&s.usesTypes, "types", target)
			}
		}
	}
	if e.To != nodeID {
		return
	}
	src, ok := nodeByID[e.From]
	if !ok {
		return
	}
	switch e.Relation {
	case "CALLS":
		if src.Kind == "test" {
			s.add(&s.testedBy, "tests", src)
		} else {
			s.add(&s.calledBy, "called_by", src)
		}
	case "TESTS":
		s.add(&s.testedBy, "tests", src)
	case "DOCUMENTS":
		s.add(&s.docs, "docs", src)
	}
}

func (g *Graph) appendSpecLinks(b *strings.Builder, nodeID string) {
	nodeByID := make(map[string]Node, len(g.Nodes))
	for _, n := range g.Nodes {
		nodeByID[n.ID] = n
	}
	buckets := specLinkBuckets{seen: map[string]bool{}}
	for _, e := range g.Edges {
		buckets.recordEdge(e, nodeID, nodeByID)
	}
	writeCapList(b, "calls", buckets.calls, 6)
	writeCapList(b, "called_by", buckets.calledBy, 6)
	writeCapList(b, "uses_types", buckets.usesTypes, 6)
	writeCapList(b, "tested_by", buckets.testedBy, 6)
	writeCapList(b, "docs", buckets.docs, 4)
}

func writeCapList(b *strings.Builder, label string, items []string, capLen int) {
	if len(items) == 0 {
		return
	}
	sort.Strings(items)
	if len(items) > capLen {
		items = append(items[:capLen], fmt.Sprintf("+%d more", len(items)-capLen))
	}
	fmt.Fprintf(b, "- **@%s:** %s\n", label, strings.Join(items, ", "))
}

func extractSeeRefs(raw string) []string {
	var refs []string
	for _, m := range docInlineSeeRe.FindAllStringSubmatch(raw, -1) {
		val := strings.TrimSpace(firstStr(m[1], m[2]))
		if val != "" {
			refs = append(refs, val)
		}
	}
	if len(refs) == 0 {
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
			p := strings.Trim(part, "`()")
			if len(p) >= 2 {
				refs = append(refs, p)
			}
		}
	}
	return refs
}

func firstStr(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

func joinParts(a, b string) string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	switch {
	case a != "" && b != "":
		return a + ": " + b
	case a != "":
		return a
	default:
		return b
	}
}

func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	for i := 0; i < len(s)-1; i++ {
		if s[i] == '.' && s[i+1] == ' ' {
			return s[:i+1]
		}
	}
	return s
}

func truncateSummary(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return strings.TrimSpace(s[:maxLen-3]) + "..."
}

func appendUniqueString(slice []string, item string) []string {
	item = strings.TrimSpace(item)
	if item == "" {
		return slice
	}
	for _, existing := range slice {
		if existing == item {
			return slice
		}
	}
	return append(slice, item)
}
