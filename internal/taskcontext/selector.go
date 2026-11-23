package taskcontext

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
)

const version = 1

// Scope is supplied by an admission layer. SourceID is normally
// repository.Identity.ID(); CandidateID, when present, is the exact candidate
// observation ID used to read Files. The selector cannot establish either.
type Scope struct {
	SourceID    string `json:"source_id"`
	CandidateID string `json:"candidate_id,omitempty"`
}

// File is one complete, already admitted regular-file observation. Hash must
// be the SHA-256 of Content. The selector never opens a path itself.
type File struct {
	Path    string `json:"path"`
	Hash    string `json:"hash"`
	Content []byte `json:"-"`
}

// Limits cap one pure selection. They constrain returned model-visible bytes,
// not the authority or completeness of the caller's source inventory.
type Limits struct {
	MaxFiles        int `json:"max_files"`
	MaxBytes        int `json:"max_bytes"`
	MaxBytesPerFile int `json:"max_bytes_per_file"`
	MaxInputBytes   int `json:"max_input_bytes"`
	MaxOmissions    int `json:"max_omissions"`
}

// DefaultLimits are deliberately small enough for role prompts. A caller may
// lower them, but cannot raise them above the public hard ceilings.
func DefaultLimits() Limits {
	return Limits{MaxFiles: 12, MaxBytes: 48 << 10, MaxBytesPerFile: 8 << 10, MaxInputBytes: 8 << 20, MaxOmissions: 64}
}

func (l Limits) validate() error {
	if l.MaxFiles < 1 || l.MaxFiles > 64 || l.MaxBytes < 256 || l.MaxBytes > 256<<10 || l.MaxBytesPerFile < 128 || l.MaxBytesPerFile > 64<<10 || l.MaxBytesPerFile > l.MaxBytes || l.MaxInputBytes < l.MaxBytes || l.MaxInputBytes > 32<<20 || l.MaxOmissions < 0 || l.MaxOmissions > 512 {
		return errors.New("invalid task context limits")
	}
	return nil
}

// Input contains task text plus an already admitted corpus. ChangedPaths and
// PathHints are path-only evidence; neither causes a filesystem read.
// Selector opts into the experimental rrf-coverage-v1 mode. Empty preserves
// exact historical/default behavior and hashes; only "rrf-coverage-v1" is
// admitted beyond empty. PPR carries an explicit advisory file ordering (best
// first) from one bounded rank treatment over the already admitted corpus; it
// is a deterministic tie-break only, never a score, and is hashed into the
// input identity when present. Nil/empty preserves exact historical behavior.
type Input struct {
	Version      int            `json:"version"`
	Scope        Scope          `json:"scope"`
	Objective    string         `json:"objective"`
	Files        []File         `json:"-"`
	ChangedPaths []string       `json:"changed_paths,omitempty"`
	PathHints    []string       `json:"path_hints,omitempty"`
	Anchors      map[string]int `json:"anchors,omitempty"`
	Selector     string         `json:"selector,omitempty"`
	PPR          []string       `json:"ppr,omitempty"`
	Limits       Limits         `json:"limits"`
}

// SelectedFile is an exact half-open byte excerpt. Hash binds all admitted
// file bytes; ExcerptHash binds only Content. Both ranges and content are
// retained so a later role can identify precisely what it was shown.
// Ranks retains independent RRF ranker ordinals (1-based) for the
// experimental selector; Covered lists binary concepts newly covered by this
// visible excerpt. Both are absent for legacy selections, preserving exact
// historical JSON and hashes.
type SelectedFile struct {
	Path        string         `json:"path"`
	Hash        string         `json:"hash"`
	Start       int64          `json:"start"`
	End         int64          `json:"end"`
	ExcerptHash string         `json:"excerpt_hash"`
	Reason      string         `json:"reason"`
	Content     string         `json:"content"`
	Ranks       map[string]int `json:"ranks,omitempty"`
	Covered     []string       `json:"covered,omitempty"`
}

