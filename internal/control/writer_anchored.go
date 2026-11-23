package control

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/anchoredit"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/worktree"
	"github.com/iancuuandrei/fabric/internal/writercontract"
)

const anchoredEditPreimagesMaxBytes = 256 << 10

// WriterEditPreimage is exact source bytes read from the bound candidate. The
// manifest hash is checked during replay before edits are recomposed.
type WriterEditPreimage struct {
	Path        string `json:"path"`
	ContentUTF8 string `json:"content_utf8"`
}

func decodeAnchoredProposal(raw string) (writercontract.AnchoredProposal, error) {
	var proposal writercontract.AnchoredProposal
	if err := canonical.Decode([]byte(raw), &proposal); err != nil {
		return proposal, err
	}
	var envelope map[string]json.RawMessage
	if err := canonical.Decode([]byte(raw), &envelope); err != nil {
		return proposal, err
	}
	if err := requireAnchoredFields(envelope, "candidate_id", "changes"); err != nil {
		return proposal, err
	}
	if err := safepath.RequireDigest(proposal.CandidateID); err != nil {
		return proposal, errors.New("anchored proposal candidate ID invalid")
	}
	if err := writercontract.ValidateCount(len(proposal.Changes)); err != nil {
		return proposal, err
	}
	seen := map[string]bool{}
	editBytes, newFileBytes := 0, 0
	var wireChanges []map[string]json.RawMessage
	if err := canonical.Decode(envelope["changes"], &wireChanges); err != nil {
		return proposal, err
	}
	for i, change := range proposal.Changes {
		if err := requireAnchoredFields(wireChanges[i], "path", "before_hash", "edits", "new_content_utf8", "executable"); err != nil {
			return proposal, err
		}
		var edits []map[string]json.RawMessage
		if err := canonical.Decode(wireChanges[i]["edits"], &edits); err != nil {
			return proposal, err
		}
		for _, edit := range edits {
			if err := requireAnchoredFields(edit, "before", "after"); err != nil {
				return proposal, err
			}
		}
		if err := safepath.Writable(change.Path); err != nil {
			return proposal, err
		}
		if seen[change.Path] {
			return proposal, errors.New("anchored change paths must be unique")
		}
		seen[change.Path] = true
		if len(change.Edits) > anchoredit.MaxEdits {
			return proposal, errors.New("anchored edit count outside bounds")
		}
		for _, edit := range change.Edits {
			if len(edit.Before) == 0 || len(edit.Before) > writercontract.AnchoredEditAnchorMax || len(edit.After) > writercontract.AnchoredEditAnchorMax || !utf8.ValidString(edit.Before) || !utf8.ValidString(edit.After) {
				return proposal, errors.New("anchored edit text outside UTF-8 bounds")
			}
			editBytes += len(edit.Before) + len(edit.After)
		}
		if editBytes > writercontract.AnchoredEditsMaxBytes {
			return proposal, errors.New("anchored edit payload exceeds bound")
		}
		if change.BeforeHash == nil {
			if len(change.Edits) != 0 || change.NewContentUTF8 == nil {
				return proposal, errors.New("new files require an empty edits array and explicit non-null content")
			}
			if err := anchoredit.ValidateNewFile(*change.NewContentUTF8); err != nil {
				return proposal, err
			}
			newFileBytes += len(*change.NewContentUTF8)
		} else {
			if err := safepath.RequireDigest(*change.BeforeHash); err != nil {
				return proposal, errors.New("existing file before_hash invalid")
			}
			if len(change.Edits) == 0 || change.NewContentUTF8 != nil {
				return proposal, errors.New("existing files require edits and null new_content_utf8; deletion is unsupported")
			}
		}
	}
	if newFileBytes > 256<<10 {
		return proposal, errors.New("new file contents exceed proposal bound")
	}
	return proposal, nil
}

func requireAnchoredFields(fields map[string]json.RawMessage, required ...string) error {
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return errors.New("anchored writer required field missing: " + key)
		}
	}
	return nil
}

