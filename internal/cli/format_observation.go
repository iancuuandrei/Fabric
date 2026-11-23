package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/verification"
)

const goFormatManifestMaxBytes = 16 << 10

type goFormatObservationResult struct {
	RunID                string                           `json:"run_id"`
	CacheState           verification.GoFormatCacheState  `json:"cache_state"`
	Observation          verification.GoFormatObservation `json:"observation"`
	ArtifactMetadataPath string                           `json:"artifact_metadata_path"`
	ArtifactPayloadPath  string                           `json:"artifact_payload_path"`
}

// formatObservationCommand records a local, non-authoritative artifact for a
// fixed in-process Go formatter. It never appends a controller event or runs a
// configured verification check.
func formatObservationCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	if len(args) != 3 {
		return errors.New("observe-format requires RUN MANIFEST_JSON CACHE_DIR")
	}
	manifest, err := readGoFormatManifest(args[1])
	if err != nil {
		return err
	}
	if !filepath.IsAbs(args[2]) || filepath.Clean(args[2]) != args[2] {
		return errors.New("Go format cache directory must be an absolute clean path")
	}
	path, err := runPath(root, args[0])
	if err != nil {
		return err
	}
	snapshot, err := control.Inspect(path)
	if err != nil {
		return err
	}
	if err := requireRunBinding(snapshot, root, args[0]); err != nil {
		return err
	}
	if snapshot.Workspace == nil || snapshot.Candidate == nil || snapshot.WorkspaceOutcome != "CONFIRMED" {
		return errors.New("observe-format requires a confirmed workspace candidate")
	}
	observation, cacheState, err := verification.ObserveGoFormat(ctx, *snapshot.Workspace, *snapshot.Candidate, manifest, args[2])
	if err != nil {
		return err
	}
	result := goFormatObservationResult{RunID: args[0], CacheState: cacheState, Observation: observation}
	if cacheState != verification.GoFormatCacheContended {
		result.ArtifactMetadataPath = filepath.Join(args[2], observation.ActionID+".json")
		result.ArtifactPayloadPath = filepath.Join(args[2], observation.ActionID+".out")
	}
	return output(out, result)
}

func readGoFormatManifest(path string) (verification.GoFormatManifest, error) {
	var manifest verification.GoFormatManifest
	file, err := os.Open(path)
	if err != nil {
		return manifest, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > goFormatManifestMaxBytes {
		return manifest, errors.New("Go format manifest must be a regular JSON file no larger than 16 KiB")
	}
	raw, err := io.ReadAll(io.LimitReader(file, goFormatManifestMaxBytes+1))
	if err != nil || len(raw) > goFormatManifestMaxBytes {
		return manifest, errors.New("Go format manifest exceeds 16 KiB or could not be read")
	}
	if err := canonical.Decode(raw, &manifest); err != nil {
		return manifest, err
	}
	return manifest, manifest.Validate()
}
