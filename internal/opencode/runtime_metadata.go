package opencode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/safepath"
)

// Metadata authorities, patch classifications, and violation codes bound how pinned patch parts are interpreted.
const (
	MetadataAuthorityReadOnly     = "READ_ONLY"
	MetadataAuthorityProposalOnly = "PROPOSAL_ONLY"

	PatchClassificationEmpty              = "EMPTY_PATCH"
	PatchClassificationWorkspaceMutation  = "WORKSPACE_MUTATION"
	PatchClassificationStateContamination = "ORCHESTRATION_STATE_CONTAMINATION"
	MetadataViolationReadOnlyAuthority    = "READ_ONLY_AUTHORITY_VIOLATION"
	MetadataViolationStateContamination   = "ORCHESTRATION_STATE_CONTAMINATION"
)

// RuntimeMetadataExpectation is immutable classification policy. It grants no
// file or tool authority; it only bounds how a pinned OpenCode patch part is
// interpreted after the provider turn has already completed.
type RuntimeMetadataExpectation struct {
	Version              int      `json:"version"`
	Authority            string   `json:"authority"`
	WorkspaceRoot        string   `json:"workspace_root"`
	KnownControllerPaths []string `json:"known_controller_paths"`
}

// PatchSnapshotReceipt preserves validated OpenCode snapshot metadata apart
// from semantic assistant text. Files retain wire order and spelling.
type PatchSnapshotReceipt struct {
	Version        int      `json:"version"`
	PartID         string   `json:"part_id"`
	MessageID      string   `json:"message_id"`
	SessionID      string   `json:"session_id"`
	SnapshotHash   string   `json:"snapshot_hash"`
	Files          []string `json:"files"`
	Classification string   `json:"classification"`
	Violation      string   `json:"violation,omitempty"`
	PartsSHA256    string   `json:"parts_sha256"`
	SHA256         string   `json:"sha256"`
}

// RuntimeMetadataViolation is a classified authority signal. It does not
// claim that the model wrote a file and never authorizes retry or fallback.
type RuntimeMetadataViolation struct{ Code string }

// Error returns the violation code, or the generic violation message when empty.
func (e *RuntimeMetadataViolation) Error() string {
	if e == nil || e.Code == "" {
		return "OpenCode runtime metadata violation"
	}
	return e.Code
}

func (e RuntimeMetadataExpectation) validate() error {
	if e.Version != 1 || (e.Authority != MetadataAuthorityReadOnly && e.Authority != MetadataAuthorityProposalOnly) || !cleanMetadataAbsolute(e.WorkspaceRoot) || e.KnownControllerPaths == nil || !slices.IsSortedFunc(e.KnownControllerPaths, compareMetadataPath) {
		return errors.New("invalid OpenCode runtime metadata expectation")
	}
	seen := map[string]bool{}
	for _, path := range e.KnownControllerPaths {
		key := metadataPathKey(path)
		if !cleanMetadataAbsolute(path) || seen[key] {
			return errors.New("invalid OpenCode controller metadata path")
		}
		seen[key] = true
	}
	return nil
}

// ValidateRuntimeMetadataExpectation validates immutable metadata policy.
func ValidateRuntimeMetadataExpectation(expected RuntimeMetadataExpectation) error {
	return expected.validate()
}

// ValidateRuntimeMetadataForResult enforces classified metadata before a
// semantic result may be projected. It performs no I/O.
func ValidateRuntimeMetadataForResult(expected *RuntimeMetadataExpectation, receipts []PatchSnapshotReceipt) error {
	if expected != nil && expected.validate() != nil {
		return errors.New("invalid OpenCode runtime metadata expectation")
	}
	if len(receipts) != 0 && expected == nil {
		return errors.New("OpenCode runtime metadata expectation required")
	}
	for _, receipt := range receipts {
		if ValidatePatchSnapshotReceipt(receipt) != nil || ValidatePatchSnapshotReceiptAgainstExpectation(*expected, receipt) != nil {
			return errors.New("invalid OpenCode runtime metadata receipt")
		}
		switch receipt.Classification {
		case PatchClassificationEmpty:
			continue
		case PatchClassificationWorkspaceMutation:
			return &RuntimeMetadataViolation{Code: MetadataViolationReadOnlyAuthority}
		case PatchClassificationStateContamination:
			return &RuntimeMetadataViolation{Code: MetadataViolationStateContamination}
		default:
			return errors.New("unknown OpenCode runtime metadata classification")
		}
	}
	return nil
}

