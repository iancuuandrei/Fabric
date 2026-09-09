package hostenvironment

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// HostKind identifies the outer host selected by the available observations.
type HostKind string

const (
	// HostNative is the standalone host used when there is no Codex host hint.
	HostNative HostKind = "native"
	// HostCodex identifies a passive hint that the process is hosted by Codex.
	HostCodex HostKind = "codex"
)

// HostEvidenceLevel describes how Host was selected. A hint is informational
// and carries no authority over sandbox or effect policy.
type HostEvidenceLevel string

const (
	// EvidenceDefault means the native host was selected without a host hint.
	EvidenceDefault HostEvidenceLevel = "DEFAULT"
	// EvidenceHint means passive signals suggest a host without proving it.
	EvidenceHint HostEvidenceLevel = "HINT"
)

// SandboxStatus distinguishes absent proof from evidence supplied by a trusted
// host-specific verifier.
type SandboxStatus string

const (
	// SandboxNotVerified means no trusted verifier established a boundary.
	SandboxNotVerified SandboxStatus = "NOT_VERIFIED"
	// SandboxVerified means a trusted verifier established inherited enforcement.
	SandboxVerified SandboxStatus = "VERIFIED"
)

// SandboxMode is a verified description of the inherited outer boundary.
type SandboxMode string

const (
	// SandboxReadOnly permits observation under the verified inherited boundary.
	SandboxReadOnly SandboxMode = "read-only"
	// SandboxWorkspaceWrite permits bounded workspace writes.
	SandboxWorkspaceWrite SandboxMode = "workspace-write"
	// SandboxRestricted records a verified boundary with host-specific limits.
	SandboxRestricted SandboxMode = "restricted"
)

// VerifiedSandbox is returned only by an independently trusted host-specific
// verifier. Environment variables and process names must not be copied into it.
type VerifiedSandbox struct {
	Mode      SandboxMode `json:"mode"`
	Source    string      `json:"source"`
	Inherited bool        `json:"inherited"`
}

// BoundaryVerifier verifies an already-enforced inherited process boundary.
// Implementations must use evidence independent of process-controlled hints.
// The harness does not install, recreate, weaken, or elevate that boundary.
type BoundaryVerifier interface {
	VerifyInherited(context.Context) (VerifiedSandbox, error)
}

// Input makes passive signals and trusted verification independently
// injectable. An omitted Verifier deliberately produces NOT_VERIFIED.
type Input struct {
	Environ     []string
	GOOS        string
	ProcessName string
	Verifier    BoundaryVerifier
}

// HostEvidence records bounded passive signals without their values.
type HostEvidence struct {
	Level   HostEvidenceLevel `json:"level"`
	Signals []string          `json:"signals"`
}

// SandboxObservation records whether an inherited boundary was actually
// verified. Mode and Source are omitted when verification is unavailable.
type SandboxObservation struct {
	Status    SandboxStatus `json:"status"`
	Mode      SandboxMode   `json:"mode,omitempty"`
	Source    string        `json:"source,omitempty"`
	Inherited bool          `json:"inherited"`
}

// Observation is an informational snapshot of the outer host boundary.
type Observation struct {
	Version        int                `json:"version"`
	Host           HostKind           `json:"host"`
	HostEvidence   HostEvidence       `json:"host_evidence"`
	Platform       string             `json:"platform"`
	Sandbox        SandboxObservation `json:"sandbox"`
	verifiedHere   bool
	verifiedMode   SandboxMode
	verifiedSource string
}

var codexEnvironmentHints = map[string]struct{}{
	"CODEX_APP_TOOLS_PIPE_PATH": {},
	"CODEX_CI":                  {},
	"CODEX_HOME":                {},
	"CODEX_SESSION_ID":          {},
	"CODEX_THREAD_ID":           {},
}

// ObserveDefault observes the current process without a sandbox verifier.
// Consequently it cannot report VERIFIED sandbox evidence.
func ObserveDefault(ctx context.Context) (Observation, error) {
	return Observe(ctx, Input{Environ: environ(), GOOS: runtime.GOOS})
}

var environ = func() []string { return runtimeEnviron() }

// runtimeEnviron is split out for a small test seam without making environment
// access part of the security evidence interface.
func runtimeEnviron() []string {
	// os.Environ lives behind this helper so Observe itself remains deterministic.
	return currentEnvironment()
}

// Observe returns a bounded host observation. Passive Codex markers affect only
// HostEvidence; SandboxVerified requires a successful trusted Verifier.
func Observe(ctx context.Context, in Input) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	platform := strings.ToLower(strings.TrimSpace(in.GOOS))
	if !identifier(platform, 32) {
		return Observation{}, errors.New("host environment platform must be a lowercase identifier")
	}
	if len(in.Environ) > 4096 {
		return Observation{}, errors.New("host environment exceeds bounded entry count")
	}
	signals := codexHintSignals(in.Environ, in.ProcessName)
	host, level := HostNative, EvidenceDefault
	if len(signals) != 0 {
		host, level = HostCodex, EvidenceHint
	}
	result := Observation{
		Version:      1,
		Host:         host,
		HostEvidence: HostEvidence{Level: level, Signals: signals},
		Platform:     platform,
		Sandbox:      SandboxObservation{Status: SandboxNotVerified},
	}
	if in.Verifier == nil {
		return result, nil
	}
	evidence, err := in.Verifier.VerifyInherited(ctx)
	if err != nil {
		return Observation{}, fmt.Errorf("verify inherited sandbox: %w", err)
	}
	if err := validateVerifiedSandbox(evidence); err != nil {
		return Observation{}, err
	}
	result.Sandbox = SandboxObservation{
		Status: SandboxVerified, Mode: evidence.Mode,
		Source: evidence.Source, Inherited: true,
	}
	result.verifiedHere, result.verifiedMode, result.verifiedSource = true, evidence.Mode, evidence.Source
	return result, nil
}

func codexHintSignals(environment []string, processName string) []string {
	set := make(map[string]struct{})
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		name = strings.ToUpper(strings.TrimSpace(name))
		if !found {
			continue
		}
		if _, ok := codexEnvironmentHints[name]; ok {
			set["env:"+name] = struct{}{}
		}
	}
	base := strings.ToLower(filepath.Base(strings.TrimSpace(processName)))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if base == "codex" || base == "chatgpt" {
		set["process-name"] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for signal := range set {
		result = append(result, signal)
	}
	sort.Strings(result)
	return result
}

func validateVerifiedSandbox(evidence VerifiedSandbox) error {
	if !evidence.Inherited {
		return errors.New("sandbox evidence does not verify an inherited boundary")
	}
	switch evidence.Mode {
	case SandboxReadOnly, SandboxWorkspaceWrite, SandboxRestricted:
	default:
		return errors.New("sandbox evidence has an unsupported mode")
	}
	if !identifier(evidence.Source, 64) {
		return errors.New("sandbox evidence source must be a lowercase identifier of 1 to 64 bytes")
	}
	return nil
}

func identifier(value string, limit int) bool {
	if len(value) == 0 || len(value) > limit {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' && r != '.' {
			return false
		}
	}
	return true
}
