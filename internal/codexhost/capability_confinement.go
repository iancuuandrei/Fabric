package codexhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/codexrpc"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/safepath"
)

const (
	CapabilityConfinementRequired   = "required"
	CapabilityConfinementUnverified = "UNVERIFIED"
	CapabilityConfinementR17        = "R17_REGISTRY_ATTESTED"
	CapabilityConfinementR19        = "R19_REGISTRY_TOPOLOGY_ATTESTED"
	attestationKind                 = "codex-registry-confinement-r17"
	attestationTopologyKind         = "codex-registry-confinement-topology-r19"
	maxAttestationLifetime          = 24 * time.Hour
)

var ErrCapabilityConfinementUnverified = errors.New("CAPABILITY_CONFINEMENT_UNVERIFIED")

type CapabilityConfinementDecision struct {
	Version       int    `json:"version"`
	Requested     string `json:"requested"`
	Observed      string `json:"observed"`
	Admitted      bool   `json:"admitted"`
	Reason        string `json:"reason"`
	AttestationID string `json:"attestation_id,omitempty"`
}

type CapabilityConfinementError struct {
	Decision CapabilityConfinementDecision
	Launch   Launch
	Receipt  Receipt
}

func (e *CapabilityConfinementError) Error() string { return e.Decision.Reason }
func (e *CapabilityConfinementError) Unwrap() error { return ErrCapabilityConfinementUnverified }

type CapabilityConfinementAttestation struct {
	ManifestPath   string
	ExpectedSHA256 string
	Profile        runtime.Profile
	DynamicTools   []any
}

type attestationEvidence struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type attestationCheck struct {
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
	NoChild bool   `json:"no_child"`
}

type attestedProfile struct {
	Profile                     runtime.Profile `json:"profile"`
	DynamicToolsSHA256          string          `json:"dynamic_tools_sha256"`
	PreparedToolInventorySHA256 string          `json:"prepared_tool_inventory_sha256"`
}

type attestationManifest struct {
	Version                   int                   `json:"version"`
	Kind                      string                `json:"kind"`
	Verdict                   string                `json:"verdict"`
	IssuedAt                  string                `json:"issued_at"`
	ExpiresAt                 string                `json:"expires_at"`
	BinarySHA256              string                `json:"binary_sha256"`
	CLIIdentity               string                `json:"cli_identity"`
	CatalogFile               string                `json:"catalog_file"`
	CatalogSHA256             string                `json:"catalog_sha256"`
	SelectedModelRecordSHA256 string                `json:"selected_model_record_sha256"`
	LaunchConfigSHA256        string                `json:"launch_config_sha256"`
	LaunchControlsSHA256      string                `json:"launch_controls_sha256"`
	Profiles                  []attestedProfile     `json:"profiles"`
	AllowedCheck              attestationCheck      `json:"allowed_check"`
	ForbiddenChecks           []attestationCheck    `json:"forbidden_checks"`
	QualificationFile         string                `json:"qualification_file"`
	Evidence                  []attestationEvidence `json:"evidence"`
}

type attestationTopologyEntry struct {
	Profile            runtime.Profile `json:"profile"`
	DynamicToolsSHA256 string          `json:"dynamic_tools_sha256"`
	LeafFile           string          `json:"leaf_file"`
	LeafSHA256         string          `json:"leaf_sha256"`
}

type attestationTopology struct {
	Version   int                        `json:"version"`
	Kind      string                     `json:"kind"`
	IssuedAt  string                     `json:"issued_at"`
	ExpiresAt string                     `json:"expires_at"`
	Entries   []attestationTopologyEntry `json:"entries"`
}

type validatedAttestation struct {
	id            string
	manifestSHA   string
	manifest      attestationManifest
	catalogSource string
	expires       time.Time
	observed      string
}

func AttestationBindingID(manifestSHA256, launchID string) (string, error) {
	if safepath.RequireDigest(manifestSHA256) != nil || safepath.RequireDigest(launchID) != nil {
		return "", errors.New("invalid capability attestation binding")
	}
	return canonical.Hash("harness.codex-confinement-attestation.r17.v1", struct {
		ManifestSHA256 string `json:"manifest_sha256"`
		LaunchID       string `json:"launch_id"`
	}{manifestSHA256, launchID})
}

func unverified(requested string) (CapabilityConfinementDecision, error) {
	decision := CapabilityConfinementDecision{Version: 1, Requested: requested, Observed: CapabilityConfinementUnverified, Reason: ErrCapabilityConfinementUnverified.Error()}
	return decision, &CapabilityConfinementError{Decision: decision}
}

func DecideCapabilityConfinement(requested string) (CapabilityConfinementDecision, error) {
	if requested == "" {
		return CapabilityConfinementDecision{}, nil
	}
	return unverified(requested)
}