func decodeRuntimeMetadataParts(raw []byte, assistant Assistant, expected *RuntimeMetadataExpectation) ([]PatchSnapshotReceipt, error) {
	if expected == nil {
		return nil, nil
	}
	if err := expected.validate(); err != nil {
		return nil, err
	}
	var parts []json.RawMessage
	if len(raw) > (1<<20)-10 || json.Unmarshal(raw, &parts) != nil || parts == nil || len(parts) > 4096 {
		return nil, errors.New("invalid OpenCode parts for runtime metadata")
	}
	partsSum := sha256.Sum256(raw)
	partsSHA256 := hex.EncodeToString(partsSum[:])
	receipts := make([]PatchSnapshotReceipt, 0, 1)
	seen := map[string]bool{}
	for _, rawPart := range parts {
		part, err := wireObject(rawPart)
		if err != nil {
			return nil, err
		}
		var id, session, message, kind string
		if field(part, "id", &id) != nil || field(part, "sessionID", &session) != nil || field(part, "messageID", &message) != nil || field(part, "type", &kind) != nil {
			return nil, errors.New("runtime metadata part identity missing")
		}
		if !locator(id) || seen[id] || session != assistant.Binding.SessionID || message != assistant.ID {
			return nil, errors.New("runtime metadata part identity mismatch or duplication")
		}
		seen[id] = true
		if kind != "patch" {
			continue
		}
		receipt, err := decodePatchSnapshotReceipt(part, *expected, partsSHA256)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, receipt)
	}
	return receipts, nil
}

func decodePatchSnapshotReceipt(part map[string]json.RawMessage, expected RuntimeMetadataExpectation, partsSHA256 string) (PatchSnapshotReceipt, error) {
	var receipt PatchSnapshotReceipt
	if len(part) != 6 || field(part, "id", &receipt.PartID) != nil || field(part, "messageID", &receipt.MessageID) != nil || field(part, "sessionID", &receipt.SessionID) != nil || field(part, "hash", &receipt.SnapshotHash) != nil || field(part, "files", &receipt.Files) != nil || receipt.Files == nil || !locator(receipt.PartID) || !snapshotHash(receipt.SnapshotHash) || safepath.RequireDigest(partsSHA256) != nil {
		return receipt, errors.New("invalid OpenCode patch snapshot metadata")
	}
	receipt.Version = 1
	receipt.PartsSHA256 = partsSHA256
	seen := map[string]bool{}
	kind := ""
	for _, path := range receipt.Files {
		key := metadataPathKey(path)
		if !cleanMetadataAbsolute(path) || seen[key] {
			return PatchSnapshotReceipt{}, errors.New("invalid or duplicate OpenCode patch path")
		}
		seen[key] = true
		classification, err := classifyPatchPath(expected, path)
		if err != nil {
			return PatchSnapshotReceipt{}, err
		}
		if kind != "" && kind != classification {
			return PatchSnapshotReceipt{}, errors.New("mixed OpenCode patch authority paths")
		}
		kind = classification
	}
	switch kind {
	case "":
		receipt.Classification = PatchClassificationEmpty
	case PatchClassificationStateContamination:
		receipt.Classification = kind
		receipt.Violation = MetadataViolationStateContamination
	case PatchClassificationWorkspaceMutation:
		receipt.Classification = kind
		receipt.Violation = MetadataViolationReadOnlyAuthority
	default:
		return PatchSnapshotReceipt{}, errors.New("unknown OpenCode patch classification")
	}
	digest, err := patchSnapshotReceiptDigest(receipt)
	if err != nil {
		return PatchSnapshotReceipt{}, err
	}
	receipt.SHA256 = digest
	return receipt, nil
}

// ValidatePatchSnapshotReceipt verifies a detached receipt without granting
// authority or deciding whether its classification permits result success.
func ValidatePatchSnapshotReceipt(receipt PatchSnapshotReceipt) error {
	want := receipt.SHA256
	if receipt.Version != 1 || !locator(receipt.PartID) || !locator(receipt.MessageID) || !locator(receipt.SessionID) || !snapshotHash(receipt.SnapshotHash) || receipt.Files == nil || len(receipt.Files) > 4096 || safepath.RequireDigest(receipt.PartsSHA256) != nil || safepath.RequireDigest(want) != nil {
		return errors.New("invalid patch snapshot receipt")
	}
	seen := map[string]bool{}
	for _, path := range receipt.Files {
		key := metadataPathKey(path)
		if !cleanMetadataAbsolute(path) || seen[key] {
			return errors.New("invalid or duplicate patch snapshot receipt path")
		}
		seen[key] = true
	}
	switch receipt.Classification {
	case PatchClassificationEmpty:
		if len(receipt.Files) != 0 || receipt.Violation != "" {
			return errors.New("invalid empty patch snapshot receipt")
		}
	case PatchClassificationWorkspaceMutation:
		if len(receipt.Files) == 0 || receipt.Violation != MetadataViolationReadOnlyAuthority {
			return errors.New("invalid workspace mutation receipt")
		}
	case PatchClassificationStateContamination:
		if len(receipt.Files) == 0 || receipt.Violation != MetadataViolationStateContamination {
			return errors.New("invalid orchestration state receipt")
		}
	default:
		return errors.New("invalid patch snapshot classification")
	}
	digest, err := patchSnapshotReceiptDigest(receipt)
	if err != nil || digest != want {
		return errors.New("patch snapshot receipt digest mismatch")
	}
	return nil
}

