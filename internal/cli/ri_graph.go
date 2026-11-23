package cli

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/ri"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

const (
	goGraphSpecMaxBytes     = canonical.MaxBytes
	goGraphMaxFiles         = 256
	goGraphMaxBytes         = 8 << 20
	goGraphMaxFileBytes     = 1 << 20
	goTopologyPathsMaxBytes = 32 << 10
	goTopologyMaxPaths      = 64
)

type goGraphSpec struct {
	Files           []goGraphFileSpec       `json:"files"`
	Generators      []ri.GoGeneratorBinding `json:"generators"`
	ModuleInventory *ri.GoModuleInventory   `json:"module_inventory,omitempty"`
}

type goGraphFileSpec struct {
	Path             string `json:"path"`
	ImportPath       string `json:"import_path"`
	ModulePath       string `json:"module_path"`
	TestOfImportPath string `json:"test_of_import_path,omitempty"`
}

type goGraphSourceObservation struct {
	Source repository.SourceDigest `json:"source"`
}

type goGraphResult struct {
	Repository ri.Source                  `json:"repository"`
	Sources    []goGraphSourceObservation `json:"sources"`
	Graph      ri.GoEngineeringGraph      `json:"graph"`
}

type goContextResult struct {
	Repository ri.Source                  `json:"repository"`
	Sources    []goGraphSourceObservation `json:"sources"`
	Context    ri.GoContextManifest       `json:"context"`
}

type goTopologyResult struct {
	Repository ri.Source                  `json:"repository"`
	Sources    []goGraphSourceObservation `json:"sources"`
	Topology   ri.GoTopologyResult        `json:"topology"`
}

type goGraphSource struct {
	spec   goGraphFileSpec
	digest repository.SourceDigest
	bytes  []byte
}

type builtGoGraph struct {
	result       goGraphResult
	contextFiles []taskcontext.File
}

type goGraphSourceBuffer struct {
	strings.Builder
	limit int
}

// Write retains committed source bytes within the Go graph file ceiling.
func (b *goGraphSourceBuffer) Write(value []byte) (int, error) {
	if len(value) > b.limit-b.Len() {
		return 0, errors.New("committed Go graph source exceeds its configured byte bound")
	}
	return b.Builder.Write(value)
}

func riGoGraphCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	if len(args) != 4 && len(args) != 5 {
		return errors.New("usage: ri graph EXE EXE_SHA256 SPEC_JSON [CACHE_DIR]")
	}
	if len(args) == 5 && args[4] == "" {
		return errors.New("cache directory cannot be empty when supplied")
	}
	built, err := buildGoGraph(ctx, root, args[1], args[2], args[3], cacheArgument(args, 4))
	if err != nil {
		return err
	}
	return output(out, built.result)
}

func riGoContextCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	if len(args) != 5 && len(args) != 6 {
		return errors.New("usage: ri context EXE EXE_SHA256 SPEC_JSON OBJECTIVE [CACHE_DIR]")
	}
	if len(args) == 6 && args[5] == "" {
		return errors.New("cache directory cannot be empty when supplied")
	}
	built, err := buildGoGraph(ctx, root, args[1], args[2], args[3], cacheArgument(args, 5))
	if err != nil {
		return err
	}
	manifest, err := ri.CompileGoContext(ri.GoContextInput{SourceID: built.result.Graph.SourceID, Graph: built.result.Graph, Objective: args[4], Files: built.contextFiles, Limits: taskcontext.DefaultLimits()})
	if err != nil {
		return err
	}
	return output(out, goContextResult{Repository: built.result.Repository, Sources: built.result.Sources, Context: manifest})
}

func riGoTopologyCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	if len(args) != 6 && len(args) != 7 {
		return errors.New("usage: ri topology EXE EXE_SHA256 SPEC_JSON CHANGED_PATHS_JSON MAX_GROUP_FILES [CACHE_DIR]")
	}
	if len(args) == 7 && args[6] == "" {
		return errors.New("cache directory cannot be empty when supplied")
	}
	maxGroupFiles, err := strconv.Atoi(args[5])
	if err != nil || maxGroupFiles < 1 || maxGroupFiles > 32 {
		return errors.New("MAX_GROUP_FILES must be an integer from 1 to 32")
	}
	pathsFile, err := riAbsolutePath(root, args[4])
	if err != nil {
		return err
	}
	changedPaths, err := readGoTopologyPaths(pathsFile)
	if err != nil {
		return err
	}
	built, err := buildGoGraph(ctx, root, args[1], args[2], args[3], cacheArgument(args, 6))
	if err != nil {
		return err
	}
	result, err := queryGoTopologyResult(built, changedPaths, maxGroupFiles)
	if err != nil {
		return err
	}
	return output(out, result)
}

func queryGoTopologyResult(built builtGoGraph, changedPaths []string, maxGroupFiles int) (goTopologyResult, error) {
	topology, err := ri.QueryGoTopology(built.result.Graph, changedPaths, maxGroupFiles)
	if err != nil {
		return goTopologyResult{}, err
	}
	return goTopologyResult{Repository: built.result.Repository, Sources: built.result.Sources, Topology: topology}, nil
}

func readGoTopologyPaths(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > goTopologyPathsMaxBytes {
		return nil, errors.New("changed paths must be a regular JSON file no larger than 32 KiB")
	}
	raw, err := io.ReadAll(io.LimitReader(file, goTopologyPathsMaxBytes+1))
	if err != nil || len(raw) > goTopologyPathsMaxBytes {
		return nil, errors.New("changed paths exceed 32 KiB or could not be read")
	}
	var paths []string
	normal, err := canonical.Normalize(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid changed-path JSON encoding: %w", err)
	}
	if err := canonical.Decode(normal, &paths); err != nil {
		return nil, fmt.Errorf("invalid changed-path JSON: %w", err)
	}
	if len(paths) == 0 || len(paths) > goTopologyMaxPaths {
		return nil, errors.New("changed paths must contain 1 to 64 entries")
	}
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		if len(path) > 4096 || safepath.Relative(path) != nil || filepath.Ext(path) != ".go" || !taskcontext.EligiblePath(path) {
			return nil, errors.New("changed paths contain an invalid, sensitive, or ineligible Go source path")
		}
		if seen[path] {
			return nil, fmt.Errorf("changed paths repeat %q", path)
		}
		seen[path] = true
	}
	return paths, nil
}

func cacheArgument(args []string, index int) string {
	if len(args) <= index {
		return ""
	}
	return args[index]
}