func DynamicToolsHash(tools []any) (string, error) {
	return canonical.Hash("harness.codex-dynamic-tools.v1", tools)
}

func LaunchConfigHash() string { return digest(configuration()) }

func SelectedModelRecordHash(record any) (string, error) {
	return canonical.Hash("harness.codex-model-record.r17.v1", record)
}

// SelectedModelMetadataHash is the raw, pretty-printed metadata identity
// emitted by the qualification probe. It is distinct from the canonical
// selected-record hash in the manifest, and is checked against every raw
// result so a capture cannot silently switch model metadata.
func SelectedModelMetadataHash(record any) (string, error) {
	// The qualification probe binds this field to Python's
	// json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True) plus a
	// trailing newline. Encoder's key ordering and two-space indentation match
	// that representation; disabling HTML escaping is required for exact
	// cross-language bytes (the model metadata contains prompt text).
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(record); err != nil {
		return "", err
	}
	return digest(raw.Bytes()), nil
}

func PreparedToolInventoryHash(names []string) (string, error) {
	copyNames := append([]string{}, names...)
	sort.Strings(copyNames)
	for index, name := range copyNames {
		if strings.TrimSpace(name) == "" || index > 0 && name == copyNames[index-1] {
			return "", errors.New("invalid prepared tool inventory")
		}
	}
	return canonical.Hash("harness.codex-prepared-tool-inventory.r17.v1", copyNames)
}

func LaunchControlsHash() string {
	hash, err := canonical.Hash("harness.codex-launch-controls.r17.v1", launchArguments())
	if err != nil {
		panic(err)
	}
	return hash
}

type sliceWriter struct{ bytes *[]byte }

func (w sliceWriter) Write(p []byte) (int, error) {
	*w.bytes = append(*w.bytes, p...)
	return len(p), nil
}

func validateAttestation(a CapabilityConfinementAttestation, launch Launch, now time.Time) (validatedAttestation, error) {
	var result validatedAttestation
	if err := a.Profile.Validate(); err != nil || a.Profile.Runtime != "codex-app-server" {
		return result, errors.New("invalid attested Codex profile")
	}
	if !filepath.IsAbs(a.ManifestPath) || filepath.Clean(a.ManifestPath) != a.ManifestPath || safepath.RequireDigest(a.ExpectedSHA256) != nil {
		return result, errors.New("invalid capability attestation reference")
	}
	rootPath := filepath.Dir(a.ManifestPath)
	if err := safepath.Directory(rootPath); err != nil {
		return result, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return result, err
	}
	defer root.Close()
	var raw []byte
	hash, _, _, exists, err := safepath.CopyRegular(root, filepath.Base(a.ManifestPath), 1<<20, sliceWriter{bytes: &raw})
	if err != nil || !exists || hash != a.ExpectedSHA256 {
		return result, errors.New("capability attestation hash mismatch")
	}
	// Read only the discriminator permissively so the R19 topology can be
	// routed before strict decoding. Both concrete formats are decoded strictly
	// below; unknown or missing fields therefore remain rejected.
	var header struct {
		Kind string `json:"kind"`
	}
	if err := decodeProvider(raw, &header); err != nil {
		return result, err
	}
	if header.Kind == attestationTopologyKind {
		return validateTopologyAttestation(a, launch, now, root, rootPath, raw)
	}
	var manifest attestationManifest
	if err := canonical.Decode(raw, &manifest); err != nil {
		return result, err
	}
	issued, issueErr := time.Parse(time.RFC3339, manifest.IssuedAt)
	expires, expiryErr := time.Parse(time.RFC3339, manifest.ExpiresAt)
	if issueErr != nil || expiryErr != nil || issued.After(now) || !expires.After(now) || !expires.After(issued) || expires.Sub(issued) > maxAttestationLifetime {
		return result, errors.New("capability attestation expired or has invalid lifetime")
	}
	toolsHash, err := DynamicToolsHash(a.DynamicTools)
	if err != nil {
		return result, err
	}
	if manifest.Version != 1 || manifest.Kind != attestationKind || manifest.Verdict != "PASS" || manifest.BinarySHA256 != launch.BinaryHash || manifest.LaunchConfigSHA256 != launch.ConfigHash || manifest.LaunchControlsSHA256 != LaunchControlsHash() || manifest.CLIIdentity == "" || len(manifest.Profiles) == 0 || len(manifest.Profiles) > 8 {
		return result, errors.New("capability attestation identity mismatch")
	}
	profile, fakeProvider, err := selectLeafProfile(manifest, a.Profile, toolsHash)
	if err != nil {
		return result, err
	}
	for _, digest := range []string{manifest.CatalogSHA256, manifest.SelectedModelRecordSHA256} {
		if safepath.RequireDigest(digest) != nil {
			return result, errors.New("invalid capability evidence digest")
		}
	}
	if manifest.AllowedCheck != (attestationCheck{Name: "functions.source_list", Outcome: "success", NoChild: true}) {
		return result, errors.New("allowed registry control missing")
	}
	wantForbidden := []attestationCheck{
		{Name: "collaboration.spawn_agent", Outcome: "unsupported", NoChild: true},
		{Name: "spawn_agent", Outcome: "unsupported", NoChild: true},
		{Name: "tool_search:spawn", Outcome: "no_forbidden_tools", NoChild: true},
	}
	if !slices.Equal(manifest.ForbiddenChecks, wantForbidden) || len(manifest.Evidence) == 0 || len(manifest.Evidence) > 128 || safepath.Relative(manifest.QualificationFile) != nil {
		return result, errors.New("forbidden registry controls missing")
	}
	seen := map[string]bool{}
	catalogFound := false
	for _, evidence := range manifest.Evidence {
		if seen[evidence.Path] || safepath.Relative(evidence.Path) != nil || safepath.RequireDigest(evidence.SHA256) != nil || evidence.Bytes < 0 || evidence.Bytes > 16<<20 {
			return result, errors.New("invalid capability evidence entry")
		}
		seen[evidence.Path] = true
		got, size, _, ok, readErr := safepath.ReadRegular(root, evidence.Path, 16<<20)
		if readErr != nil || !ok || got != evidence.SHA256 || size != evidence.Bytes {
			return result, errors.New("capability evidence changed")
		}
		if evidence.Path == manifest.CatalogFile {
			catalogFound = got == manifest.CatalogSHA256
		}
	}
	if !catalogFound || safepath.Relative(manifest.CatalogFile) != nil {
		return result, errors.New("pinned catalog evidence missing")
	}
	if !seen[manifest.QualificationFile] {
		return result, errors.New("qualification evidence missing")
	}
	metadataHash, err := selectedMetadataHashFromCatalog(root, manifest, profile.Model)
	if err != nil {
		return result, err
	}
	if err := validateQualification(root, manifest, profile, fakeProvider, metadataHash); err != nil {
		return result, err
	}
	launchID, err := launch.BindingID()
	if err != nil {
		return result, err
	}
	id, err := AttestationBindingID(a.ExpectedSHA256, launchID)
	if err != nil {
		return result, err
	}
	return validatedAttestation{id: id, manifestSHA: a.ExpectedSHA256, manifest: manifest, catalogSource: filepath.Join(rootPath, filepath.FromSlash(manifest.CatalogFile)), expires: expires, observed: CapabilityConfinementR17}, nil
}

