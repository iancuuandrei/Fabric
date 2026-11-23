package ri

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

const (
	// GoModuleInventoryVersionCommitted identifies inventories read from the
	// immutable Git tree. Its canonical shape and digest domain are stable.
	GoModuleInventoryVersionCommitted = 1
	// GoModuleInventoryVersionCandidate identifies inventories read from one
	// exact admitted worktree candidate.
	GoModuleInventoryVersionCandidate = 2
	goModuleInventoryVersion          = GoModuleInventoryVersionCommitted
	goModuleMaxRecords                = 128
	goModuleMaxFileBytes              = 128 << 10
	goModuleMaxTotalBytes             = 1 << 20
)

// GoModuleInventory is a bounded observation of module-related files in one
// committed tree or one exact candidate. It records declarations; it does not
// claim that a Go command would select a workspace, replacement, vendor tree,
// or dependency version.
type GoModuleInventory struct {
	Version                int                     `json:"version"`
	RepositoryID           string                  `json:"repository_id"`
	Commit                 string                  `json:"commit"`
	Tree                   string                  `json:"tree"`
	Coverage               string                  `json:"coverage"`
	ObservedManifestCount  int                     `json:"observed_manifest_count"`
	TruncatedManifestCount int                     `json:"truncated_manifest_count"`
	Files                  []GoManifestObservation `json:"files"`
	Omissions              []GoManifestOmission    `json:"omissions"`
	CandidateID            string                  `json:"candidate_id,omitempty"`
	CandidateFilesHash     string                  `json:"candidate_files_hash,omitempty"`
	BaseInventoryDigest    string                  `json:"base_inventory_digest,omitempty"`
	Digest                 string                  `json:"digest"`
}

// GoManifestOmission records a bounded path-only manifest omission. Sensitive
// and unsafe paths are redacted; omissions never contain blob or content hashes.
type GoManifestOmission struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// GoManifestObservation binds a parsed declaration or omission to its exact
// committed Git blob (v1) or candidate file hash (v2). Candidate observations
// never invent Git blob IDs.
type GoManifestObservation struct {
	Kind       string                `json:"kind"`
	Path       string                `json:"path"`
	Blob       string                `json:"blob"`
	Bytes      int64                 `json:"bytes"`
	SHA256     string                `json:"sha256,omitempty"`
	Status     string                `json:"status"`
	ModulePath string                `json:"module_path,omitempty"`
	GoVersion  string                `json:"go_version,omitempty"`
	Toolchain  string                `json:"toolchain,omitempty"`
	Requires   []GoModuleRequirement `json:"requires,omitempty"`
	Replaces   []GoModuleReplacement `json:"replaces,omitempty"`
	Uses       []GoWorkspaceUse      `json:"uses,omitempty"`
}

// GoModuleRequirement is one exact go.mod require declaration.
type GoModuleRequirement struct {
	Path     string `json:"path"`
	Version  string `json:"version"`
	Indirect bool   `json:"indirect"`
}

// GoModuleReplacement is one exact go.mod or go.work replace declaration.
type GoModuleReplacement struct {
	OldPath     string `json:"old_path"`
	OldVersion  string `json:"old_version,omitempty"`
	NewPath     string `json:"new_path"`
	NewVersion  string `json:"new_version,omitempty"`
	Local       bool   `json:"local"`
	LocalRoot   string `json:"local_root,omitempty"`
	LocalStatus string `json:"local_status,omitempty"`
}

// GoWorkspaceUse records a go.work use declaration and safe lexical root.
type GoWorkspaceUse struct {
	DeclaredPath       string `json:"declared_path"`
	DeclaredModulePath string `json:"declared_module_path,omitempty"`
	Root               string `json:"root,omitempty"`
	Status             string `json:"status"`
}