func buildGoGraph(ctx context.Context, root, executableArg, executableHash, specArg, cacheArg string) (builtGoGraph, error) {
	var empty builtGoGraph
	specPath, err := riAbsolutePath(root, specArg)
	if err != nil {
		return empty, err
	}
	spec, err := readGoGraphSpec(specPath)
	if err != nil {
		return empty, err
	}
	if err := validateGoGraphSpec(spec); err != nil {
		return empty, err
	}
	cfg, err := configuration(root)
	if err != nil {
		return empty, err
	}
	identity, err := repository.Discover(ctx, root, cfg.Repository)
	if err != nil {
		return empty, err
	}
	source, err := ri.FromRepository(identity)
	if err != nil {
		return empty, err
	}
	if spec.ModuleInventory != nil {
		if err := ri.ValidateGoModuleInventory(*spec.ModuleInventory, identity); err != nil {
			return empty, fmt.Errorf("Go graph module inventory is not bound to the configured committed source: %w", err)
		}
		committedInventory, err := ri.CollectGoModuleInventory(ctx, identity)
		if err != nil {
			return empty, fmt.Errorf("revalidate committed Go module inventory: %w", err)
		}
		if committedInventory.Digest != spec.ModuleInventory.Digest {
			return empty, errors.New("Go graph module inventory differs from the configured committed source")
		}
	}
	executable, err := riAbsolutePath(root, executableArg)
	if err != nil {
		return empty, err
	}
	cacheDir := ""
	if cacheArg != "" {
		if !filepath.IsAbs(cacheArg) || filepath.Clean(cacheArg) != cacheArg {
			return empty, errors.New("cache directory must be an absolute clean path")
		}
		cacheDir, err = riAbsolutePath(root, cacheArg)
		if err != nil {
			return empty, err
		}
		volumeRoot := filepath.VolumeName(cacheDir) + string(filepath.Separator)
		if filepath.Clean(cacheDir) != cacheDir || cacheDir == volumeRoot {
			return empty, errors.New("cache directory must be a clean non-root path")
		}
	}

	// Read and bind the complete requested corpus before invoking the parser.
	// Sensitive and unsupported paths are rejected before any content is read.
	sources := make([]goGraphSource, 0, len(spec.Files))
	totalBytes := 0
	for _, file := range spec.Files {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		remaining := min(goGraphMaxBytes-totalBytes, goGraphMaxFileBytes)
		buffer := &goGraphSourceBuffer{limit: remaining}
		digest, err := repository.CopySource(ctx, identity, file.Path, buffer)
		if err != nil {
			return empty, fmt.Errorf("read committed Go source %q: %w", file.Path, err)
		}
		content := []byte(buffer.String())
		if digest.RepositoryID != source.RepositoryID || digest.Commit != source.Commit || digest.Path != file.Path || digest.Bytes != int64(len(content)) || len(content) > goGraphMaxBytes-totalBytes {
			return empty, errors.New("committed Go graph source binding or aggregate size mismatch")
		}
		totalBytes += len(content)
		sources = append(sources, goGraphSource{spec: file, digest: digest, bytes: content})
	}

	client := ri.Client{Executable: executable, ExecutableHash: executableHash}
	stream, err := client.OpenStream(ctx)
	if err != nil {
		return empty, err
	}
	defer stream.Close()
	graphInputs := make([]ri.GoGraphFileInput, 0, len(sources))
	observations := make([]goGraphSourceObservation, 0, len(sources))
	contextFiles := make([]taskcontext.File, 0, len(sources))
	for _, sourceFile := range sources {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		facts, err := stream.GoFileFacts(ctx, sourceFile.spec.Path, sourceFile.bytes, cacheDir)
		if err != nil {
			return empty, fmt.Errorf("parse committed Go source %q: %w", sourceFile.spec.Path, err)
		}
		binding, err := graphPackageBinding(sourceFile.spec, sourceFile.bytes, source.RepositoryID, spec.ModuleInventory)
		if err != nil {
			return empty, fmt.Errorf("derive package identity for committed Go source %q: %w", sourceFile.spec.Path, err)
		}
		graphInputs = append(graphInputs, ri.GoGraphFileInput{Facts: facts, Source: sourceFile.bytes, Package: binding})
		observations = append(observations, goGraphSourceObservation{Source: sourceFile.digest})
		contextFiles = append(contextFiles, taskcontext.File{Path: sourceFile.spec.Path, Hash: sourceFile.digest.SHA256, Content: sourceFile.bytes})
	}
	graph, err := ri.BuildGoEngineeringGraph(ri.GoGraphSnapshotInput{SourceID: source.RepositoryID, ProducerSHA256: executableHash, Files: graphInputs, Generators: spec.Generators, ModuleInventory: spec.ModuleInventory})
	if err != nil {
		return empty, err
	}
	sort.Slice(observations, func(i, j int) bool { return observations[i].Source.Path < observations[j].Source.Path })
	sort.Slice(contextFiles, func(i, j int) bool { return contextFiles[i].Path < contextFiles[j].Path })
	return builtGoGraph{
		result:       goGraphResult{Repository: source, Sources: observations, Graph: graph},
		contextFiles: contextFiles,
	}, nil
}

