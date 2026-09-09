package gitlocal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// Validate proves that the supplied object bytes reconstruct this exact plan.
// It does not attest current workspace state, storage, authorization or readiness.
func (o CandidateObjects) Validate(plan CommitPlan) error {
	if _, err := plan.ID(); err != nil {
		return err
	}
	if len(o.Blobs) != len(plan.Files) {
		return errors.New("commit blob count mismatch")
	}
	format := plan.Workspace.Request.Source.ObjectFormat
	entries := make([]BlobEntry, 0, len(o.Blobs))
	total := 0
	for i, blob := range o.Blobs {
		file := plan.Files[i]
		if len(blob.Data) > (64<<20)-total {
			return errors.New("commit content bound exceeded")
		}
		total += len(blob.Data)
		digest := sha256.Sum256(blob.Data)
		if blob.Path != file.Path || hex.EncodeToString(digest[:]) != file.Hash {
			return errors.New("commit blob differs from manifest")
		}
		id, err := ObjectID(format, "blob", blob.Data)
		if err != nil {
			return err
		}
		if id != blob.ObjectID {
			return errors.New("commit blob ID mismatch")
		}
		entries = append(entries, BlobEntry{Path: file.Path, ObjectID: id, Executable: file.Executable})
	}
	trees, err := BuildTrees(format, entries)
	if err != nil {
		return err
	}
	if len(trees) != len(o.Trees) {
		return errors.New("commit tree count mismatch")
	}
	for i, tree := range trees {
		if tree.Path != o.Trees[i].Path || tree.ObjectID != o.Trees[i].ObjectID || !bytes.Equal(tree.Data, o.Trees[i].Data) {
			return errors.New("commit tree mismatch")
		}
	}
	data, id, err := CommitObject(plan, trees[len(trees)-1].ObjectID)
	if err != nil {
		return err
	}
	if id != o.CommitID || !bytes.Equal(data, o.CommitData) {
		return errors.New("commit body or identity mismatch")
	}
	return nil
}
