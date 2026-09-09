package codexruntime

import (
	"errors"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/ri"
	"harness.local/engorch/internal/safepath"
	"path/filepath"
)

// RIBinding fixes one immutable snapshot and reader before provider thread creation.
// The caller must select a published artifact; this contract validates identity,
// not store publication history or operating-system confinement.
type RIBinding struct {
	Snapshot         ri.SnapshotRef `json:"snapshot"`
	Executable       string         `json:"executable"`
	ExecutableSHA256 string         `json:"executable_sha256"`
}

// Validate binds RI access to the same exact source used by source tools.
func (b RIBinding) Validate(source repository.Identity) error {
	expected, err := ri.FromRepository(source)
	if err != nil {
		return err
	}
	if b.Snapshot.Source != expected || !filepath.IsAbs(b.Snapshot.Path) || !filepath.IsAbs(b.Executable) {
		return errors.New("runtime RI source or path mismatch")
	}
	if err := safepath.RequireDigest(b.Snapshot.ID); err != nil {
		return err
	}
	return safepath.RequireDigest(b.ExecutableSHA256)
}