func composeAnchoredProposal(proposal writercontract.AnchoredProposal, manifest []worktree.FileState, preimages []WriterEditPreimage) ([]fileeffects.Change, error) {
	if err := safepath.RequireDigest(proposal.CandidateID); err != nil {
		return nil, errors.New("anchored proposal candidate ID invalid")
	}
	if err := writercontract.ValidateCount(len(proposal.Changes)); err != nil {
		return nil, err
	}
	files := make(map[string]worktree.FileState, len(manifest))
	for _, file := range manifest {
		files[file.Path] = file
	}
	before := make(map[string]string, len(preimages))
	preimageTotal := 0
	for _, item := range preimages {
		if err := safepath.Writable(item.Path); err != nil {
			return nil, err
		}
		if _, exists := before[item.Path]; exists || len(item.ContentUTF8) > anchoredit.MaxSourceBytes || !utf8.ValidString(item.ContentUTF8) {
			return nil, errors.New("anchored edit preimage duplicate or outside bounds")
		}
		before[item.Path] = item.ContentUTF8
		preimageTotal += len(item.ContentUTF8)
		if preimageTotal > anchoredEditPreimagesMaxBytes {
			return nil, errors.New("anchored edit preimages exceed aggregate bound")
		}
	}
	ordered := append([]writercontract.AnchoredChange(nil), proposal.Changes...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	changes := make([]fileeffects.Change, 0, len(ordered))
	used := make(map[string]bool, len(ordered))
	var contentFailure error
	for i, change := range ordered {
		if i > 0 && ordered[i-1].Path == change.Path {
			return nil, errors.New("anchored change paths must be unique")
		}
		state, exists := files[change.Path]
		var content []byte
		if change.BeforeHash == nil {
			if exists || len(change.Edits) != 0 || change.NewContentUTF8 == nil {
				return nil, errors.New("new file must be absent and have explicit content with no edits")
			}
			if err := anchoredit.ValidateNewFile(*change.NewContentUTF8); err != nil {
				return nil, err
			}
			content = []byte(*change.NewContentUTF8)
		} else {
			if !exists || state.Hash != *change.BeforeHash || len(change.Edits) == 0 || change.NewContentUTF8 != nil {
				return nil, errors.New("existing file candidate hash or anchored-edit shape mismatch")
			}
			original, ok := before[change.Path]
			if !ok {
				return nil, errors.New("anchored edit preimage missing")
			}
			hash := sha256.Sum256([]byte(original))
			if hex.EncodeToString(hash[:]) != state.Hash {
				return nil, errors.New("anchored edit preimage does not match before manifest")
			}
			var err error
			content, err = anchoredit.Apply([]byte(original), toAnchorEdits(change.Edits))
			if err != nil {
				if contentFailure == nil {
					contentFailure = fmt.Errorf("anchored edit %q: %w", change.Path, err)
				}
			}
			used[change.Path] = true
		}
		encoded := base64.StdEncoding.EncodeToString(content)
		var beforeHash *string
		if change.BeforeHash != nil {
			value := *change.BeforeHash
			beforeHash = &value
		}
		changes = append(changes, fileeffects.Change{Path: change.Path, BeforeHash: beforeHash, ContentBase64: &encoded, Executable: change.Executable})
	}
	if len(used) != len(before) {
		return nil, errors.New("anchored edit preimages include unused paths")
	}
	if contentFailure != nil {
		return nil, rejectedSemanticOutput(contentFailure)
	}
	return changes, nil
}

func toAnchorEdits(edits []writercontract.AnchoredEdit) []anchoredit.Edit {
	out := make([]anchoredit.Edit, len(edits))
	for i, edit := range edits {
		out[i] = anchoredit.Edit{Before: edit.Before, After: edit.After}
	}
	return out
}

func readAnchoredPreimages(ctx context.Context, path string, s Snapshot, proposal writercontract.AnchoredProposal, taskIDs ...string) (manifest []worktree.FileState, preimages []WriterEditPreimage, err error) {
	if len(taskIDs) > 1 {
		return nil, nil, ErrAutonomousUnsafe
	}
	taskID := ""
	if len(taskIDs) == 1 {
		taskID = taskIDs[0]
	}
	if s.Workspace == nil || s.Candidate == nil {
		return nil, nil, errors.New("anchored edits require a confirmed workspace candidate")
	}
	lease, err := worktree.AcquireRead(s.Workspace.Request)
	if err != nil {
		return nil, nil, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	latest, err := Inspect(path)
	if err != nil {
		return nil, nil, err
	}
	latest, err = anchoredCorrectionSnapshot(latest, taskID)
	if err != nil {
		return nil, nil, err
	}
	if latest.Workspace == nil || latest.Candidate == nil || *latest.Workspace != *s.Workspace || *latest.Candidate != *s.Candidate {
		return nil, nil, errors.New("anchored edit candidate changed before source read")
	}
	observed, manifest, err := worktree.Capture(ctx, *latest.Workspace)
	if err != nil {
		return nil, nil, err
	}
	if observed != *latest.Candidate {
		return nil, nil, errors.New("anchored edit candidate differs from workspace")
	}
	byPath := make(map[string]worktree.FileState, len(manifest))
	for _, file := range manifest {
		byPath[file.Path] = file
	}
	candidateID, err := latest.Candidate.ID()
	if err != nil {
		return nil, nil, err
	}
	preimageTotal := 0
	for _, change := range proposal.Changes {
		state, exists := byPath[change.Path]
		if change.BeforeHash == nil {
			if exists {
				return nil, nil, errors.New("new anchored file already exists in candidate")
			}
			continue
		}
		if !exists || state.Hash != *change.BeforeHash {
			return nil, nil, errors.New("anchored before_hash differs from candidate manifest")
		}
		var content []byte
		var offset int64
		var expectedSize int64 = -1
		for {
			chunk, readErr := worktree.ReadSource(ctx, *latest.Workspace, *latest.Candidate, change.Path, offset, 32<<10)
			if readErr != nil {
				return nil, nil, readErr
			}
			if chunk.CandidateID != candidateID || chunk.Path != change.Path || chunk.SHA256 != state.Hash || expectedSize >= 0 && chunk.Size != expectedSize || chunk.Offset != offset {
				return nil, nil, errors.New("anchored source read binding mismatch")
			}
			expectedSize = chunk.Size
			if expectedSize > anchoredit.MaxSourceBytes {
				return nil, nil, errors.New("anchored source file exceeds 256 KiB bound")
			}
			part, decodeErr := base64.StdEncoding.DecodeString(chunk.ContentBase64)
			if decodeErr != nil {
				return nil, nil, errors.New("anchored source read returned invalid base64")
			}
			if chunk.ContentUTF8 != nil && string(part) != *chunk.ContentUTF8 {
				return nil, nil, errors.New("anchored source UTF-8 and byte views disagree")
			}
			content = append(content, part...)
			if len(content) > anchoredit.MaxSourceBytes {
				return nil, nil, errors.New("anchored source file exceeds 256 KiB bound")
			}
			if chunk.NextOffset == nil {
				break
			}
			if *chunk.NextOffset <= offset {
				return nil, nil, errors.New("anchored source read did not advance")
			}
			offset = *chunk.NextOffset
		}
		if int64(len(content)) != expectedSize || !utf8.Valid(content) {
			return nil, nil, errors.New("anchored source read is incomplete or invalid UTF-8")
		}
		preimageTotal += len(content)
		if preimageTotal > anchoredEditPreimagesMaxBytes {
			return nil, nil, errors.New("anchored source preimages exceed 256 KiB aggregate bound")
		}
		preimages = append(preimages, WriterEditPreimage{Path: change.Path, ContentUTF8: string(content)})
	}
	final, err := worktree.Fingerprint(ctx, *latest.Workspace)
	if err != nil {
		return nil, nil, err
	}
	current, err := Inspect(path)
	if err != nil {
		return nil, nil, err
	}
	current, err = anchoredCorrectionSnapshot(current, taskID)
	if err != nil {
		return nil, nil, err
	}
	if final != *latest.Candidate || current.Candidate == nil || *current.Candidate != *latest.Candidate {
		return nil, nil, errors.New("anchored source read candidate drift")
	}
	sort.Slice(preimages, func(i, j int) bool { return preimages[i].Path < preimages[j].Path })
	return manifest, preimages, nil
}
