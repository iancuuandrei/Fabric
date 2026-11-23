package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/ri"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

const goSemanticQueryMaxBytes = 32 << 10

type goSemanticQueryEnvelope struct {
	Repository ri.Source                  `json:"repository"`
	Sources    []goGraphSourceObservation `json:"sources"`
	Result     ri.SemanticResult          `json:"result"`
}

func riGoQueryCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	if len(args) != 5 && len(args) != 6 {
		return errors.New("usage: ri query EXE EXE_SHA256 SPEC_JSON QUERY_JSON [CACHE_DIR]")
	}
	if len(args) == 6 && args[5] == "" {
		return errors.New("cache directory cannot be empty when supplied")
	}
	queryPath, err := riAbsolutePath(root, args[4])
	if err != nil {
		return err
	}
	query, err := readGoSemanticQuery(queryPath)
	if err != nil {
		return err
	}
	if err := validateGoSemanticQuery(query); err != nil {
		return err
	}
	built, err := buildGoGraph(ctx, root, args[1], args[2], args[3], cacheArgument(args, 5))
	if err != nil {
		return err
	}
	result, err := ri.QueryGoSemantic(built.result.Graph, query)
	if err != nil {
		return err
	}
	return output(out, goSemanticQueryEnvelope{Repository: built.result.Repository, Sources: built.result.Sources, Result: result})
}

func readGoSemanticQuery(path string) (ri.SemanticQuery, error) {
	var query ri.SemanticQuery
	file, err := os.Open(path)
	if err != nil {
		return query, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return query, err
	}
	if !info.Mode().IsRegular() || info.Size() > goSemanticQueryMaxBytes {
		return query, errors.New("semantic query must be a regular JSON file no larger than 32 KiB")
	}
	raw, err := io.ReadAll(io.LimitReader(file, goSemanticQueryMaxBytes+1))
	if err != nil || len(raw) > goSemanticQueryMaxBytes {
		return query, errors.New("semantic query exceeds 32 KiB or could not be read")
	}
	normal, err := canonical.Normalize(raw)
	if err != nil {
		return query, fmt.Errorf("invalid semantic query JSON encoding: %w", err)
	}
	if err := canonical.Decode(normal, &query); err != nil {
		return query, fmt.Errorf("invalid semantic query JSON: %w", err)
	}
	return query, nil
}

func validateGoSemanticQuery(query ri.SemanticQuery) error {
	if query.Limit < 1 || query.Limit > 1000 || len(query.Path) > 4096 || len(query.NamePrefix) > 4096 || len(query.ImportPath) > 4096 || query.MaxDepth < 0 || query.MaxDepth > 16 {
		return errors.New("semantic query selectors or limit exceed bounds")
	}
	if query.Path != "" && (safepath.Relative(query.Path) != nil || filepath.Ext(query.Path) != ".go" || !taskcontext.EligiblePath(query.Path)) {
		return errors.New("semantic query path is invalid, sensitive, or ineligible")
	}
	if query.ImportPath != "" && !validGoImportPathCLI(query.ImportPath) {
		return errors.New("semantic query import path is invalid")
	}
	if query.NamePrefix != "" && strings.TrimSpace(query.NamePrefix) != query.NamePrefix {
		return errors.New("semantic query name prefix must not contain surrounding whitespace")
	}
	if len(query.Paths) > 64 {
		return errors.New("semantic impact query accepts at most 64 paths")
	}
	seen := make(map[string]bool, len(query.Paths))
	for _, path := range query.Paths {
		if len(path) > 4096 || safepath.Relative(path) != nil || filepath.Ext(path) != ".go" || !taskcontext.EligiblePath(path) || seen[path] {
			return errors.New("semantic impact query contains an invalid, repeated, or ineligible Go path")
		}
		seen[path] = true
	}
	switch query.Vocabulary {
	case "symbol":
		if query.ImportPath != "" || len(query.Paths) != 0 || query.MaxDepth != 0 || query.NamePrefix == "" && query.Path == "" && query.Kind == "" || query.Kind != "" && !validGoSemanticSymbolKind(query.Kind) {
			return errors.New("symbol query requires a name, path, or supported kind selector")
		}
	case "calls":
		if query.ImportPath != "" || len(query.Paths) != 0 || query.Kind != "" || query.MaxDepth != 0 || query.NamePrefix == "" && query.Path == "" {
			return errors.New("calls query requires a spelling or path selector")
		}
	case "imports", "tests", "generators", "module":
		if query.NamePrefix != "" || query.Kind != "" || len(query.Paths) != 0 || query.MaxDepth != 0 || query.Path == "" && query.ImportPath == "" {
			return errors.New("semantic relation query requires a path or import selector")
		}
	case "path":
		if query.Path == "" || query.NamePrefix != "" || query.ImportPath != "" || query.Kind != "" || len(query.Paths) != 0 || query.MaxDepth != 0 {
			return errors.New("path query requires one exact path")
		}
	case "impact":
		if query.Path != "" || query.NamePrefix != "" || query.ImportPath != "" || query.Kind != "" || len(query.Paths) == 0 {
			return errors.New("impact query requires one or more exact source paths")
		}
	case "references", "implementations":
		return ri.ErrSemanticVocabularyUnsupported
	default:
		return errors.New("unknown semantic vocabulary")
	}
	return nil
}

func validGoSemanticSymbolKind(value string) bool {
	switch value {
	case "function_declaration", "method_declaration", "type_spec", "type_alias":
		return true
	default:
		return false
	}
}