func selectedMetadataHashFromCatalog(root *os.Root, m attestationManifest, model string) (string, error) {
	catalogBytes, err := proofBytes(root, m, m.CatalogFile)
	if err != nil {
		return "", err
	}
	var catalog struct {
		Models []map[string]any `json:"models"`
	}
	if err := decodeProvider(catalogBytes, &catalog); err != nil {
		return "", err
	}
	var selected map[string]any
	for _, record := range catalog.Models {
		if record["slug"] == model {
			if selected != nil {
				return "", errors.New("selected model duplicated")
			}
			selected = record
		}
	}
	if selected == nil {
		return "", errors.New("selected model missing")
	}
	return SelectedModelMetadataHash(selected)
}

func validateTopologyAttestation(a CapabilityConfinementAttestation, launch Launch, now time.Time, root *os.Root, rootPath string, raw []byte) (validatedAttestation, error) {
	var result validatedAttestation
	var topology attestationTopology
	if err := canonical.Decode(raw, &topology); err != nil {
		return result, err
	}
	issued, issueErr := time.Parse(time.RFC3339, topology.IssuedAt)
	expires, expiryErr := time.Parse(time.RFC3339, topology.ExpiresAt)
	if topology.Version != 1 || topology.Kind != attestationTopologyKind || issueErr != nil || expiryErr != nil || issued.After(now) || !expires.After(now) || !expires.After(issued) || expires.Sub(issued) > maxAttestationLifetime || len(topology.Entries) != 2 {
		return result, errors.New("invalid capability attestation topology")
	}
	wantProfiles := []runtime.Profile{
		{Runtime: "codex-app-server", Provider: "openai", Model: "gpt-5.6-sol", Effort: "medium", Role: "planner"},
		{Runtime: "codex-app-server", Provider: "openai", Model: "gpt-5.6-luna", Effort: "medium", Role: "reviewer"},
	}
	toolsHash, err := DynamicToolsHash(a.DynamicTools)
	if err != nil {
		return result, err
	}
	seen := map[runtime.Profile]bool{}
	var selected *attestationTopologyEntry
	for index := range topology.Entries {
		entry := &topology.Entries[index]
		if !slices.Contains(wantProfiles, entry.Profile) || seen[entry.Profile] || safepath.RequireDigest(entry.DynamicToolsSHA256) != nil || safepath.RequireDigest(entry.LeafSHA256) != nil || safepath.Relative(entry.LeafFile) != nil {
			return result, errors.New("invalid capability attestation topology entry")
		}
		seen[entry.Profile] = true
		if entry.Profile == a.Profile && entry.DynamicToolsSHA256 == toolsHash {
			selected = entry
		}
	}
	for _, profile := range wantProfiles {
		if !seen[profile] {
			return result, errors.New("capability attestation topology profile missing")
		}
	}
	if selected == nil {
		return result, errors.New("profile and dynamic tools are not in capability attestation topology")
	}
	leafHash, _, _, exists, err := safepath.ReadRegular(root, selected.LeafFile, 1<<20)
	if err != nil || !exists || leafHash != selected.LeafSHA256 {
		return result, errors.New("capability attestation leaf changed")
	}
	leafPath := filepath.Join(rootPath, filepath.FromSlash(selected.LeafFile))
	var leafRaw []byte
	leafRoot, err := os.OpenRoot(filepath.Dir(leafPath))
	if err != nil {
		return result, err
	}
	_, _, _, _, err = safepath.CopyRegular(leafRoot, filepath.Base(leafPath), 1<<20, sliceWriter{bytes: &leafRaw})
	closeErr := leafRoot.Close()
	if err != nil || closeErr != nil {
		return result, errors.New("capability attestation leaf unavailable")
	}
	var kind struct {
		Version int    `json:"version"`
		Kind    string `json:"kind"`
	}
	if err := decodeProvider(leafRaw, &kind); err != nil || kind.Version != 1 || kind.Kind != attestationKind {
		return result, errors.New("nested or invalid capability attestation leaf")
	}
	leaf, err := validateAttestation(CapabilityConfinementAttestation{ManifestPath: leafPath, ExpectedSHA256: selected.LeafSHA256, Profile: a.Profile, DynamicTools: a.DynamicTools}, launch, now)
	if err != nil {
		return result, err
	}
	launchID, err := launch.BindingID()
	if err != nil {
		return result, err
	}
	id, err := AttestationBindingID(a.ExpectedSHA256, launchID)
	if err != nil {
		return result, err
	}
	leaf.id = id
	leaf.manifestSHA = a.ExpectedSHA256
	leaf.observed = CapabilityConfinementR19
	if expires.Before(leaf.expires) {
		leaf.expires = expires
	}
	return leaf, nil
}