// Omission records why a supplied path was unavailable to this prompt. It is
// bounded; OmittedCount remains exact when path records are truncated.
type Omission struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Manifest is a deterministic, observable selection result. It does not claim
// that omitted files are irrelevant or absent from the bound source.
// Selector records the experimental mode that produced this selection. Empty
// preserves exact historical JSON and hashes; "rrf-coverage-v1" binds the new
// RRF plus bounded coverage treatment.
type Manifest struct {
	Version          int            `json:"version"`
	Scope            Scope          `json:"scope"`
	InputHash        string         `json:"input_hash"`
	InputBytes       int            `json:"input_bytes"`
	SelectedBytes    int            `json:"selected_bytes"`
	Selected         []SelectedFile `json:"selected"`
	Omissions        []Omission     `json:"omissions"`
	OmittedCount     int            `json:"omitted_count"`
	OmissionsTrimmed bool           `json:"omissions_trimmed"`
	Selector         string         `json:"selector,omitempty"`
}

// ID hashes the complete observed result. It is evidence of this selection,
// not a capability to read or mutate the source. The hash domain is unchanged;
// empty-selector manifests hash exactly as before because new optional fields
// are omitted when empty.
func (m Manifest) ID() (string, error) {
	if m.Version != version || m.InputBytes < 0 || m.SelectedBytes < 0 || m.SelectedBytes > m.InputBytes || safepath.RequireDigest(m.InputHash) != nil {
		return "", errors.New("invalid task context manifest")
	}
	if err := ValidateSelector(m.Selector); err != nil {
		return "", err
	}
	for _, sel := range m.Selected {
		if err := validateProvenance(sel.Ranks, sel.Covered); err != nil {
			return "", err
		}
		if m.Selector == "" && (len(sel.Ranks) != 0 || len(sel.Covered) != 0) {
			return "", errors.New("legacy manifest carries selector provenance")
		}
	}
	return canonical.Hash("harness.task-context-manifest.v1", m)
}

