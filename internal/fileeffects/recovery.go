package fileeffects

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/worktree"
)

// Recovery binds fresh partial-state evidence to one original approved proposal.
// Temporary contains only exact original temporary paths and observed base64 bytes.
// It is a new approval target; it never authorizes an automatic retry.
type Recovery struct {
	Version            int                  `json:"version"`
	Nonce              string               `json:"nonce"`
	OriginalProposalID string               `json:"original_proposal_id"`
	Observed           worktree.Candidate   `json:"observed"`
	Files              []worktree.FileState `json:"files"`
	Temporary          map[string]string    `json:"temporary"`
}

func temporaryName(id string, index int) string { return fmt.Sprintf(".harness-tmp-%s-%d", id, index) }

// Remaining validates that every changed target is exactly before or after,
// every unrelated file is unchanged, and temporary bytes are approved prefixes.
// It returns only unfinished change indices; unexpected state is an error.
func (r Recovery) Remaining(p Proposal) (map[int]bool, error) {
	id, err := p.ID()
	if err != nil {
		return nil, err
	}
	if r.Version != 1 || strings.TrimSpace(r.Nonce) == "" || len(r.Nonce) > 128 || r.OriginalProposalID != id || r.Temporary == nil {
		return nil, errors.New("invalid recovery binding")
	}
	if _, err = r.Observed.ID(); err != nil {
		return nil, err
	}
	hash, err := worktree.FilesID(r.Files)
	if err != nil {
		return nil, err
	}
	if hash != r.Observed.FilesHash || len(r.Files) != r.Observed.FileCount || r.Observed.WorktreeID != p.Before.WorktreeID || r.Observed.Head != p.Before.Head || r.Observed.IndexHash != p.Before.IndexHash {
		return nil, errors.New("recovery candidate binding mismatch")
	}
	actual := map[string]worktree.FileState{}
	expected := map[string]worktree.FileState{}
	for _, f := range r.Files {
		actual[f.Path] = f
	}
	for _, f := range p.BeforeFiles {
		expected[f.Path] = f
	}
	remaining := map[int]bool{}
	knownTemps := map[string]bool{}
	for i, c := range p.Changes {
		old, oldExists := expected[c.Path]
		current, currentExists := actual[c.Path]
		newState := worktree.FileState{}
		newExists := c.ContentBase64 != nil
		data, err := content(c)
		if err != nil {
			return nil, err
		}
		if newExists {
			h := sha256.Sum256(data)
			newState = worktree.FileState{Path: c.Path, Hash: hex.EncodeToString(h[:]), Executable: c.Executable}
		}
		if currentExists == newExists && (!newExists || current == newState) {
		} else if currentExists == oldExists && (!oldExists || current == old) {
			remaining[i] = true
		} else {
			return nil, errors.New("recovery target is neither approved before nor after state")
		}
		delete(actual, c.Path)
		delete(expected, c.Path)
		temp := temporaryName(id, i)
		encoded, hasTemp := r.Temporary[temp]
		if hasTemp {
			if !newExists {
				return nil, errors.New("temporary file for deletion")
			}
			b, err := base64.StdEncoding.Strict().DecodeString(encoded)
			if err != nil || base64.StdEncoding.EncodeToString(b) != encoded || !bytes.HasPrefix(data, b) {
				return nil, errors.New("temporary content is not an approved prefix")
			}
			f, exists := actual[temp]
			h := sha256.Sum256(b)
			if !exists || f.Hash != hex.EncodeToString(h[:]) || f.Executable != c.Executable {
				return nil, errors.New("temporary observation mismatch")
			}
			if _, existed := expected[temp]; existed {
				return nil, errors.New("temporary collides with original source")
			}
			delete(actual, temp)
			knownTemps[temp] = true
		}
	}
	if len(knownTemps) != len(r.Temporary) {
		return nil, errors.New("unrelated temporary cleanup requested")
	}
	if len(actual) != len(expected) {
		return nil, errors.New("unrelated recovery file changes")
	}
	for path, want := range expected {
		if got, ok := actual[path]; !ok || got != want {
			return nil, errors.New("unrelated recovery file changed")
		}
	}
	return remaining, nil
}

// ID binds validated recovery evidence to a fresh operator approval.
func (r Recovery) ID(p Proposal) (string, error) {
	if _, err := r.Remaining(p); err != nil {
		return "", err
	}
	if Classify(p, &r.Observed) != "UNKNOWN" {
		return "", errors.New("exact before/after state requires reconciliation, not recovery")
	}
	return canonical.Hash("harness.file-recovery.v1", r)
}

// PrepareRecovery observes the entire workspace and only known original temporary
// files. It performs no writes and rejects unrecognized or substituted state.
func PrepareRecovery(ctx context.Context, b worktree.Binding, p Proposal, nonce string) (Recovery, error) {
	if err := p.AdmitHost(); err != nil {
		return Recovery{}, err
	}
	candidate, files, err := worktree.Capture(ctx, b)
	if err != nil {
		return Recovery{}, err
	}
	id, err := p.ID()
	if err != nil {
		return Recovery{}, err
	}
	r := Recovery{Version: 1, Nonce: nonce, OriginalProposalID: id, Observed: candidate, Files: files, Temporary: map[string]string{}}
	byPath := map[string]worktree.FileState{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	root, err := os.OpenRoot(b.Request.Path)
	if err != nil {
		return r, err
	}
	defer root.Close()
	for i, c := range p.Changes {
		temp := temporaryName(id, i)
		if _, exists := byPath[temp]; !exists {
			continue
		}
		approved, err := content(c)
		if err != nil {
			return r, err
		}
		if err = safepath.Check(root, temp, false); err != nil {
			return r, err
		}
		f, err := root.Open(temp)
		if err != nil {
			return r, err
		}
		data, readErr := io.ReadAll(io.LimitReader(f, int64(len(approved))+1))
		closeErr := f.Close()
		if err = errors.Join(readErr, closeErr); err != nil {
			return r, err
		}
		r.Temporary[temp] = base64.StdEncoding.EncodeToString(data)
	}
	_, err = r.Remaining(p)
	return r, err
}

// Recover finishes only the approved residual writes after exact fresh-state
// comparison. The caller must persist this recovery intent and hold the lease.
// A second interruption remains UNKNOWN and needs a new observed approval.
func Recover(ctx context.Context, b worktree.Binding, p Proposal, r Recovery) error {
	if err := p.AdmitHost(); err != nil {
		return err
	}
	remaining, err := r.Remaining(p)
	if err != nil {
		return err
	}
	current, err := worktree.Fingerprint(ctx, b)
	if err != nil {
		return err
	}
	if current != r.Observed {
		return errors.New("recovery observation is stale")
	}
	root, err := os.OpenRoot(b.Request.Path)
	if err != nil {
		return err
	}
	defer root.Close()
	id, err := p.ID()
	if err != nil {
		return err
	}
	for i := range p.Changes {
		temp := temporaryName(id, i)
		encoded, exists := r.Temporary[temp]
		if !exists {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		h, _, _, present, err := safepath.ReadRegular(root, temp, 256<<10)
		if err != nil {
			return err
		}
		data, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		if !present || h != hex.EncodeToString(digest[:]) {
			return errors.New("temporary file changed before cleanup")
		}
		if err = root.Remove(temp); err != nil {
			return err
		}
	}
	return applySelected(ctx, b, p, remaining, nil)
}
