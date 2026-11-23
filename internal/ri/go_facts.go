package ri

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"go/scanner"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/safepath"
)

const (
	goFactsSchema        = "engorch.go-file-facts.v1"
	goFactsParserVersion = "tree-sitter-go-0.25.0"
	goFactsMaxSource     = 1 << 20
	goFactsMaxItems      = 100_000
)

// GoFileFacts is a bounded syntax-only observation of one committed Go file.
// It does not resolve symbols or claim semantic completeness.
type GoFileFacts struct {
	Schema           string     `json:"schema"`
	Language         string     `json:"language"`
	ParserVersion    string     `json:"parser_version"`
	Path             string     `json:"path"`
	SourceSHA256     string     `json:"source_sha256"`
	ProducerSHA256   string     `json:"producer_sha256"`
	CacheKey         string     `json:"cache_key"`
	BodySHA256       string     `json:"body_sha256"`
	SyntaxErrors     bool       `json:"syntax_errors"`
	Coverage         string     `json:"coverage"`
	Declarations     []GoSymbol `json:"declarations"`
	Imports          []GoImport `json:"imports"`
	Calls            []GoCall   `json:"calls"`
	GeneratedMarkers []string   `json:"generated_markers"`
	Cache            string     `json:"cache"`
	ParseCount       int        `json:"parse_count"`
}

// GoRange is a half-open byte span in the exact committed file content.
type GoRange struct {
	StartByte int `json:"start_byte"`
	EndByte   int `json:"end_byte"`
}

// GoSymbol is a syntactic declaration name and its source span.
type GoSymbol struct {
	Name  string  `json:"name"`
	Kind  string  `json:"kind"`
	Range GoRange `json:"range"`
	Test  bool    `json:"test"`
}

// GoImport records one syntactic Go import specification.
type GoImport struct {
	Path  string  `json:"path"`
	Alias *string `json:"alias"`
	Range GoRange `json:"range"`
}

// GoCall records a syntactic call spelling; resolution is intentionally
// unresolved because this operation does not perform type checking.
type GoCall struct {
	Spelling   string  `json:"spelling"`
	Resolution string  `json:"resolution"`
	Range      GoRange `json:"range"`
}

// GoFileFacts parses one exact UTF-8 source value using the pinned RI binary.
// The caller must obtain source from a committed repository blob; this method
// binds the response to its bytes, path and producer executable hash.
func (c Client) GoFileFacts(ctx context.Context, path string, source []byte, cacheDir string) (GoFileFacts, error) {
	request, sourceSHA256, err := goFileFactsRequest(path, source, cacheDir, c.ExecutableHash)
	if err != nil {
		return GoFileFacts{}, err
	}
	result, err := c.Call(ctx, request)
	if err != nil {
		return GoFileFacts{}, err
	}
	return decodeGoFileFacts(result, path, sourceSHA256, c.ExecutableHash, source)
}

// GoFileFacts sends one exact source value over a stream whose producer hash
// was pinned when the executable was validated and opened.
func (s *Stream) GoFileFacts(ctx context.Context, path string, source []byte, cacheDir string) (GoFileFacts, error) {
	if s == nil || !lowerDigest(s.producerHash) {
		return GoFileFacts{}, errors.New("validated RI stream required")
	}
	request, sourceSHA256, err := goFileFactsRequest(path, source, cacheDir, s.producerHash)
	if err != nil {
		return GoFileFacts{}, err
	}
	result, err := s.Call(ctx, request)
	if err != nil {
		return GoFileFacts{}, err
	}
	return decodeGoFileFacts(result, path, sourceSHA256, s.producerHash, source)
}

func goFileFactsRequest(path string, source []byte, cacheDir, producerHash string) (map[string]any, string, error) {
	if err := safepath.Relative(path); err != nil || len(path) > 4096 {
		return nil, "", errors.New("invalid Go facts repository path")
	}
	if len(source) > goFactsMaxSource || !utf8.Valid(source) {
		return nil, "", errors.New("Go facts source must be UTF-8 and at most 1 MiB")
	}
	sourceHash := sha256.Sum256(source)
	sourceSHA256 := hex.EncodeToString(sourceHash[:])
	request := map[string]any{
		"operation":       "go_file_facts",
		"path":            path,
		"source_text":     string(source),
		"source_sha256":   sourceSHA256,
		"producer_sha256": producerHash,
	}
	if cacheDir != "" {
		volumeRoot := filepath.VolumeName(cacheDir) + string(filepath.Separator)
		if !filepath.IsAbs(cacheDir) || filepath.Clean(cacheDir) != cacheDir || filepath.Clean(cacheDir) == volumeRoot {
			return nil, "", errors.New("Go facts cache directory must be an absolute clean non-root path")
		}
		request["cache_dir"] = cacheDir
	}
	return request, sourceSHA256, nil
}

func decodeGoFileFacts(result json.RawMessage, path, sourceSHA256, producerHash string, source []byte) (GoFileFacts, error) {
	var facts GoFileFacts
	if err := canonical.Decode(result, &facts); err != nil {
		return GoFileFacts{}, err
	}
	if err := validateGoFileFacts(facts, path, sourceSHA256, producerHash, source); err != nil {
		return GoFileFacts{}, err
	}
	return facts, nil
}