// Select ranks path and lexical evidence, returns UTF-8 snippets within Limits,
// and makes every omission explicit. It is deterministic for equivalent input:
// callers may provide files and hints in any order. Empty Selector preserves
// exact legacy behavior; Selector "rrf-coverage-v1" fuses independent ordinal
// rankers with unweighted RRF and bounded coverage. An explicit PPR ordering
// (legacy selection only) breaks score ties by advisory rank before the
// historical path tie-break; it never promotes zero-signal files and an empty
// ordering preserves exact legacy order and hashes.
func Select(in Input) (Manifest, error) {
	if err := validateInput(in); err != nil {
		return Manifest{}, err
	}
	files := append([]File(nil), in.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if in.Selector == SelectorRRFCoverageV1 {
		return selectRRFCoverage(in, files)
	}
	inputHash, err := inputID(in, files)
	if err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{Version: version, Scope: in.Scope, InputHash: inputHash, Selected: []SelectedFile{}, Omissions: []Omission{}}
	for _, f := range files {
		manifest.InputBytes += len(f.Content)
	}

	changed := pathSet(in.ChangedPaths)
	hints := pathSet(in.PathHints)
	terms := words(in.Objective)
	candidates := make([]scoredFile, 0, len(files))
	for _, file := range files {
		if sensitivePath(file.Path) {
			appendSensitiveOmission(&manifest, in.Limits)
			continue
		}
		if !utf8.Valid(file.Content) {
			appendOmission(&manifest, in.Limits, file.Path, "non_utf8")
			continue
		}
		score, first, reason := rank(file, terms, changed, hints)
		if pivot, anchored := in.Anchors[file.Path]; anchored {
			first = pivot
			score += 700
			if reason == "" {
				reason = "graph_anchor"
			}
		}
		candidates = append(candidates, scoredFile{file: file, score: score, first: first, reason: reason})
	}
	// An explicit advisory PPR ordering breaks score ties deterministically
	// before the historical path tie-break. Files absent from the ordering
	// sort after every ordered file; equal scores with no ordering resolve
	// exactly as before. Zero-signal (score 0) files stay omitted below: the
	// ordering never promotes them to positive evidence.
	pprRank := make(map[string]int, len(in.PPR))
	for i, p := range in.PPR {
		pprRank[p] = i + 1
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		ri, rj := pprRank[candidates[i].file.Path], pprRank[candidates[j].file.Path]
		if ri == 0 {
			ri = len(in.PPR) + 1
		}
		if rj == 0 {
			rj = len(in.PPR) + 1
		}
		if ri != rj {
			return ri < rj
		}
		return candidates[i].file.Path < candidates[j].file.Path
	})
	hasSignal := false
	for _, c := range candidates {
		if c.score > 0 {
			hasSignal = true
			break
		}
	}
	if !hasSignal && len(candidates) > 0 {
		candidates[0].reason = "deterministic_fallback"
		for i := 1; i < len(candidates); i++ {
			appendOmission(&manifest, in.Limits, candidates[i].file.Path, "unranked")
			candidates[i].score = -1
		}
	} else {
		for i := range candidates {
			if candidates[i].score == 0 {
				appendOmission(&manifest, in.Limits, candidates[i].file.Path, "unranked")
				candidates[i].score = -1
			}
		}
	}
	for _, candidate := range candidates {
		if candidate.score < 0 {
			continue
		}
		if len(manifest.Selected) >= in.Limits.MaxFiles {
			appendOmission(&manifest, in.Limits, candidate.file.Path, "file_limit")
			continue
		}
		remaining := in.Limits.MaxBytes - manifest.SelectedBytes
		if remaining < 128 {
			appendOmission(&manifest, in.Limits, candidate.file.Path, "byte_limit")
			continue
		}
		want := min(in.Limits.MaxBytesPerFile, remaining)
		start, end := excerpt(candidate.file.Content, candidate.first, want)
		if end-start < 1 {
			appendOmission(&manifest, in.Limits, candidate.file.Path, "empty")
			continue
		}
		content := candidate.file.Content[start:end]
		hash := sha256.Sum256(content)
		manifest.Selected = append(manifest.Selected, SelectedFile{Path: candidate.file.Path, Hash: candidate.file.Hash, Start: int64(start), End: int64(end), ExcerptHash: hex.EncodeToString(hash[:]), Reason: candidate.reason, Content: string(content)})
		manifest.SelectedBytes += len(content)
	}
	manifest.OmittedCount = len(files) - len(manifest.Selected)
	manifest.OmissionsTrimmed = manifest.OmittedCount > len(manifest.Omissions)
	return manifest, nil
}

type scoredFile struct {
	file         File
	score, first int
	reason       string
}

