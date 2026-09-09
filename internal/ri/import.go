package ri

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/safepath"
)

// Input binds an exact producer input to its raw SHA-256.
type Input struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// Producer declares the implementation and inputs behind observations.
type Producer struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Version        string  `json:"version"`
	ArtifactSHA256 string  `json:"artifact_sha256"`
	Inputs         []Input `json:"inputs"`
}

// Manifest carries the Rust snapshot provenance contract.
type Manifest struct {
	Format    int        `json:"format"`
	Source    Source     `json:"source"`
	Producers []Producer `json:"producers"`
}

// ImportRequest selects explicit disk inputs; source paths never derive from a URI.
type ImportRequest struct {
	IndexPath   string            `json:"index_path"`
	Manifest    Manifest          `json:"manifest"`
	Source      Source            `json:"source"`
	Producer    string            `json:"producer"`
	ProjectRoot string            `json:"project_root"`
	Sources     map[string]string `json:"sources"`
	Policy      string            `json:"policy"`
}

// ImportReceipt describes a verified staging artifact, not a published snapshot.
type ImportReceipt struct {
	SnapshotID string `json:"snapshot_id"`
	Bytes      int    `json:"bytes"`
	Source     Source `json:"source"`
	OutputPath string `json:"output_path"`
}

// Import stages one snapshot and independently checks its bytes and reader status.
// Caller must journal intent before invocation and own the staging directory.
// Errors can leave staging bytes; retries and cleanup are never automatic.
func (c Client) Import(ctx context.Context, request ImportRequest, identity repository.Identity, output string) (ImportReceipt, error) {
	source, err := FromRepository(identity)
	if err != nil {
		return ImportReceipt{}, err
	}
	if request.Source != source || request.Manifest.Source != source || !filepath.IsAbs(request.IndexPath) || !filepath.IsAbs(output) || filepath.Clean(output) != output || (request.Policy != "strict" && request.Policy != "scip_go027") {
		return ImportReceipt{}, errors.New("invalid import authority, paths or policy")
	}
	for _, path := range request.Sources {
		if !filepath.IsAbs(path) {
			return ImportReceipt{}, errors.New("absolute import source path required")
		}
	}
	if err := safepath.Directory(filepath.Dir(output)); err != nil {
		return ImportReceipt{}, err
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		return ImportReceipt{}, errors.New("import staging path must be new")
	}
	result, err := c.Call(ctx, map[string]any{"operation": "scip_import", "request": request, "output_path": output})
	if err != nil {
		return ImportReceipt{}, err
	}
	var receipt ImportReceipt
	if err := canonical.Decode(result, &receipt); err != nil {
		return ImportReceipt{}, err
	}
	if receipt.Source != source || receipt.OutputPath != output || receipt.Bytes < 1 || receipt.Bytes > 64<<20 {
		return ImportReceipt{}, errors.New("import receipt binding mismatch")
	}
	if err := safepath.RequireDigest(receipt.SnapshotID); err != nil {
		return ImportReceipt{}, err
	}
	root, err := os.OpenRoot(filepath.Dir(output))
	if err != nil {
		return ImportReceipt{}, err
	}
	defer root.Close()
	data, err := stored(root, filepath.Base(output), receipt.SnapshotID)
	if err != nil {
		return ImportReceipt{}, err
	}
	if len(data) != receipt.Bytes {
		return ImportReceipt{}, errors.New("import staging size mismatch")
	}
	if _, err := c.Inspect(ctx, SnapshotRef{output, receipt.SnapshotID, source}); err != nil {
		return ImportReceipt{}, err
	}
	return receipt, nil
}
