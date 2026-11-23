package cli

import (
	"fmt"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/evidencevalue"
)

const evidenceAutoPolicyMaxBytes = 64 << 10

// readEvidenceAutoPolicy reads a strict versioned finite EVC template. The
// template binding must stay empty; the controller fills the live run, source,
// candidate and journal head at decision time. Estimates stay unvalidated.
func readEvidenceAutoPolicy(path string) (control.EvidenceAutoPolicy, error) {
	var policy control.EvidenceAutoPolicy
	raw, err := readRegularPolicyJSON(path, evidenceAutoPolicyMaxBytes, "evidence policy", "64 KiB")
	if err != nil {
		return policy, err
	}
	if err := canonical.Decode(raw, &policy); err != nil {
		return policy, fmt.Errorf("invalid evidence policy: %w", err)
	}
	if err := control.ValidateEvidenceAutoPolicyTemplate(policy); err != nil {
		return policy, err
	}
	// Enforce the canonical size bound on the normalized form as well.
	normal, err := canonical.Bytes(policy)
	if err != nil || len(normal) > evidencevalue.MaxBytes {
		return policy, fmt.Errorf("evidence policy exceeds 64 KiB canonical bound")
	}
	return policy, nil
}