func validateInput(in Input) error {
	if in.Version != version || safepath.RequireDigest(in.Scope.SourceID) != nil || (in.Scope.CandidateID != "" && safepath.RequireDigest(in.Scope.CandidateID) != nil) || len(in.Objective) == 0 || len(in.Objective) > 16<<10 || !utf8.ValidString(in.Objective) || len(in.Files) == 0 || len(in.Files) > 4096 {
		return errors.New("invalid task context input")
	}
	if err := ValidateSelector(in.Selector); err != nil {
		return err
	}
	if err := in.Limits.validate(); err != nil {
		return err
	}
	seen := map[string]bool{}
	filesByPath := make(map[string]File, len(in.Files))
	total := 0
	for _, f := range in.Files {
		if safepath.Relative(f.Path) != nil || safepath.RequireDigest(f.Hash) != nil || !sameHash(f.Hash, f.Content) {
			return errors.New("invalid admitted task context file")
		}
		key := strings.ToLower(f.Path)
		if seen[key] {
			return errors.New("task context path alias")
		}
		seen[key] = true
		filesByPath[f.Path] = f
		total += len(f.Content)
		if total > in.Limits.MaxInputBytes {
			return errors.New("task context input byte limit")
		}
	}
	if len(in.Anchors) > len(in.Files) {
		return errors.New("too many task context anchors")
	}
	for path, offset := range in.Anchors {
		file, exists := filesByPath[path]
		if safepath.Relative(path) != nil || !exists || offset < 0 || offset >= len(file.Content) || !utf8.Valid(file.Content) || !utf8.RuneStart(file.Content[offset]) {
			return errors.New("invalid task context anchor")
		}
	}
	for _, paths := range [][]string{in.ChangedPaths, in.PathHints} {
		if len(paths) > 512 {
			return errors.New("too many task context paths")
		}
		for _, p := range paths {
			if safepath.Relative(p) != nil {
				return errors.New("invalid task context path hint")
			}
		}
	}
	if len(in.ChangedPaths) > 0 && in.Scope.CandidateID == "" {
		return errors.New("task context candidate missing for changed paths")
	}
	// An explicit advisory ordering binds only already admitted corpus paths:
	// every entry must name one admitted file exactly once, with no scope
	// escape and no mixing with the experimental RRF selector. Ordering
	// length is capped to the bounded rank-treatment size.
	if len(in.PPR) > 64 {
		return errors.New("too many task context PPR paths")
	}
	if len(in.PPR) > 0 {
		if in.Selector != "" {
			return errors.New("task context PPR requires legacy selection")
		}
		admitted := make(map[string]bool, len(in.Files))
		for _, f := range in.Files {
			admitted[f.Path] = true
		}
		seenPPR := make(map[string]bool, len(in.PPR))
		for _, p := range in.PPR {
			if safepath.Relative(p) != nil || !admitted[p] || seenPPR[p] {
				return errors.New("invalid task context PPR path")
			}
			seenPPR[p] = true
		}
	}
	return nil
}

func inputID(in Input, files []File) (string, error) {
	type inputFile struct{ Path, Hash string }
	bound := make([]inputFile, 0, len(files))
	for _, f := range files {
		bound = append(bound, inputFile{f.Path, f.Hash})
	}
	changed, hints := append([]string(nil), in.ChangedPaths...), append([]string(nil), in.PathHints...)
	sort.Strings(changed)
	sort.Strings(hints)
	// The advisory ordering is rank order, not a set: it hashes as given so
	// a reordered treatment is a different input. Empty stays omitted, which
	// preserves the exact historical input hash.
	ppr := append([]string(nil), in.PPR...)
	return canonical.Hash("harness.task-context-input.v1", struct {
		Version   int            `json:"version"`
		Scope     Scope          `json:"scope"`
		Objective string         `json:"objective"`
		Files     []inputFile    `json:"files"`
		Changed   []string       `json:"changed"`
		Hints     []string       `json:"hints"`
		Anchors   map[string]int `json:"anchors,omitempty"`
		Selector  string         `json:"selector,omitempty"`
		PPR       []string       `json:"ppr,omitempty"`
		Limits    Limits         `json:"limits"`
	}{in.Version, in.Scope, in.Objective, bound, changed, hints, in.Anchors, in.Selector, ppr, in.Limits})
}

func sameHash(want string, content []byte) bool {
	got := sha256.Sum256(content)
	return want == hex.EncodeToString(got[:])
}
func pathSet(paths []string) map[string]bool {
	out := map[string]bool{}
	for _, p := range paths {
		out[p] = true
	}
	return out
}
func words(s string) []string {
	seen := map[string]bool{}
	for _, part := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' }) {
		if len([]rune(part)) >= 2 {
			seen[part] = true
		}
	}
	out := make([]string, 0, len(seen))
	for w := range seen {
		out = append(out, w)
	}
	sort.Strings(out)
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