// ValidatePatchSnapshotReceiptAgainstExpectation recomputes the classification
// and violation from immutable authority policy; receipt labels are not trusted.
func ValidatePatchSnapshotReceiptAgainstExpectation(expected RuntimeMetadataExpectation, receipt PatchSnapshotReceipt) error {
	if expected.validate() != nil || ValidatePatchSnapshotReceipt(receipt) != nil {
		return errors.New("invalid patch snapshot receipt expectation")
	}
	wantClassification := PatchClassificationEmpty
	wantViolation := ""
	for _, path := range receipt.Files {
		classification, err := classifyPatchPath(expected, path)
		if err != nil {
			return err
		}
		if wantClassification != PatchClassificationEmpty && wantClassification != classification {
			return errors.New("mixed OpenCode patch authority paths")
		}
		wantClassification = classification
	}
	if wantClassification == PatchClassificationWorkspaceMutation {
		wantViolation = MetadataViolationReadOnlyAuthority
	} else if wantClassification == PatchClassificationStateContamination {
		wantViolation = MetadataViolationStateContamination
	}
	if receipt.Classification != wantClassification || receipt.Violation != wantViolation {
		return errors.New("OpenCode patch classification differs from expectation")
	}
	return nil
}

func patchSnapshotReceiptDigest(receipt PatchSnapshotReceipt) (string, error) {
	receipt.SHA256 = ""
	return canonical.Hash("harness.opencode-runtime-patch-snapshot.v1", receipt)
}

func classifyPatchPath(expected RuntimeMetadataExpectation, path string) (string, error) {
	key := metadataPathKey(path)
	index, found := slices.BinarySearchFunc(expected.KnownControllerPaths, path, compareMetadataPath)
	if found && metadataPathKey(expected.KnownControllerPaths[index]) == key {
		return PatchClassificationStateContamination, nil
	}
	cleanRoot := filepath.Clean(filepath.FromSlash(expected.WorkspaceRoot))
	cleanPath := filepath.Clean(filepath.FromSlash(path))
	rel, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("OpenCode patch path escapes workspace authority")
	}
	return PatchClassificationWorkspaceMutation, nil
}

func cleanMetadataAbsolute(path string) bool {
	if strings.ContainsRune(path, 0) {
		return false
	}
	native := filepath.FromSlash(path)
	volume := filepath.VolumeName(native)
	remainder := strings.TrimPrefix(native, volume)
	if strings.ContainsRune(remainder, ':') {
		return false
	}
	return len(path) > 0 && len(path) <= 4096 && filepath.IsAbs(native) && filepath.Clean(native) == native
}

func metadataPathKey(path string) string {
	key := filepath.Clean(filepath.FromSlash(path))
	if runtime.GOOS == "windows" {
		return strings.ToLower(key)
	}
	return key
}

func compareMetadataPath(left, right string) int {
	return strings.Compare(metadataPathKey(left), metadataPathKey(right))
}

// NewRuntimeMetadataExpectation constructs deterministic exact path policy.
func NewRuntimeMetadataExpectation(authority, workspaceRoot string, controllerPaths []string) (RuntimeMetadataExpectation, error) {
	expected := RuntimeMetadataExpectation{Version: 1, Authority: authority, WorkspaceRoot: workspaceRoot, KnownControllerPaths: append([]string{}, controllerPaths...)}
	sort.Slice(expected.KnownControllerPaths, func(i, j int) bool {
		return compareMetadataPath(expected.KnownControllerPaths[i], expected.KnownControllerPaths[j]) < 0
	})
	if err := expected.validate(); err != nil {
		return RuntimeMetadataExpectation{}, err
	}
	return expected, nil
}

func snapshotHash(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
