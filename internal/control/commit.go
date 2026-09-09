package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/gitlocal"
	"harness.local/engorch/internal/worktree"
)

// PreparedCommit is a frozen local commit proposal and exact authorization target.
type PreparedCommit struct {
	Plan   gitlocal.CommitPlan `json:"plan"`
	Intent effects.Intent      `json:"intent"`
}

// PrepareCommit captures a ready candidate under lease without writing Git objects
// or refs. Author, committer and message must be explicitly supplied by the caller.
func PrepareCommit(ctx context.Context, path string, author, committer gitlocal.Identity, message string) (prepared PreparedCommit, err error) {
	s, err := Inspect(path)
	if err != nil {
		return prepared, err
	}
	if s.State != "READY" || s.Workspace == nil || s.Candidate == nil || s.WorkspaceOutcome != "CONFIRMED" {
		return prepared, errors.New("ready verified candidate required for commit preparation")
	}
	lease, err := worktree.Acquire(s.Workspace.Request)
	if err != nil {
		return prepared, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(path)
	if err != nil {
		return prepared, err
	}
	if s.State != "READY" || s.Workspace == nil || s.Candidate == nil || s.WorkspaceOutcome != "CONFIRMED" {
		return prepared, errors.New("readiness changed before commit capture")
	}
	candidate, files, err := worktree.Capture(ctx, *s.Workspace)
	if err != nil {
		return prepared, err
	}
	if candidate != *s.Candidate {
		return prepared, errors.New("commit candidate changed since readiness")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return prepared, err
	}
	plan := gitlocal.CommitPlan{Version: 1, Nonce: hex.EncodeToString(nonce[:]), Workspace: *s.Workspace, Candidate: candidate, Files: files, Author: author, Committer: committer, Message: message}
	id, err := plan.ID()
	if err != nil {
		return prepared, err
	}
	repositoryID, err := s.Creation.Repository.ID()
	if err != nil {
		return prepared, err
	}
	intent := effects.Intent{Version: 1, RunID: s.RunID, PlanID: s.PlanID, RepositoryID: repositoryID, Kind: "commit", InputHash: id}
	if _, err := intent.ID(); err != nil {
		return prepared, err
	}
	return PreparedCommit{plan, intent}, nil
}