func rank(file File, terms []string, changed, hints map[string]bool) (score, first int, reason string) {
	path := strings.ToLower(file.Path)
	first = -1
	if changed[file.Path] {
		score += 1000
		reason = "changed_path"
	}
	if hints[file.Path] {
		score += 800
		if reason == "" {
			reason = "path_hint"
		}
	}
	sample := file.Content
	if len(sample) > 32<<10 {
		cut := 32 << 10
		for cut > 0 && !utf8.RuneStart(sample[cut]) {
			cut--
		}
		sample = sample[:cut]
	}
	// Content lexical evidence is token based so short objective terms (e.g.
	// "fix") do not spuriously match inside larger words (e.g. "prefix").
	// Path evidence intentionally stays substring based so "greet" still
	// matches "greeting.go". Sample is capped above and terms are capped in
	// words, so per-file work stays proportional to a fixed budget.
	counts, firsts := tokenLexicalIndex(string(sample))
	pivotTerm := ""
	pivotCount := 0
	pivotLength := 0
	for _, term := range terms {
		if strings.Contains(path, term) {
			score += 80
			if reason == "" {
				reason = "path_match"
			}
		}
		if n, ok := counts[term]; ok {
			score += min(240, 30*n)
			off := firsts[term]
			termLength := utf8.RuneCountInString(term)
			// Prefer the most informative matching token as the excerpt pivot:
			// rarer terms beat common words, longer terms break frequency ties,
			// and the earliest occurrence then breaks remaining ties. Include a
			// final lexical tie-break so callers may supply objective terms in
			// any order without changing the result.
			if pivotTerm == "" || n < pivotCount || n == pivotCount && (termLength > pivotLength || termLength == pivotLength && (off < first || off == first && term < pivotTerm)) {
				first = off
				pivotTerm = term
				pivotCount = n
				pivotLength = termLength
			}
			if reason == "" {
				reason = "lexical_match"
			}
		}
	}
	if first < 0 {
		first = 0
	}
	return score, first, reason
}

