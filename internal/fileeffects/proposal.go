package fileeffects

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"runtime"
	"sort"
	"strings"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/worktree"
	"harness.local/engorch/internal/writercontract"
)

// Change describes one regular-file state transition. Null before means absent;
// null content means delete. Base64 supports binary files without text coercion.
type Change struct {
	Path          string  `json:"path"`
	BeforeHash    *string `json:"before_hash"`
	ContentBase64 *string `json:"content_base64"`
	Executable    bool    `json:"executable"`
}

// Proposal binds an exact predicted delta and the evidence needed to replay it.
// Nonce prevents an old effect approval from being reused as a new attempt.
type Proposal struct {
	Version     int                  `json:"version"`
	Nonce       string               `json:"nonce"`
	Before      worktree.Candidate   `json:"before"`
	BeforeFiles []worktree.FileState `json:"before_files"`
	Changes     []Change             `json:"changes"`
	After       worktree.Candidate   `json:"after"`
}

func content(c Change) ([]byte, error) {
	if c.ContentBase64 == nil {
		return nil, nil
	}
	if len(*c.ContentBase64) > 350000 {
		return nil, errors.New("content bound exceeded")
	}
	b, err := base64.StdEncoding.Strict().DecodeString(*c.ContentBase64)
	if err != nil {
		return nil, err
	}
	if base64.StdEncoding.EncodeToString(b) != *c.ContentBase64 {
		return nil, errors.New("noncanonical base64")
	}
	return b, nil
}

func predict(before worktree.Candidate, files []worktree.FileState, changes []Change) (worktree.Candidate, error) {
	if _, err := before.ID(); err != nil {
		return before, err
	}
	id, err := worktree.FilesID(files)
	if err != nil {
		return before, err
	}
	if before.FilesHash != id || before.FileCount != len(files) {
		return before, errors.New("before manifest binding mismatch")
	}
	if err := writercontract.ValidateCount(len(changes)); err != nil {
		return before, err
	}
	byPath := map[string]worktree.FileState{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	previous := ""
	total := 0
	for _, c := range changes {
		if err := safepath.Writable(c.Path); err != nil {
			return before, err
		}
		if c.Path <= previous {
			return before, errors.New("changes must be unique and sorted by path")
		}
		previous = c.Path
		old, exists := byPath[c.Path]
		if c.BeforeHash == nil {
			if exists {
				return before, errors.New("expected absent file exists")
			}
		} else {
			if err := safepath.RequireDigest(*c.BeforeHash); err != nil {
				return before, err
			}
			if !exists || old.Hash != *c.BeforeHash {
				return before, errors.New("file precondition mismatch")
			}
		}
		if c.ContentBase64 == nil {
			if !exists || c.Executable {
				return before, errors.New("invalid deletion")
			}
			delete(byPath, c.Path)
			continue
		}
		b, err := content(c)
		if err != nil {
			return before, err
		}
		total += len(b)
		if total > 256<<10 {
			return before, errors.New("proposal content exceeds 256 KiB")
		}
		h := sha256.Sum256(b)
		newState := worktree.FileState{Path: c.Path, Hash: hex.EncodeToString(h[:]), Executable: c.Executable}
		if exists && old == newState {
			return before, errors.New("no-op file change")
		}
		byPath[c.Path] = newState
	}
	afterFiles := make([]worktree.FileState, 0, len(byPath))
	for _, f := range byPath {
		afterFiles = append(afterFiles, f)
	}
	hash, err := worktree.FilesID(afterFiles)
	if err != nil {
		return before, err
	}
	after := before
	after.FilesHash = hash
	after.FileCount = len(afterFiles)
	return after, nil
}

// Validate proves the after candidate follows only from the admitted changes.
// It performs no filesystem access and is suitable for semantic journal replay.
func (p Proposal) Validate() error {
	if p.Version != 1 || strings.TrimSpace(p.Nonce) == "" || len(p.Nonce) > 128 {
		return errors.New("invalid proposal identity")
	}
	after, err := predict(p.Before, p.BeforeFiles, p.Changes)
	if err != nil {
		return err
	}
	if after != p.After {
		return errors.New("predicted candidate mismatch")
	}
	return nil
}

// ID returns the exact content identity of a validated proposal.
func (p Proposal) ID() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	return canonical.Hash("harness.file-proposal.v1", p)
}

// Prepare captures current file evidence and predicts a proposal without writing.
// The controller must compare the captured before candidate with its journal.
func Prepare(ctx context.Context, b worktree.Binding, nonce string, changes []Change) (Proposal, error) {
	before, files, err := worktree.Capture(ctx, b)
	if err != nil {
		return Proposal{}, err
	}
	ordered := append([]Change{}, changes...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	after, err := predict(before, files, ordered)
	if err != nil {
		return Proposal{}, err
	}
	p := Proposal{Version: 1, Nonce: nonce, Before: before, BeforeFiles: files, Changes: ordered, After: after}
	return p, p.AdmitHost()
}

// AdmitHost rejects permissions this host cannot faithfully apply. Pure replay
// remains portable, but execution must honor the proposal's actual mode.
func (p Proposal) AdmitHost() error {
	if err := p.Validate(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		for _, c := range p.Changes {
			if c.Executable {
				return errors.New("executable-bit changes unsupported on Windows")
			}
		}
	}
	return nil
}
