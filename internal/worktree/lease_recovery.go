package worktree

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/safepath"
)

// LeaseRecovery binds operator-supplied quiescence evidence and the observed
// token digest. Quiescence is an attestation, not inferred process-tree proof.
type LeaseRecovery struct {
	PreviousIntentID string  `json:"previous_intent_id,omitempty"`
	Version          int     `json:"version"`
	Request          Request `json:"request"`
	Nonce            string  `json:"nonce"`
	TokenSHA256      string  `json:"token_sha256"`
	Evidence         string  `json:"evidence"`
	WorkloadsStopped bool    `json:"workloads_stopped"`
}

// ID validates and hashes token, workspace and operator evidence bindings.
func (p LeaseRecovery) ID() (string, error) {
	if p.PreviousIntentID != "" {
		if err := safepath.RequireDigest(p.PreviousIntentID); err != nil {
			return "", err
		}
	}
	if err := p.Request.Validate(); err != nil {
		return "", err
	}
	if err := safepath.RequireDigest(p.TokenSHA256); err != nil {
		return "", err
	}
	if p.Version != 1 || !p.WorkloadsStopped || strings.TrimSpace(p.Nonce) == "" || len(p.Nonce) > 128 || strings.TrimSpace(p.Evidence) == "" || len(p.Evidence) > 4096 || !utf8.ValidString(p.Evidence) {
		return "", errors.New("explicit bounded lease recovery evidence required")
	}
	return canonical.Hash("harness.lease-recovery.v1", p)
}

// PrepareLeaseRecovery observes token bytes without acquiring or removing a lock.
func PrepareLeaseRecovery(r Request, nonce, evidence string, stopped bool) (LeaseRecovery, error) {
	if err := r.Validate(); err != nil {
		return LeaseRecovery{}, err
	}
	rootPath, _ := leaseRoot(r)
	if err := safepath.Directory(rootPath); err != nil {
		return LeaseRecovery{}, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return LeaseRecovery{}, err
	}
	var token bytes.Buffer
	digest, size, _, exists, readErr := safepath.CopyRegular(root, leaseName(r), 64, &token)
	if err := errors.Join(readErr, root.Close()); err != nil {
		return LeaseRecovery{}, err
	}
	if !exists || size != 64 || safepath.RequireDigest(token.String()) != nil {
		return LeaseRecovery{}, errors.New("invalid lease token")
	}
	p := LeaseRecovery{Version: 1, Request: r, Nonce: nonce, TokenSHA256: digest, Evidence: evidence, WorkloadsStopped: stopped}
	_, err = p.ID()
	return p, err
}

// AdoptLease requires a persisted recovery intent and explicit authorization.
// It takes kernel exclusion on the original file, verifies token and identity,
// and returns ownership without deleting/replacing it. Close releases this lease.
// Kernel exclusion cannot prove that an orphaned Git child has stopped.
func AdoptLease(p LeaseRecovery, intent effects.Intent, auth effects.Authorization) (*Lease, error) {
	id, err := p.ID()
	if err != nil {
		return nil, err
	}
	repositoryID, err := p.Request.Source.ID()
	if err != nil {
		return nil, err
	}
	if intent.Kind != "lease_recovery" || intent.InputHash != id || intent.RunID != p.Request.RunID || intent.RepositoryID != repositoryID {
		return nil, errors.New("lease recovery intent mismatch")
	}
	if err := auth.Validate(intent); err != nil {
		return nil, err
	}
	guard, guardPath, err := acquireLeaseGuard(p.Request, false)
	if err != nil {
		return nil, err
	}
	guardAccepted := false
	defer func() {
		if !guardAccepted {
			guard.Close()
		}
	}()
	observed, err := PrepareLeaseRecovery(p.Request, p.Nonce, p.Evidence, p.WorkloadsStopped)
	if err != nil {
		return nil, err
	}
	observed.PreviousIntentID = p.PreviousIntentID
	if observed != p {
		return nil, errors.New("lease changed before adoption")
	}
	rootPath, _ := leaseRoot(p.Request)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name := leaseName(p.Request)
	if err := safepath.Check(root, name, false); err != nil {
		return nil, err
	}
	f, err := root.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	accepted := false
	defer func() {
		if !accepted {
			f.Close()
		}
	}()
	if err := lockLeaseFile(f); err != nil {
		return nil, err
	}
	token, err := io.ReadAll(io.LimitReader(f, 65))
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(token)
	if len(token) != 64 || hex.EncodeToString(digest[:]) != p.TokenSHA256 {
		return nil, errors.New("lease token changed during adoption")
	}
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	current, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(opened, current) {
		return nil, errors.New("lease file substituted during adoption")
	}
	accepted = true
	guardAccepted = true
	return &Lease{file: f, guard: guard, guardPath: guardPath, path: LeasePath(p.Request), token: string(token), request: p.Request}, nil
}