// tokenLexicalIndex builds token frequencies and first byte offsets for a
// capped sample. Tokenisation mirrors words(): split on anything that is not
// a letter, digit, or underscore, lowercased for comparison. Offsets are
// original sample byte positions at rune boundaries.
func tokenLexicalIndex(sample string) (map[string]int, map[string]int) {
	counts := map[string]int{}
	firsts := map[string]int{}
	start := -1
	for i := 0; i <= len(sample); {
		var r rune
		var sz int
		if i < len(sample) {
			r, sz = utf8.DecodeRuneInString(sample[i:])
		}
		isWord := i < len(sample) && isWordRune(r)
		if isWord && start < 0 {
			start = i
		}
		if !isWord && start >= 0 {
			tok := strings.ToLower(sample[start:i])
			counts[tok]++
			if _, ok := firsts[tok]; !ok {
				firsts[tok] = start
			}
			start = -1
		}
		if i >= len(sample) {
			break
		}
		if sz <= 0 {
			sz = 1
		}
		i += sz
	}
	return counts, firsts
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

// indexFoldInsensitive returns the byte offset in s of the first
// case-insensitive whole-token occurrence of term, preserving original byte
// positions. Callers must pass the capped 32KiB sample, never the full file,
// to keep scanning bounded. Substring hits inside larger words are rejected,
// and the result is always at a rune boundary in s.
func indexFoldInsensitive(s, term string) int {
	if term == "" {
		return -1
	}
	termFolded := strings.ToLower(term)
	if termFolded == "" {
		return -1
	}
	// Whole-token only: term itself must be a single token.
	for _, r := range termFolded {
		if !isWordRune(r) {
			return -1
		}
	}
	var folded strings.Builder
	folded.Grow(len(s))
	mapping := make([]int, 0, len(s)+1)
	for orig := 0; orig < len(s); {
		r, sz := utf8.DecodeRuneInString(s[orig:])
		if r == utf8.RuneError && sz <= 1 {
			folded.WriteByte(s[orig])
			mapping = append(mapping, orig)
			orig++
			continue
		}
		lower := strings.ToLower(string(r))
		for i := 0; i < len(lower); i++ {
			mapping = append(mapping, orig)
		}
		folded.WriteString(lower)
		orig += sz
	}
	f := folded.String()
	searchFrom := 0
	for searchFrom <= len(f) {
		rel := strings.Index(f[searchFrom:], termFolded)
		if rel < 0 {
			return -1
		}
		idx := searchFrom + rel
		origOff := len(s)
		if idx < len(mapping) {
			origOff = mapping[idx]
		}
		endFold := idx + len(termFolded)
		origEnd := len(s)
		if endFold < len(mapping) {
			origEnd = mapping[endFold]
		}
		if tokenBoundariesOK(s, origOff, origEnd) {
			return origOff
		}
		searchFrom = idx + 1
	}
	return -1
}

// tokenBoundariesOK reports whether [start,end) in original bytes is a whole
// token: bounded by non-word runes or string edges.
func tokenBoundariesOK(s string, start, end int) bool {
	if start < 0 || end > len(s) || start >= end {
		return false
	}
	if start > 0 {
		r, _ := utf8.DecodeLastRuneInString(s[:start])
		if isWordRune(r) {
			return false
		}
	}
	if end < len(s) {
		r, _ := utf8.DecodeRuneInString(s[end:])
		if isWordRune(r) {
			return false
		}
	}
	return true
}

func excerpt(content []byte, pivot, limit int) (int, int) {
	if limit > len(content) {
		limit = len(content)
	}
	if limit == len(content) {
		return 0, len(content)
	}
	start := pivot - limit/3
	if start < 0 {
		start = 0
	}
	if start+limit > len(content) {
		start = len(content) - limit
	}
	for start > 0 && !utf8.RuneStart(content[start]) {
		start--
	}
	end := start + limit
	for end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	return start, end
}

func appendOmission(m *Manifest, l Limits, path, reason string) {
	if len(m.Omissions) < l.MaxOmissions {
		m.Omissions = append(m.Omissions, Omission{path, reason})
	}
}
func appendSensitiveOmission(m *Manifest, l Limits) {
	if len(m.Omissions) < l.MaxOmissions {
		m.Omissions = append(m.Omissions, Omission{Path: "[redacted]", Reason: "sensitive_path"})
	}
}

// EligiblePath reports whether a candidate path may be admitted for task
// context selection. It reuses the exact sensitive-path rules applied by
// Select: sensitive paths are never eligible for model-visible content and
// must be omitted as redacted rather than read. Ordinary paths that merely
// contain substrings such as "key" or "secret" as part of a larger token
// remain eligible.
func EligiblePath(p string) bool {
	return !sensitivePath(p)
}

func sensitivePath(p string) bool {
	b := strings.ToLower(p)
	segs := strings.Split(b, "/")
	for _, seg := range segs {
		if seg == ".env" || strings.HasPrefix(seg, ".env.") || strings.HasPrefix(seg, ".env_") || strings.HasPrefix(seg, ".env-") || seg == ".envrc" || strings.HasSuffix(seg, ".env") || strings.Contains(seg, ".env.") {
			return true
		}
	}
	if len(segs) > 1 {
		for _, dir := range segs[:len(segs)-1] {
			if dir == "secret" || dir == "secrets" || dir == "credential" || dir == "credentials" || dir == ".secret" || dir == ".secrets" || dir == "private" {
				return true
			}
		}
	}
	base := segs[len(segs)-1]
	if base == "id_rsa" || base == "id_ed25519" || base == "id_ecdsa" || base == "id_dsa" {
		return true
	}
	if strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") {
		return true
	}
	if strings.Contains(base, "credential") {
		return true
	}
	for _, tok := range strings.FieldsFunc(base, func(r rune) bool { return r == '.' || r == '_' || r == '-' }) {
		if tok == "secret" || tok == "secrets" || tok == "private" {
			return true
		}
	}
	return false
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