func validateGoFileFacts(facts GoFileFacts, path, sourceSHA256, producerSHA256 string, source []byte) error {
	if facts.Schema != goFactsSchema || facts.Language != "go" || facts.ParserVersion != goFactsParserVersion || facts.Path != path || facts.SourceSHA256 != sourceSHA256 || facts.ProducerSHA256 != producerSHA256 || facts.Coverage != "PARTIAL" {
		return errors.New("Go facts response binding or coverage mismatch")
	}
	if (facts.Cache != "miss" && facts.Cache != "hit") ||
		(facts.Cache == "miss" && facts.ParseCount != 1) || (facts.Cache == "hit" && facts.ParseCount != 0) {
		return errors.New("Go facts response has unexpected cache metrics")
	}
	if !lowerDigest(facts.CacheKey) || !lowerDigest(facts.BodySHA256) {
		return errors.New("Go facts response digest is malformed")
	}
	cacheKey, err := canonical.Hash("harness.ri.go-file-facts.v1", map[string]any{
		"schema":          goFactsSchema,
		"language":        "go",
		"parser":          goFactsParserVersion,
		"path":            path,
		"source_sha256":   sourceSHA256,
		"producer_sha256": producerSHA256,
	})
	if err != nil || facts.CacheKey != cacheKey {
		return errors.New("Go facts cache key differs from request binding")
	}
	body := facts
	body.BodySHA256 = ""
	body.Cache = ""
	body.ParseCount = 0
	encoded, err := canonical.Bytes(body)
	if err != nil {
		return err
	}
	bodyHash := sha256.Sum256(encoded)
	if facts.BodySHA256 != hex.EncodeToString(bodyHash[:]) {
		return errors.New("Go facts body digest mismatch")
	}
	if len(facts.Declarations)+len(facts.Imports)+len(facts.Calls)+len(facts.GeneratedMarkers) > goFactsMaxItems {
		return errors.New("Go facts item count exceeds bound")
	}
	for i, symbol := range facts.Declarations {
		if symbol.Name == "" || len(symbol.Name) > 4096 || !validGoSymbolKind(symbol.Kind) || !validGoRange(symbol.Range, len(source)) || string(source[symbol.Range.StartByte:symbol.Range.EndByte]) != symbol.Name {
			return errors.New("invalid Go declaration fact")
		}
		if i > 0 && goRangeNameLess(symbol.Range, symbol.Name, facts.Declarations[i-1].Range, facts.Declarations[i-1].Name) {
			return errors.New("Go declarations are not ordered")
		}
	}
	for i, imported := range facts.Imports {
		if len(imported.Path) > 4096 || !validGoRange(imported.Range, len(source)) || !matchesGoImport(source[imported.Range.StartByte:imported.Range.EndByte], imported) {
			return errors.New("invalid Go import fact")
		}
		if i > 0 && goRangeNameLess(imported.Range, imported.Path, facts.Imports[i-1].Range, facts.Imports[i-1].Path) {
			return errors.New("Go imports are not ordered")
		}
	}
	for i, call := range facts.Calls {
		if call.Spelling == "" || len(call.Spelling) > 4096 || call.Resolution != "UNRESOLVED" || !validGoRange(call.Range, len(source)) || string(source[call.Range.StartByte:call.Range.EndByte]) != call.Spelling {
			return errors.New("invalid Go call fact")
		}
		if i > 0 && goRangeNameLess(call.Range, call.Spelling, facts.Calls[i-1].Range, facts.Calls[i-1].Spelling) {
			return errors.New("Go calls are not ordered")
		}
	}
	if !matchesGoGeneratedMarkers(source, facts.GeneratedMarkers) {
		return errors.New("Go generated marker facts differ from source comments")
	}
	return nil
}

func validGoSymbolKind(kind string) bool {
	switch kind {
	case "function_declaration", "method_declaration", "type_spec", "type_alias":
		return true
	default:
		return false
	}
}

func matchesGoImport(source []byte, imported GoImport) bool {
	fileSet := token.NewFileSet()
	file := fileSet.AddFile("import.go", fileSet.Base(), len(source))
	var s scanner.Scanner
	s.Init(file, source, func(token.Position, string) {}, scanner.ScanComments)
	var prefixToken token.Token
	var prefixText string
	var importPath string
	stringCount := 0
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.COMMENT {
			continue
		}
		if tok == token.STRING {
			stringCount++
			value, err := strconv.Unquote(lit)
			if err != nil {
				return false
			}
			importPath = value
			continue
		}
		if tok == token.SEMICOLON && stringCount == 1 && lit == "\n" {
			continue
		}
		if stringCount != 0 || prefixToken != token.ILLEGAL || (tok != token.IDENT && tok != token.PERIOD) {
			return false
		}
		prefixToken = tok
		prefixText = lit
		if tok == token.PERIOD {
			prefixText = "."
		}
	}
	if stringCount != 1 || importPath != imported.Path {
		return false
	}
	if imported.Alias == nil {
		return prefixToken == token.ILLEGAL
	}
	return (prefixToken == token.IDENT || prefixToken == token.PERIOD) && prefixText == *imported.Alias
}

func matchesGoGeneratedMarkers(source []byte, markers []string) bool {
	fileSet := token.NewFileSet()
	file := fileSet.AddFile("source.go", fileSet.Base(), len(source))
	var s scanner.Scanner
	s.Init(file, source, func(token.Position, string) {}, scanner.ScanComments)
	observed := make([]string, 0, len(markers))
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.COMMENT && (strings.Contains(lit, "//go:generate") || strings.Contains(lit, "Code generated")) {
			observed = append(observed, lit)
		}
	}
	if len(observed) != len(markers) {
		return false
	}
	sort.Strings(observed)
	for i := range observed {
		if observed[i] != markers[i] {
			return false
		}
	}
	return true
}

func validGoRange(r GoRange, sourceBytes int) bool {
	return r.StartByte >= 0 && r.EndByte > r.StartByte && r.EndByte <= sourceBytes
}

func goRangeNameLess(a GoRange, aName string, b GoRange, bName string) bool {
	return a.StartByte < b.StartByte || (a.StartByte == b.StartByte && strings.Compare(aName, bName) < 0)
}

func lowerDigest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}