func StartWithCapabilityConfinement(ctx context.Context, launch Launch, requested string, handler codexrpc.ToolHandler) (*Host, CapabilityConfinementDecision, error) {
	host, err := StartWithToolHandler(ctx, launch, handler)
	if err != nil {
		return nil, CapabilityConfinementDecision{}, err
	}
	decision, decisionErr := DecideCapabilityConfinement(requested)
	if decisionErr != nil {
		var typed *CapabilityConfinementError
		if errors.As(decisionErr, &typed) {
			typed.Launch, typed.Receipt = launch, host.Receipt
		}
		return nil, decision, errors.Join(decisionErr, host.Close())
	}
	return host, decision, nil
}

func StartWithCapabilityConfinementAttested(ctx context.Context, launch Launch, requested string, attestation CapabilityConfinementAttestation, handler codexrpc.ToolHandler) (*Host, CapabilityConfinementDecision, error) {
	if requested != CapabilityConfinementRequired {
		decision, deny := unverified(requested)
		return nil, decision, deny
	}
	validated, err := validateAttestation(attestation, launch, time.Now().UTC())
	if err != nil {
		decision, deny := unverified(requested)
		return nil, decision, errors.Join(deny, err)
	}
	host, err := startWithToolHandlerAndConstraint(ctx, launch, handler, validated.catalogSource, validated.manifest.CatalogSHA256, validated.manifest.CLIIdentity, attestation.Profile, attestation.DynamicTools, validated.expires)
	if err != nil {
		return nil, CapabilityConfinementDecision{}, err
	}
	if _, err := validateAttestation(attestation, launch, time.Now().UTC()); err != nil {
		decision, deny := unverified(requested)
		return nil, decision, errors.Join(deny, err, host.Close())
	}
	decision := CapabilityConfinementDecision{Version: 1, Requested: requested, Observed: validated.observed, Admitted: true, AttestationID: validated.id}
	host.Receipt.CapabilityAttestationID = validated.id
	host.Receipt.CapabilityAttestationSHA256 = validated.manifestSHA
	return host, decision, nil
}