func readGoGraphSpec(path string) (goGraphSpec, error) {
	var spec goGraphSpec
	file, err := os.Open(path)
	if err != nil {
		return spec, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return spec, err
	}
	if !info.Mode().IsRegular() || info.Size() > goGraphSpecMaxBytes {
		return spec, errors.New("Go graph spec must be a regular file no larger than 1 MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(file, goGraphSpecMaxBytes+1))
	if err != nil || len(raw) > goGraphSpecMaxBytes {
		return spec, errors.New("Go graph spec exceeds 1 MiB or could not be read")
	}
	normal, err := canonical.Normalize(raw)
	if err != nil {
		return spec, fmt.Errorf("invalid Go graph spec encoding: %w", err)
	}
	if err := canonical.Decode(normal, &spec); err != nil {
		return spec, fmt.Errorf("invalid Go graph spec: %w", err)
	}
	return spec, nil
}

func validateGoGraphSpec(spec goGraphSpec) error {
	if len(spec.Files) == 0 || len(spec.Files) > goGraphMaxFiles || len(spec.Generators) > goGraphMaxFiles {
		return errors.New("Go graph spec must contain 1 to 256 files and bounded relations")
	}
	if spec.ModuleInventory != nil {
		if err := ri.ValidateGoModuleInventoryRecord(*spec.ModuleInventory); err != nil {
			return fmt.Errorf("Go graph spec module inventory is invalid: %w", err)
		}
	}
	paths := make(map[string]bool, len(spec.Files))
	for _, file := range spec.Files {
		if err := safepath.Relative(file.Path); err != nil || filepath.Ext(file.Path) != ".go" || len(file.Path) > 4096 {
			return errors.New("Go graph spec contains an invalid source path")
		}
		if !taskcontext.EligiblePath(file.Path) {
			return fmt.Errorf("Go graph spec path is ineligible for context: %q", file.Path)
		}
		if paths[file.Path] {
			return fmt.Errorf("Go graph spec repeats path %q", file.Path)
		}
		paths[file.Path] = true
		if spec.ModuleInventory == nil {
			if !validGoImportPathCLI(file.ImportPath) || !validGoImportPathCLI(file.ModulePath) || (file.ImportPath != file.ModulePath && !strings.HasPrefix(file.ImportPath, strings.TrimSuffix(file.ModulePath, "/")+"/")) {
				return fmt.Errorf("Go graph spec has invalid explicit package identity for %q", file.Path)
			}
		} else if (file.ImportPath == "") != (file.ModulePath == "") || (file.ImportPath != "" && (!validGoImportPathCLI(file.ImportPath) || !validGoImportPathCLI(file.ModulePath) || (file.ImportPath != file.ModulePath && !strings.HasPrefix(file.ImportPath, strings.TrimSuffix(file.ModulePath, "/")+"/")))) {
			return fmt.Errorf("Go graph spec has invalid optional package identity for module-backed file %q", file.Path)
		}
		if file.TestOfImportPath != "" && (!validGoImportPathCLI(file.TestOfImportPath) || !strings.HasSuffix(file.Path, "_test.go")) {
			return fmt.Errorf("Go graph spec has invalid external-test identity for %q", file.Path)
		}
	}
	for _, relation := range spec.Generators {
		if !paths[relation.GeneratorPath] || !paths[relation.GeneratedPath] || relation.GeneratorPath == relation.GeneratedPath || !strings.HasPrefix(relation.Directive, "//go:generate") || len(relation.Directive) > 4096 {
			return errors.New("Go graph spec generator relations must reference bounded corpus members")
		}
	}
	return nil
}

func graphPackageBinding(spec goGraphFileSpec, source []byte, sourceID string, inventory *ri.GoModuleInventory) (ri.GoPackageBinding, error) {
	if inventory == nil {
		return ri.GoPackageBinding{ImportPath: spec.ImportPath, ModulePath: spec.ModulePath, TestOfImportPath: spec.TestOfImportPath}, nil
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), spec.Path, source, parser.PackageClauseOnly)
	if err != nil || parsed == nil || parsed.Name == nil {
		return ri.GoPackageBinding{}, errors.New("package clause is unavailable")
	}
	binding, err := ri.DeclaredGoPackageBinding(*inventory, spec.Path, parsed.Name.Name)
	if err != nil {
		return ri.GoPackageBinding{}, err
	}
	if (spec.ImportPath != "" && spec.ImportPath != binding.ImportPath) || (spec.ModulePath != "" && spec.ModulePath != binding.ModulePath) || (spec.TestOfImportPath != "" && spec.TestOfImportPath != binding.TestOfImportPath) {
		return ri.GoPackageBinding{}, errors.New("explicit package identity differs from committed module ownership")
	}
	return binding, nil
}

func validGoImportPathCLI(value string) bool {
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\\:\t\r\n ") || strings.HasPrefix(value, "/") || path.Clean(value) != value {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}
