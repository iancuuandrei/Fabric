package draftpr

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/go-github/v89/github"
	"harness.local/engorch/internal/canonical"
)

// Reconcile searches repository PRs without mutation, then independently reads
// the sole marker match. Missing, ambiguous, changed or incomplete observations
// return an error, never permission to retry creation. Pagination is not an
// atomic hosted snapshot; concurrent changes can prevent confirmation.
func (c *Client) Reconcile(ctx context.Context, plan Plan) (Observation, error) {
	intent, err := plan.Intent()
	if err != nil {
		return Observation{}, err
	}
	id, _ := intent.ID()
	marker := markerPrefix + id + " -->"
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	seen := map[int]bool{}
	match := 0
	owner, repository, err := repositoryParts(plan.Repository)
	if err != nil || !c.initialized() {
		return Observation{}, errors.New("uninitialized GitHub client")
	}
	for page := 1; page <= 21; page++ {
		// Scan all states and branches: a changed base or closed duplicate must
		// not disappear behind a head/base/open-state filter.
		pulls, response, err := c.api.PullRequests.List(ctx, owner, repository, &github.PullRequestListOptions{State: "all", Sort: "created", Direction: "asc", ListOptions: github.ListOptions{Page: page, PerPage: 100}})
		if err != nil {
			return Observation{}, githubError(ctx, err)
		}
		if response == nil || response.Response == nil || response.StatusCode != 200 || pulls == nil || len(pulls) > 100 {
			return Observation{}, errors.New("invalid draft reconciliation page")
		}
		items := make([]listedPR, 0, len(pulls))
		for _, pull := range pulls {
			if pull == nil || pull.Number == nil || *pull.Number <= 0 {
				return Observation{}, errors.New("incomplete PR list entry")
			}
			item := listedPR{number: *pull.Number}
			if pull.Body != nil {
				item.body = *pull.Body
			}
			items = append(items, item)
		}
		if len(items) == 0 {
			if match == 0 {
				return Observation{}, errors.New("draft marker not observed; outcome unknown")
			}
			o, err := c.Read(ctx, plan.Repository, match)
			if err != nil {
				return Observation{}, err
			}
			if err := o.Validate(plan); err != nil {
				return Observation{}, err
			}
			return o, nil
		}
		if page == 21 {
			return Observation{}, errors.New("draft reconciliation exceeds 2000 PR bound")
		}
		for _, item := range items {
			if seen[item.number] {
				return Observation{}, errors.New("duplicate PR across reconciliation pages")
			}
			seen[item.number] = true
			if strings.Contains(item.body, marker) {
				if match != 0 {
					return Observation{}, errors.New("ambiguous draft marker")
				}
				match = item.number
			}
		}
	}
	return Observation{}, errors.New("incomplete draft reconciliation")
}

type listedPR struct {
	number int
	body   string
}

func decodePage(raw []byte) ([]listedPR, error) {
	normal, err := canonical.Normalize(raw)
	if err != nil {
		return nil, err
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(normal, &rows); err != nil {
		return nil, err
	}
	if rows == nil || len(rows) > 100 {
		return nil, errors.New("invalid draft reconciliation page")
	}
	items := make([]listedPR, 0, len(rows))
	for _, row := range rows {
		var number int
		if err := projectObject(row, map[string]any{"number": &number}); err != nil {
			return nil, err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(row, &fields); err != nil {
			return nil, err
		}
		body, exists := fields["body"]
		if !exists || number <= 0 {
			return nil, errors.New("incomplete PR list entry")
		}
		var value *string
		if err := json.Unmarshal(body, &value); err != nil {
			return nil, err
		}
		item := listedPR{number: number}
		if value != nil {
			item.body = *value
		}
		items = append(items, item)
	}
	return items, nil
}
