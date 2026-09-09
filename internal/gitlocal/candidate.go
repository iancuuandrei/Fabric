package gitlocal

import (
	"bytes"
	"context"
	"errors"
	"os"

	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/worktree"
)

// EncodedBlob retains exact candidate bytes and their native Git identity.
type EncodedBlob struct {
	Path     string
	ObjectID string
	Data     []byte
}

// CandidateObjects is an in-memory, verified object set, not a storage receipt.
type CandidateObjects struct {
	Blobs      []EncodedBlob
	Trees      []EncodedTree
	CommitID   string
	CommitData []byte
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	remaining int
}

// Bytes returns retained bytes without copying.
func (b *boundedBuffer) Bytes() []byte { return b.buffer.Bytes() }

// String returns retained output as text without interpretation.
func (b *boundedBuffer) String() string { return b.buffer.String() }

// Write rejects a write that would exceed the remaining output budget.
func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.remaining {
		return 0, errors.New("candidate object byte bound exceeded")
	}
	n, err := b.buffer.Write(p)
	b.remaining -= n
	return n, err
}

// ReadCandidateObjects requires a caller-held workspace lease. It checks the
// whole candidate before and after reading and verifies every file's hash/mode.
// Total retained blob content is bounded to 64 MiB. No Git objects are written.
func ReadCandidateObjects(ctx context.Context, plan CommitPlan) (result CandidateObjects, err error) {
	if _, err := plan.ID(); err != nil {
		return CandidateObjects{}, err
	}
	before, err := worktree.Fingerprint(ctx, plan.Workspace)
	if err != nil {
		return CandidateObjects{}, err
	}
	if before != plan.Candidate {
		return CandidateObjects{}, errors.New("candidate changed before object construction")
	}
	root, err := os.OpenRoot(plan.Workspace.Request.Path)
	if err != nil {
		return CandidateObjects{}, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
		if err != nil {
			result = CandidateObjects{}
		}
	}()
	remaining := 64 << 20
	entries := make([]BlobEntry, 0, len(plan.Files))
	result.Blobs = make([]EncodedBlob, 0, len(plan.Files))
	format := plan.Workspace.Request.Source.ObjectFormat
	for _, file := range plan.Files {
		if err := ctx.Err(); err != nil {
			return CandidateObjects{}, err
		}
		buffer := &boundedBuffer{remaining: remaining}
		digest, _, executable, exists, err := safepath.CopyRegular(root, file.Path, 64<<20, buffer)
		if err != nil {
			return CandidateObjects{}, err
		}
		if !exists || digest != file.Hash || executable != file.Executable {
			return CandidateObjects{}, errors.New("candidate blob hash or mode mismatch")
		}
		remaining = buffer.remaining
		data := buffer.Bytes()
		id, err := ObjectID(format, "blob", data)
		if err != nil {
			return CandidateObjects{}, err
		}
		result.Blobs = append(result.Blobs, EncodedBlob{Path: file.Path, ObjectID: id, Data: data})
		entries = append(entries, BlobEntry{Path: file.Path, ObjectID: id, Executable: executable})
	}
	result.Trees, err = BuildTrees(format, entries)
	if err != nil {
		return CandidateObjects{}, err
	}
	result.CommitData, result.CommitID, err = CommitObject(plan, result.Trees[len(result.Trees)-1].ObjectID)
	if err != nil {
		return CandidateObjects{}, err
	}
	after, err := worktree.Fingerprint(ctx, plan.Workspace)
	if err != nil {
		return CandidateObjects{}, err
	}
	if after != plan.Candidate {
		return CandidateObjects{}, errors.New("candidate changed during object construction")
	}
	return result, nil
}
