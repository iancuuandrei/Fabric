package draftpr

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/go-github/v89/github"
	"harness.local/engorch/internal/effects"
)

// Create sends one authorized draft request and independently reads its returned
// number. The caller must hold the run lease, establish confirmed push and
// persist the intent before calling. Branch preflight runs before POST. It cannot
// enforce journal uniqueness: an error may follow creation and never permits a
// retry. Reconciliation of a lost response belongs to the controller.
func (c *Client) Create(ctx context.Context, plan Plan, intent effects.Intent, authorization effects.Authorization) (Observation, error) {
	expected, err := plan.Intent()
	if err != nil {
		return Observation{}, err
	}
	if intent != expected {
		return Observation{}, errors.New("draft intent mismatch")
	}
	if err := authorization.Validate(intent); err != nil {
		return Observation{}, err
	}
	payload, err := plan.Request()
	if err != nil {
		return Observation{}, err
	}
	if err := c.Preflight(ctx, plan); err != nil {
		return Observation{}, err
	}
	owner, repository, err := repositoryParts(plan.Repository)
	if err != nil || !c.initialized() {
		return Observation{}, errors.New("uninitialized GitHub client")
	}
	request := &github.NewPullRequest{Title: github.Ptr(payload.Title), Head: github.Ptr(payload.Head), Base: github.Ptr(payload.Base), Body: github.Ptr(payload.Body), Draft: github.Ptr(payload.Draft), MaintainerCanModify: github.Ptr(payload.MaintainerCanModify)}
	pull, response, err := c.api.PullRequests.Create(ctx, owner, repository, request)
	if err != nil {
		return Observation{}, githubError(ctx, err)
	}
	if response == nil || response.Response == nil || response.StatusCode != http.StatusCreated {
		return Observation{}, errors.New("GitHub returned unexpected HTTP status")
	}
	created, err := observationFromPull(plan.Repository, pull)
	if err != nil {
		return Observation{}, err
	}
	if err := created.Validate(plan); err != nil {
		return Observation{}, err
	}
	observed, err := c.Read(context.WithoutCancel(ctx), plan.Repository, created.Number)
	if err != nil {
		return Observation{}, err
	}
	if err := observed.Validate(plan); err != nil {
		return Observation{}, err
	}
	return observed, nil
}