// GoModuleOwnership states whether a unique deepest module declaration owns a
// source path. SourceLocal fallback preserves existing v1 package identities;
// it is not a guessed module path.
type GoModuleOwnership struct {
	Status     string `json:"status"`
	ModuleRoot string `json:"module_root,omitempty"`
	ModulePath string `json:"module_path,omitempty"`
	ImportPath string `json:"import_path,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type goModuleBuffer struct {
	bytes.Buffer
	limit int64
}

// Write appends bytes only while the preflighted manifest size permits.
func (b *goModuleBuffer) Write(p []byte) (int, error) {
	if int64(len(p)) > b.limit-int64(b.Len()) {
		return 0, errors.New("module manifest exceeded admitted size")
	}
	return b.Buffer.Write(p)
}

type goModuleBufferSink struct{ buffer *goModuleBuffer }

// Write forwards selected manifest bytes to its bounded buffer.
func (s goModuleBufferSink) Write(p []byte) (int, error) { return s.buffer.Write(p) }

// Close does not take ownership of the shared manifest buffer.
func (goModuleBufferSink) Close() error { return nil }

func sortGoManifestOmissions(omissions []GoManifestOmission) {
	sort.Slice(omissions, func(i, j int) bool {
		left, right := omissions[i], omissions[j]
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.Reason < right.Reason
	})
}

// CollectGoModuleInventory observes exact committed go.mod/go.work/vendor
// metadata using a bounded tree walk and one selected-source batch. It never
// invokes go, resolves modules, or reads working-tree files.
func CollectGoModuleInventory(ctx context.Context, identity repository.Identity) (GoModuleInventory, error) {
	var result GoModuleInventory
	repositoryID, err := identity.ID()
	if err != nil {
		return result, err
	}
	result = GoModuleInventory{Version: goModuleInventoryVersion, RepositoryID: repositoryID, Commit: identity.Commit, Tree: identity.Tree, Coverage: "complete", Files: []GoManifestObservation{}, Omissions: []GoManifestOmission{}}
	entries := make([]repository.SourceEntry, 0, 16)
	truncated := 0
	err = repository.VisitSource(ctx, identity, func(entry repository.SourceEntry) error {
		kind := goManifestKind(entry.Path)
		if kind == "" {
			return nil
		}
		result.ObservedManifestCount++
		if len(result.Files)+len(result.Omissions) >= goModuleMaxRecords {
			truncated++
			return nil
		}
		if !taskcontext.EligiblePath(entry.Path) {
			result.Omissions = append(result.Omissions, GoManifestOmission{Kind: kind, Path: "[redacted]", Reason: "sensitive_path"})
			result.Coverage = "partial"
			return nil
		}
		observation := GoManifestObservation{Kind: kind, Path: entry.Path, Blob: entry.Object, Status: "pending"}
		if safepath.Relative(entry.Path) != nil {
			result.Omissions = append(result.Omissions, GoManifestOmission{Kind: kind, Path: "[redacted]", Reason: "unsafe_path"})
			result.Coverage = "partial"
			return nil
		}
		if err := safepath.Writable(entry.Path); err != nil {
			result.Omissions = append(result.Omissions, GoManifestOmission{Kind: kind, Path: entry.Path, Reason: "protected_path"})
			result.Coverage = "partial"
			return nil
		}
		if entry.Kind != "file" {
			result.Omissions = append(result.Omissions, GoManifestOmission{Kind: kind, Path: entry.Path, Reason: "unsupported_kind"})
			result.Coverage = "partial"
			return nil
		}
		result.Files = append(result.Files, observation)
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return GoModuleInventory{}, err
	}
	result.TruncatedManifestCount = truncated
	if truncated > 0 {
		result.Coverage = "partial"
	}
	if len(entries) == 0 {
		sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
		sortGoManifestOmissions(result.Omissions)
		if err := finalizeGoModuleInventory(&result); err != nil {
			return GoModuleInventory{}, err
		}
		return result, nil
	}

	buffers := make(map[string]*goModuleBuffer, len(entries))
	used := int64(0)
	byPath := make(map[string]int, len(result.Files))
	for index := range result.Files {
		byPath[result.Files[index].Path] = index
	}
	err = repository.CopySelectedSourceBatch(ctx, identity, entries, func(entry repository.SourceEntry, size int64) (io.WriteCloser, error) {
		observation := &result.Files[byPath[entry.Path]]
		observation.Bytes = size
		if size > goModuleMaxFileBytes {
			observation.Status = "oversized"
			result.Coverage = "partial"
			return nil, nil
		}
		if size > goModuleMaxTotalBytes-used {
			observation.Status = "budget_omitted"
			result.Coverage = "partial"
			return nil, nil
		}
		used += size
		buffer := &goModuleBuffer{limit: size}
		buffers[entry.Path] = buffer
		return goModuleBufferSink{buffer}, nil
	}, func(entry repository.SourceEntry, digest *repository.SourceDigest) error {
		index, ok := byPath[entry.Path]
		if !ok {
			return errors.New("manifest batch returned an unselected path")
		}
		observation := &result.Files[index]
		if digest == nil {
			return nil
		}
		buffer := buffers[entry.Path]
		if buffer == nil || int64(buffer.Len()) != digest.Bytes {
			return errors.New("manifest batch content is incomplete")
		}
		observation.Bytes = digest.Bytes
		observation.SHA256 = digest.SHA256
		if !utf8.Valid(buffer.Bytes()) {
			observation.Status = "invalid_utf8"
			result.Coverage = "partial"
			return nil
		}
		if err := parseGoManifest(observation, buffer.Bytes()); err != nil {
			observation.Status = "invalid"
			result.Coverage = "partial"
			return nil
		}
		observation.Status = "parsed"
		return nil
	})
	if err != nil {
		return GoModuleInventory{}, err
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	sortGoManifestOmissions(result.Omissions)
	if err := finalizeGoModuleInventory(&result); err != nil {
		return GoModuleInventory{}, err
	}
	return result, nil
}

func goManifestKind(name string) string {
	// Classify using either separator so a committed Windows-style path is
	// retained as an unsafe manifest observation instead of disappearing.
	portable := strings.ReplaceAll(name, "\\", "/")
	base := path.Base(portable)
	switch {
	case base == "go.mod":
		return "go_mod"
	case base == "go.work":
		return "go_work"
	case strings.HasSuffix(portable, "/vendor/modules.txt"):
		return "vendor_modules"
	default:
		return ""
	}
}

func parseGoManifest(observation *GoManifestObservation, data []byte) error {
	switch observation.Kind {
	case "go_mod":
		file, err := modfile.Parse(observation.Path, data, nil)
		if err != nil || file.Module == nil || file.Module.Mod.Path == "" {
			return errors.New("invalid go.mod")
		}
		observation.ModulePath = file.Module.Mod.Path
		if file.Go != nil {
			observation.GoVersion = file.Go.Version
		}
		if file.Toolchain != nil {
			observation.Toolchain = file.Toolchain.Name
		}
		observation.Requires = make([]GoModuleRequirement, 0, len(file.Require))
		for _, req := range file.Require {
			observation.Requires = append(observation.Requires, GoModuleRequirement{Path: req.Mod.Path, Version: req.Mod.Version, Indirect: req.Indirect})
		}
		observation.Replaces = make([]GoModuleReplacement, 0, len(file.Replace))
		for _, replace := range file.Replace {
			observation.Replaces = append(observation.Replaces, goReplacement(observation.Path, replace.Old.Path, replace.Old.Version, replace.New.Path, replace.New.Version))
		}
	case "go_work":
		file, err := modfile.ParseWork(observation.Path, data, nil)
		if err != nil {
			return errors.New("invalid go.work")
		}
		if file.Go != nil {
			observation.GoVersion = file.Go.Version
		}
		if file.Toolchain != nil {
			observation.Toolchain = file.Toolchain.Name
		}
		observation.Uses = make([]GoWorkspaceUse, 0, len(file.Use))
		for _, use := range file.Use {
			normalized := normalizeGoWorkspaceUse(observation.Path, use.Path)
			normalized.DeclaredModulePath = use.ModulePath
			observation.Uses = append(observation.Uses, normalized)
		}
		observation.Replaces = make([]GoModuleReplacement, 0, len(file.Replace))
		for _, replace := range file.Replace {
			observation.Replaces = append(observation.Replaces, goReplacement(observation.Path, replace.Old.Path, replace.Old.Version, replace.New.Path, replace.New.Version))
		}
	case "vendor_modules":
		// The exact file digest and size establish committed vendor metadata
		// presence only; this helper does not parse or activate vendor mode.
	default:
		return errors.New("unknown Go manifest kind")
	}
	return nil
}

func goReplacement(manifestPath, oldPath, oldVersion, newPath, newVersion string) GoModuleReplacement {
	local := newVersion == ""
	replacement := GoModuleReplacement{OldPath: oldPath, OldVersion: oldVersion, NewPath: newPath, NewVersion: newVersion, Local: local}
	if local {
		normalized := normalizeGoWorkspaceUse(manifestPath, newPath)
		replacement.LocalRoot = normalized.Root
		replacement.LocalStatus = normalized.Status
	}
	return replacement
}

func normalizeGoWorkspaceUse(workFile, declared string) GoWorkspaceUse {
	use := GoWorkspaceUse{DeclaredPath: declared, Status: "unresolved_declaration"}
	if strings.ContainsRune(declared, '\\') || strings.ContainsRune(declared, ':') || strings.HasPrefix(declared, "/") {
		use.Status = "external_or_invalid_path"
		return use
	}
	root := path.Clean(path.Join(path.Dir(workFile), declared))
	if root == "." {
		root = ""
	}
	if root == ".." || strings.HasPrefix(root, "../") || safepath.Relative(path.Join(root, "go.mod")) != nil {
		use.Status = "external_or_invalid_path"
		return use
	}
	use.Root = root
	use.Status = "declared_repository_path"
	return use
}

func finalizeGoModuleInventory(inventory *GoModuleInventory) error {
	copyValue := *inventory
	copyValue.Digest = ""
	data, err := canonical.Bytes(copyValue)
	if err != nil {
		return err
	}
	if len(data) > canonical.MaxBytes {
		return errors.New("Go module inventory exceeds canonical size bound")
	}
	// Keep committed v1 bytes stable; candidate records use a separate domain.
	domain := "harness.ri.go-module-inventory.v1"
	if inventory.Version == GoModuleInventoryVersionCandidate {
		domain = "harness.ri.go-module-inventory.v2"
	}
	inventory.Digest, err = canonical.Hash(domain, copyValue)
	return err
}

// ValidateGoModuleInventory checks canonical structure/digest and binding to
// the supplied immutable repository identity. It performs no Git or Go command.
func ValidateGoModuleInventory(inventory GoModuleInventory, identity repository.Identity) error {
	if inventory.Version != GoModuleInventoryVersionCommitted || inventory.CandidateID != "" || inventory.CandidateFilesHash != "" || inventory.BaseInventoryDigest != "" {
		return errors.New("committed Go module inventory required")
	}
	repositoryID, err := identity.ID()
	if err != nil {
		return err
	}
	if err := ValidateGoModuleInventoryRecord(inventory); err != nil {
		return err
	}
	if inventory.RepositoryID != repositoryID || inventory.Commit != identity.Commit || inventory.Tree != identity.Tree {
		return errors.New("Go module inventory source or counts are invalid")
	}
	return nil
}

// ValidateGoModuleInventoryRecord validates a serialized v1 committed or v2
// candidate inventory without filesystem access. Use ValidateGoModuleInventory
// for exact committed identity, or ValidateCandidateGoModuleInventory for an
// exact candidate and its committed base inventory.
func ValidateGoModuleInventoryRecord(inventory GoModuleInventory) error {
	candidate := inventory.Version == GoModuleInventoryVersionCandidate
	if (inventory.Version != GoModuleInventoryVersionCommitted && !candidate) || inventory.RepositoryID == "" || inventory.Commit == "" || inventory.Tree == "" || (inventory.Coverage != "complete" && inventory.Coverage != "partial") || inventory.ObservedManifestCount < len(inventory.Files)+len(inventory.Omissions) || inventory.TruncatedManifestCount < 0 || inventory.TruncatedManifestCount != inventory.ObservedManifestCount-len(inventory.Files)-len(inventory.Omissions) {
		return errors.New("Go module inventory source or counts are invalid")
	}
	if candidate {
		if safepath.RequireDigest(inventory.CandidateID) != nil || safepath.RequireDigest(inventory.CandidateFilesHash) != nil || safepath.RequireDigest(inventory.BaseInventoryDigest) != nil {
			return errors.New("candidate Go module inventory binding is invalid")
		}
	} else if inventory.CandidateID != "" || inventory.CandidateFilesHash != "" || inventory.BaseInventoryDigest != "" {
		return errors.New("committed Go module inventory contains candidate binding")
	}
	if inventory.TruncatedManifestCount > 0 && inventory.Coverage != "partial" {
		return errors.New("truncated Go module inventory cannot be complete")
	}
	previous := ""
	readBytes := int64(0)
	incomplete := inventory.TruncatedManifestCount > 0 || len(inventory.Omissions) > 0
	for _, file := range inventory.Files {
		blobValid := file.Blob != "" && len(file.Blob) == len(inventory.Commit) && strings.Trim(file.Blob, "0123456789abcdef") == ""
		if candidate && file.Blob == "" {
			blobValid = true
		}
		if file.Kind != "go_mod" && file.Kind != "go_work" && file.Kind != "vendor_modules" || goManifestKind(file.Path) != file.Kind || !blobValid || candidate && file.Blob != "" || file.Bytes < 0 || !goManifestStatusValid(file.Status) || (previous != "" && file.Path <= previous) {
			return errors.New("Go module inventory file record is invalid")
		}
		previous = file.Path
		if safepath.Relative(file.Path) != nil {
			if file.Status != "unsafe_path" {
				return errors.New("unsafe manifest path lacks explicit omission status")
			}
		}
		if file.SHA256 != "" {
			fullContent := file.Status == "parsed" || file.Status == "invalid" || file.Status == "invalid_utf8"
			candidateFingerprint := candidate && (file.Status == "oversized" || file.Status == "budget_omitted")
			if safepath.RequireDigest(file.SHA256) != nil || !fullContent && !candidateFingerprint {
				return errors.New("manifest content digest/status is invalid")
			}
			if fullContent {
				readBytes += file.Bytes
			}
		}
		if file.Status == "parsed" || file.Status == "invalid" || file.Status == "invalid_utf8" {
			if file.SHA256 == "" || file.Bytes > goModuleMaxFileBytes || candidate && file.Blob != "" {
				return errors.New("read manifest is missing bounded content evidence")
			}
		}
		if file.Status == "oversized" && file.Bytes <= goModuleMaxFileBytes || file.Status == "budget_omitted" && file.Bytes > goModuleMaxFileBytes || candidate && (file.Status == "oversized" || file.Status == "budget_omitted") && file.SHA256 == "" {
			return errors.New("manifest omission status does not match its size")
		}
		if file.Status == "unsafe_path" || file.Status == "unsupported_kind" || file.Status == "protected_path" || file.Status == "sensitive_path" {
			return errors.New("path-only manifest omission must not be a content observation")
		}
		if file.Status == "parsed" {
			if file.Kind == "go_mod" && file.ModulePath == "" || file.Kind == "vendor_modules" && (file.ModulePath != "" || len(file.Requires)+len(file.Replaces)+len(file.Uses) != 0) {
				return errors.New("parsed manifest declarations are invalid")
			}
			if len(file.Requires)+len(file.Replaces)+len(file.Uses) > 1<<16 {
				return errors.New("manifest declaration count exceeds bound")
			}
			for _, use := range file.Uses {
				modulePath := use.DeclaredModulePath
				normalized := normalizeGoWorkspaceUse(file.Path, use.DeclaredPath)
				normalized.DeclaredModulePath = modulePath
				if use != normalized {
					return errors.New("workspace use declaration normalization mismatch")
				}
			}
			for _, replacement := range file.Replaces {
				if replacement.Local != (replacement.NewVersion == "") {
					return errors.New("replacement local classification mismatch")
				}
				if replacement.Local {
					normalized := normalizeGoWorkspaceUse(file.Path, replacement.NewPath)
					if replacement.LocalRoot != normalized.Root || replacement.LocalStatus != normalized.Status {
						return errors.New("local replacement path normalization mismatch")
					}
				} else if replacement.LocalRoot != "" || replacement.LocalStatus != "" {
					return errors.New("versioned replacement contains local path metadata")
				}
			}
		} else if file.ModulePath != "" || file.GoVersion != "" || file.Toolchain != "" || len(file.Requires)+len(file.Replaces)+len(file.Uses) != 0 {
			return errors.New("unparsed manifest contains declarations")
		}
		if file.Status != "parsed" {
			incomplete = true
			if inventory.Coverage != "partial" {
				return errors.New("omitted or invalid manifest lacks partial coverage")
			}
		}
	}
	var previousOmission *GoManifestOmission
	for index := range inventory.Omissions {
		omission := &inventory.Omissions[index]
		if !goManifestKindMatchesKind(omission.Kind) || !goManifestOmissionReasonValid(omission.Reason) {
			return errors.New("Go manifest omission kind or reason is invalid")
		}
		if omission.Reason == "sensitive_path" || omission.Reason == "unsafe_path" {
			if omission.Path != "[redacted]" {
				return errors.New("sensitive or unsafe manifest path is not redacted")
			}
		} else {
			if safepath.Relative(omission.Path) != nil || goManifestKind(omission.Path) != omission.Kind {
				return errors.New("manifest omission path is invalid")
			}
			if omission.Reason == "protected_path" && safepath.Writable(omission.Path) == nil {
				return errors.New("protected manifest omission is not protected")
			}
			if omission.Reason == "unsupported_kind" && safepath.Writable(omission.Path) != nil {
				return errors.New("unsupported manifest should be classified as protected")
			}
		}
		if previousOmission != nil && goManifestOmissionLess(*omission, *previousOmission) {
			return errors.New("Go manifest omissions are not canonically ordered")
		}
		previousOmission = omission
		incomplete = true
	}
	if readBytes > goModuleMaxTotalBytes || incomplete && inventory.Coverage != "partial" || !incomplete && inventory.Coverage != "complete" {
		return errors.New("Go module inventory coverage/byte accounting is invalid")
	}
	expected := inventory
	if err := finalizeGoModuleInventory(&expected); err != nil {
		return err
	}
	if inventory.Digest == "" || expected.Digest != inventory.Digest {
		return errors.New("Go module inventory digest mismatch")
	}
	return nil
}

func goManifestStatusValid(status string) bool {
	switch status {
	case "parsed", "invalid", "invalid_utf8", "oversized", "budget_omitted":
		return true
	default:
		return false
	}
}

func goManifestKindMatchesKind(kind string) bool {
	return kind == "go_mod" || kind == "go_work" || kind == "vendor_modules"
}

func goManifestOmissionReasonValid(reason string) bool {
	switch reason {
	case "sensitive_path", "unsafe_path", "protected_path", "unsupported_kind":
		return true
	default:
		return false
	}
}

func goManifestOmissionLess(left, right GoManifestOmission) bool {
	if left.Path != right.Path {
		return left.Path < right.Path
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	return left.Reason < right.Reason
}

// GoModuleOwnershipForPath derives a package import path only from a unique
// deepest valid go.mod declaration. Partial deeper module evidence shadows any
// ancestor, and inventory truncation makes all ownership ambiguous.
func GoModuleOwnershipForPath(inventory GoModuleInventory, sourcePath string) (GoModuleOwnership, error) {
	if ValidateGoModuleInventoryRecord(inventory) != nil || safepath.Relative(sourcePath) != nil {
		return GoModuleOwnership{}, errors.New("invalid inventory or source path")
	}
	if inventory.TruncatedManifestCount > 0 {
		return GoModuleOwnership{Status: "ambiguous", Reason: "manifest_inventory_truncated"}, nil
	}
	for _, omission := range inventory.Omissions {
		if omission.Kind == "go_mod" {
			return GoModuleOwnership{Status: "ambiguous", Reason: "module_manifest_omitted"}, nil
		}
	}
	fileDir := path.Dir(sourcePath)
	if fileDir == "." {
		fileDir = ""
	}
	var owner *GoManifestObservation
	for index := range inventory.Files {
		manifest := &inventory.Files[index]
		if manifest.Kind != "go_mod" {
			continue
		}
		root := path.Dir(manifest.Path)
		if root == "." {
			root = ""
		}
		if !goPathContains(root, fileDir) {
			continue
		}
		if manifest.Status != "parsed" {
			return GoModuleOwnership{Status: "ambiguous", Reason: "containing_module_manifest_incomplete"}, nil
		}
		if owner == nil || len(root) > len(path.Dir(owner.Path)) {
			owner = manifest
		} else if len(root) == len(path.Dir(owner.Path)) && root != path.Dir(owner.Path) {
			return GoModuleOwnership{Status: "ambiguous", Reason: "multiple_deepest_module_roots"}, nil
		}
	}
	if owner == nil {
		return GoModuleOwnership{Status: "source_local_fallback", Reason: "no_containing_module_manifest"}, nil
	}
	root := path.Dir(owner.Path)
	if root == "." {
		root = ""
	}
	relativeDir := strings.TrimPrefix(fileDir, root)
	relativeDir = strings.TrimPrefix(relativeDir, "/")
	importPath := owner.ModulePath
	if relativeDir != "" && relativeDir != "." {
		importPath += "/" + relativeDir
	}
	if err := module.CheckImportPath(importPath); err != nil {
		return GoModuleOwnership{Status: "ambiguous", Reason: "derived_import_path_invalid"}, nil
	}
	return GoModuleOwnership{Status: "declared_module", ModuleRoot: root, ModulePath: owner.ModulePath, ImportPath: importPath}, nil
}

func goPathContains(root, candidate string) bool {
	if root == "" {
		return true
	}
	return candidate == root || strings.HasPrefix(candidate, root+"/")
}
