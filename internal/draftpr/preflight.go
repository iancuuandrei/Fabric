package draftpr

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"harness.local/engorch/internal/gitpush"
)

// ReadBranch reads an exact branch reference from the fixed API repository.
// It rejects non-commit objects and partial or ambiguous responses.
func (c *Client) ReadBranch(ctx context.Context, repository, ref string) (BranchObservation, error) {
	if !repositoryName(repository) {
		return BranchObservation{}, errors.New("invalid branch repository")
	}
	if err := gitpush.ValidateTargetRef(ref); err != nil {
		return BranchObservation{}, err
	}
	if !c.initialized() {
		return BranchObservation{}, errors.New("uninitialized GitHub client")
	}
	owner, name, err := repositoryParts(repository)
	if err != nil {
		return BranchObservation{}, err
	}
	reference, response, err := c.api.Git.GetRef(ctx, owner, name, strings.TrimPrefix(ref, "refs/"))
	if err != nil {
		return BranchObservation{}, githubError(ctx, err)
	}
	if response == nil || response.Response == nil || response.StatusCode != http.StatusOK {
		return BranchObservation{}, errors.New("GitHub returned unexpected HTTP status")
	}
	if reference == nil || reference.Ref == nil || reference.Object == nil || reference.Object.Type == nil || reference.Object.SHA == nil {
		return BranchObservation{}, errors.New("incomplete GitHub branch response")
	}
	observed, kind, sha := *reference.Ref, *reference.Object.Type, *reference.Object.SHA
	if observed != ref || kind != "commit" || (len(sha) != 40 && len(sha) != 64) || strings.Trim(sha, "0123456789abcdef") != "" || strings.Trim(sha, "0") == "" {
		return BranchObservation{}, errors.New("branch reference identity mismatch")
	}
	return BranchObservation{Repository: repository, Ref: strings.TrimPrefix(ref, "refs/heads/"), Commit: sha}, nil
}

// Preflight checks both current branch tips against the approved plan. The two
// reads are not atomic and do not replace post-creation observation validation.
func (c *Client) Preflight(ctx context.Context, plan Plan) error {
	if _, err := plan.ID(); err != nil {
		return err
	}
	for _, expected := range []struct{ ref, sha string }{{plan.Push.TargetRef, plan.Push.Candidate.Head}, {plan.BaseRef, plan.BaseCommit}} {
		observed, err := c.ReadBranch(ctx, plan.Repository, expected.ref)
		if err != nil {
			return err
		}
		if observed.Commit != expected.sha {
			return errors.New("draft branch changed before creation")
		}
	}
	return nil
}
